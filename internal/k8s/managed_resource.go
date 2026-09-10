package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const AnnotationSSACliMigration = "middleware.cn/ssa-cli-migration"
const AnnotationSSACliConverged = "middleware.cn/ssa-cli-converged"
const migrationManager = "opensaola-managedfields-migration"

var managedResourceReader client.Reader

type ManagedResourceWriter struct {
	Client client.Client
	Reader client.Reader
}

// ConfigureManagedResources installs the direct reader before starting controllers.
func ConfigureManagedResources(reader client.Reader) {
	managedResourceReader = reader
}
func NewManagedResourceWriter(cli client.Client) *ManagedResourceWriter {
	reader := managedResourceReader
	if reader == nil {
		reader = cli
	}
	return &ManagedResourceWriter{Client: cli, Reader: reader}
}
func migrationBlocked(format string, args ...any) error {
	return fmt.Errorf("SSA compatibility blocked: "+format, args...)
}
func verifyManagedIdentity(owner client.Object, live *unstructured.Unstructured) error {
	kind := ""
	switch owner.(type) {
	case *v1.Middleware:
		kind = "Middleware"
	case *v1.MiddlewareOperator:
		kind = "MiddlewareOperator"
	default:
		return migrationBlocked("unsupported owner %T", owner)
	}
	if owner.GetUID() == "" || live.GetUID() == "" || owner.GetDeletionTimestamp() != nil || live.GetDeletionTimestamp() != nil {
		return migrationBlocked("missing UID or object deleting")
	}
	ref := metav1.GetControllerOf(live)
	if ref == nil {
		return migrationBlocked("primary resource lacks controller owner")
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || gv.Group != v1.GroupVersion.Group || ref.Kind != kind || ref.Name != owner.GetName() || ref.UID != owner.GetUID() || live.GetNamespace() != owner.GetNamespace() {
		return migrationBlocked("primary resource owner mismatch")
	}
	return nil
}
func cleanApplyObject(desired *unstructured.Unstructured) *unstructured.Unstructured {
	result := desired.DeepCopy()
	result.SetManagedFields(nil)
	result.SetResourceVersion("")
	result.SetUID("")
	delete(result.Object, "status")
	for _, key := range []string{"creationTimestamp", "deletionTimestamp", "deletionGracePeriodSeconds", "generation", "selfLink"} {
		unstructured.RemoveNestedField(result.Object, "metadata", key)
	}
	annotations := result.GetAnnotations()
	for _, key := range []string{AnnotationSSACliMigration, AnnotationSSACliConverged, AnnotationSSACompatible} {
		delete(annotations, key)
	}
	result.SetAnnotations(annotations)
	return result
}

// Reconcile never rewrites managedFields. CLI migration is a separate upgrade step.
// compatibilityOnly applies existing resources only when the CLI has migrated them.
func (w *ManagedResourceWriter) Reconcile(ctx context.Context, owner client.Object, desired *unstructured.Unstructured, configuration string, compatibilityOnly bool) error {
	if desired == nil {
		return nil
	}
	for attempt := 0; attempt < 5; attempt++ {
		fresh := owner.DeepCopyObject().(client.Object)
		if err := w.Reader.Get(ctx, client.ObjectKeyFromObject(owner), fresh); err != nil {
			return err
		}
		if fresh.GetUID() != owner.GetUID() || fresh.GetGeneration() != owner.GetGeneration() || fresh.GetDeletionTimestamp() != nil {
			return migrationBlocked("owner changed or deleting")
		}
		switch o := fresh.(type) {
		case *v1.Middleware:
			if v1.IsMiddlewareReconcileWriteSuspended(o.Annotations, o.Status.Conditions) {
				return migrationBlocked("Middleware paused")
			}
		case *v1.MiddlewareOperator:
			if v1.IsReconcileSuspended(o.Annotations) {
				return migrationBlocked("MiddlewareOperator paused")
			}
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(desired.GroupVersionKind())
		err := w.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
		var trustedUID types.UID
		if apierrors.IsNotFound(err) {
			if compatibilityOnly && (configuration != "" || isImmutableResource(desired)) {
				log.FromContext(ctx).Info("managed resource missing in steady state; leaving it absent until the next owner update",
					"gvk", desired.GroupVersionKind().String(), "namespace", desired.GetNamespace(), "name", desired.GetName(), "configuration", configuration)
				return w.recordCompatibility(ctx, owner, desired, configuration, true, "")
			}
			intent := cleanApplyObject(desired)
			if isImmutableResource(intent) {
				err = w.Client.Create(ctx, intent)
			} else {
				err = w.Client.Patch(ctx, intent, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership)
			}
			trustedUID = intent.GetUID()
		} else if err == nil {
			trustedUID = live.GetUID()
			if configuration == "" {
				if err = verifyManagedIdentity(fresh, live); err != nil {
					return err
				}
			}
			if !isImmutableResource(live) && (!compatibilityOnly || live.GetAnnotations()[AnnotationSSACliMigration] != "") {
				intent := cleanApplyObject(desired)
				intent.SetResourceVersion(live.GetResourceVersion())
				err = w.Client.Patch(ctx, intent, client.Apply, client.FieldOwner(v1.FieldOwner), client.ForceOwnership)
				if err == nil {
					err = w.markCLIConverged(ctx, intent)
				}
			} else if compatibilityOnly && !isImmutableResource(live) {
				recordMigrationPending(ctx, live)
			}
		}
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return err
		}
		return w.recordCompatibility(ctx, owner, desired, configuration, false, trustedUID)
	}
	return fmt.Errorf("resource changed during all SSA retries")
}

// selectApplyEntry picks the OpenSaola Apply entry for the converged hash.
// Multiple entries can coexist briefly across API version migrations; prefer the
// object's current API version, then the lowest version, so the controller and
// saola CLI hash the same entry. Keep in sync with the CLI.
func selectApplyEntry(live *unstructured.Unstructured) (metav1.ManagedFieldsEntry, bool) {
	var entries []metav1.ManagedFieldsEntry
	for _, f := range live.GetManagedFields() {
		if f.Manager == v1.FieldOwner && f.Operation == metav1.ManagedFieldsOperationApply && f.Subresource == "" {
			entries = append(entries, f)
		}
	}
	if len(entries) == 0 {
		return metav1.ManagedFieldsEntry{}, false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].APIVersion < entries[j].APIVersion })
	for _, f := range entries {
		if f.APIVersion == live.GetAPIVersion() {
			return f, true
		}
	}
	return entries[0], true
}

// markCLIConverged records the actual Apply workflow, including normal API defaulting.
func (w *ManagedResourceWriter) markCLIConverged(ctx context.Context, live *unstructured.Unstructured) error {
	receipt := live.GetAnnotations()[AnnotationSSACliMigration]
	if receipt == "" {
		return nil
	}
	hash := ""
	if entry, ok := selectApplyEntry(live); ok {
		raw, _ := json.Marshal(struct {
			APIVersion string           `json:"apiVersion"`
			FieldsV1   *metav1.FieldsV1 `json:"fieldsV1"`
		}{entry.APIVersion, entry.FieldsV1})
		hash = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	}
	if hash == "" {
		return migrationBlocked("successful Apply has no workflow")
	}
	raw, _ := json.Marshal(struct {
		Migration         string `json:"migration"`
		AppliedFieldsHash string `json:"appliedFieldsHash"`
	}{receipt, hash})
	if live.GetAnnotations()[AnnotationSSACliConverged] == string(raw) {
		return nil
	}
	before := live.DeepCopy()
	annotations := live.GetAnnotations()
	annotations[AnnotationSSACliConverged] = string(raw)
	live.SetAnnotations(annotations)
	return w.Client.Patch(ctx, live, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(migrationManager))
}
