package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const AnnotationSSACompatible = "middleware.cn/ssa-compatible-generation"

type compatibleResource struct {
	APIVersion    string    `json:"apiVersion"`
	Kind          string    `json:"kind"`
	Namespace     string    `json:"namespace,omitempty"`
	Name          string    `json:"name"`
	UID           types.UID `json:"uid"`
	Configuration string    `json:"configuration,omitempty"`
	Receipt       string    `json:"receipt,omitempty"`
}
type compatibilitySeal struct {
	Version    int                  `json:"version"`
	OwnerUID   types.UID            `json:"ownerUID"`
	Generation int64                `json:"generation"`
	Resources  []compatibleResource `json:"resources"`
}
type compatibilityTrackingKey struct{}
type compatibilityTracking struct {
	sync.Mutex
	resources []compatibleResource
	pending   []string
}

// WithSSACompatibilityTracking collects exactly the resources successfully checked
// or written during this reconcile. A failed reconcile never publishes its seal.
func WithSSACompatibilityTracking(ctx context.Context) context.Context {
	return context.WithValue(ctx, compatibilityTrackingKey{}, &compatibilityTracking{})
}
func compatibilityRecord(owner client.Object) (compatibilitySeal, bool) {
	var seal compatibilitySeal
	err := json.Unmarshal([]byte(owner.GetAnnotations()[AnnotationSSACompatible]), &seal)
	return seal, err == nil && seal.Version == 2 && owner.GetUID() != "" && seal.OwnerUID == owner.GetUID() && seal.Generation == owner.GetGeneration()
}
func SSACompatibilityCurrent(owner client.Object) bool {
	_, ok := compatibilityRecord(owner)
	return ok
}

// CheckSSACompatibility checks the physical resources before trusting a completed
// owner generation. In particular, old-version recreation must invalidate the seal.
func CheckSSACompatibility(ctx context.Context, cli client.Client, owner client.Object) (bool, error) {
	seal, ok := compatibilityRecord(owner)
	if !ok {
		return false, nil
	}
	reader := NewManagedResourceWriter(cli).Reader
	for _, ref := range seal.Resources {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind))
		err := reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, obj)
		if apierrors.IsNotFound(err) {
			if ref.UID == "" {
				continue
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if ref.Configuration == "" {
			if err := verifyManagedIdentity(owner, obj); err != nil {
				return false, err
			}
		}
		if obj.GetUID() != ref.UID || obj.GetDeletionTimestamp() != nil || obj.GetAnnotations()[AnnotationSSACliMigration] != ref.Receipt || ref.Receipt != "" && obj.GetAnnotations()[AnnotationSSACliConverged] == "" {
			return false, nil
		}
		// A sealed resource with legacy OpenSaola Update ownership but no CLI
		// migration receipt is gated in steady state. Surface it so the freeze
		// is visible. Only the pre-fix manager name counts as legacy evidence.
		if ref.Receipt == "" {
			for _, mf := range obj.GetManagedFields() {
				if mf.Operation == metav1.ManagedFieldsOperationUpdate && mf.Subresource == "" && mf.Manager == legacyFieldOwner {
					recordMigrationPending(ctx, obj)
					break
				}
			}
		}
	}
	if tracking, ok := ctx.Value(compatibilityTrackingKey{}).(*compatibilityTracking); ok {
		tracking.Lock()
		tracking.resources = append(tracking.resources, seal.Resources...)
		tracking.Unlock()
	}
	return true, nil
}

// legacyFieldOwner is the field manager name OpenSaola used before the SSA fix;
// middleware operators share it, so pending detection keys on it exactly.
const legacyFieldOwner = "manager"

// SteadyStateSSAReady reports whether this reconcile is a no-change pass that
// should only run the gated compatibility path. An attached upgrade annotation
// means the next reconcile is an update, so the compat pass would be wasted.
func SteadyStateSSAReady(generation, observedGeneration int64, state v1.State, annotations map[string]string, wasReconcilePaused bool) bool {
	if observedGeneration == 0 || generation != observedGeneration || state == v1.StateUpdating || wasReconcilePaused {
		return false
	}
	_, hasUpdate := annotations[v1.LabelUpdate]
	return !hasUpdate
}

// RunSSACompatibility executes the steady-state compatibility reconcile and
// surfaces failures and pending CLI migration as Events on the owner.
func RunSSACompatibility(ctx context.Context, recorder record.EventRecorder, owner client.Object, reconcile func(context.Context) error) error {
	if err := reconcile(ctx); err != nil {
		recorder.Event(owner, "Warning", "SSACompatibilityFailed", err.Error())
		return err
	}
	if pending := SSAMigrationPendingFrom(ctx); len(pending) > 0 {
		recorder.Eventf(owner, "Warning", "SSAMigrationPending", "%d resource(s) lack CLI migration evidence; steady-state apply stays gated until 'saola migrate ssa' runs or an update adopts them: %s", len(pending), SummarizeMigrationPending(pending))
	}
	return nil
}

// recordMigrationPending notes an existing resource skipped in compatibility mode
// because CLI migration evidence is missing. It self-heals on the next owner update.
func recordMigrationPending(ctx context.Context, live *unstructured.Unstructured) {
	tracking, ok := ctx.Value(compatibilityTrackingKey{}).(*compatibilityTracking)
	if !ok {
		return
	}
	tracking.Lock()
	tracking.pending = append(tracking.pending, fmt.Sprintf("%s %s/%s", live.GetKind(), live.GetNamespace(), live.GetName()))
	tracking.Unlock()
}

// SSAMigrationPendingFrom returns resources skipped this reconcile for missing
// CLI migration evidence, oldest first. Callers surface them via Events.
func SSAMigrationPendingFrom(ctx context.Context) []string {
	tracking, ok := ctx.Value(compatibilityTrackingKey{}).(*compatibilityTracking)
	if !ok {
		return nil
	}
	tracking.Lock()
	defer tracking.Unlock()
	return append([]string{}, tracking.pending...)
}

// SummarizeMigrationPending compacts the pending list for an Event message.
func SummarizeMigrationPending(pending []string) string {
	const maxShown = 3
	if len(pending) <= maxShown {
		return strings.Join(pending, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(pending[:maxShown], ", "), len(pending)-maxShown)
}

func (w *ManagedResourceWriter) recordCompatibility(ctx context.Context, owner client.Object, desired *unstructured.Unstructured, configuration string, allowMissing bool, trustedUID types.UID) error {
	tracking, ok := ctx.Value(compatibilityTrackingKey{}).(*compatibilityTracking)
	if !ok {
		return nil
	}
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(desired.GroupVersionKind())
	err := w.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
	if err != nil && (!apierrors.IsNotFound(err) || !allowMissing) {
		return err
	}
	ref := compatibleResource{APIVersion: desired.GetAPIVersion(), Kind: desired.GetKind(), Namespace: desired.GetNamespace(), Name: desired.GetName(), Configuration: configuration}
	if err == nil {
		if trustedUID == "" || live.GetUID() != trustedUID {
			return migrationBlocked("resource replaced before recording compatibility")
		}
		if configuration == "" {
			if err := verifyManagedIdentity(owner, live); err != nil {
				return err
			}
		}
		ref.Receipt = live.GetAnnotations()[AnnotationSSACliMigration]
		ref.UID = live.GetUID()
	}
	tracking.Lock()
	tracking.resources = append(tracking.resources, ref)
	tracking.Unlock()
	return nil
}

// MarkSSACompatible is called only after all managed writes/checks succeed.
func MarkSSACompatible(ctx context.Context, cli client.Client, owner client.Object) error {
	tracking, ok := ctx.Value(compatibilityTrackingKey{}).(*compatibilityTracking)
	if !ok {
		return migrationBlocked("resource tracking missing before recording compatibility")
	}
	tracking.Lock()
	resources := append([]compatibleResource{}, tracking.resources...)
	tracking.Unlock()
	seal := compatibilitySeal{Version: 2, OwnerUID: owner.GetUID(), Generation: owner.GetGeneration(), Resources: resources}
	raw, err := json.Marshal(seal)
	if err != nil {
		return err
	}
	if owner.GetAnnotations()[AnnotationSSACompatible] == string(raw) {
		return nil
	}
	fresh := owner.DeepCopyObject().(client.Object)
	if err := NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKeyFromObject(owner), fresh); err != nil {
		return err
	}
	if fresh.GetUID() != owner.GetUID() || fresh.GetGeneration() != owner.GetGeneration() {
		return migrationBlocked("owner changed before recording SSA compatibility")
	}
	before := fresh.DeepCopyObject().(client.Object)
	annotations := fresh.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationSSACompatible] = string(raw)
	fresh.SetAnnotations(annotations)
	return cli.Patch(ctx, fresh, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldOwner(migrationManager))
}
