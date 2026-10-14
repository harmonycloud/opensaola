/*
Copyright 2025 The OpenSaola Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ResourceIntentCollector uses a direct API reader. The manager's Secret cache
// deliberately excludes these records and strips Secret data from other entries.
type ResourceIntentCollector struct {
	Client   client.Client
	Reader   client.Reader
	Interval time.Duration
	MinAge   time.Duration
}

func NewResourceIntentCollector(cli client.Client, reader client.Reader) *ResourceIntentCollector {
	return &ResourceIntentCollector{Client: cli, Reader: reader, Interval: 10 * time.Minute, MinAge: 30 * time.Minute}
}

func (*ResourceIntentCollector) NeedLeaderElection() bool { return true }

func (c *ResourceIntentCollector) Start(ctx context.Context) error {
	if c.Client == nil || c.Reader == nil || c.Interval <= 0 || c.MinAge < 0 {
		return fmt.Errorf("invalid resource intent collector configuration")
	}
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		if _, err := c.Sweep(ctx); err != nil && ctx.Err() == nil {
			log.FromContext(ctx).Error(err, "resource intent collection incomplete; retrying next scan")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Counts describe this scan, not business-resource convergence. Kept includes
// current, young and unprovable records; Failed remains eligible for retry.
type IntentGCResult struct{ Scanned, Deleted, Kept, Failed int }

// Sweep processes bounded pages sequentially so failed collection never creates
// unbounded API traffic or blocks middleware reconciliation. It can be called
// directly by integration tests against their dedicated local cluster.
func (c *ResourceIntentCollector) Sweep(ctx context.Context) (IntentGCResult, error) {
	var result IntentGCResult
	var failures []error
	continuation := ""
	for {
		var records corev1.SecretList
		readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := c.Reader.List(readCtx, &records, client.HasLabels{intentRecordLabel}, client.Limit(50), client.Continue(continuation))
		cancel()
		if err != nil {
			return result, fmt.Errorf("list resource intent records: %w", err)
		}
		for i := range records.Items {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			result.Scanned++
			recordCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			deleted, err := c.collect(recordCtx, &records.Items[i], time.Now())
			cancel()
			if err != nil {
				result.Failed++
				// Neither errors nor logs contain intent.json or rendered values.
				log.FromContext(ctx).Error(err, "resource intent record retained", "namespace", records.Items[i].Namespace, "record", records.Items[i].Name)
				if len(failures) < 3 {
					failures = append(failures, err)
				}
			} else if deleted {
				result.Deleted++
			} else {
				result.Kept++
			}
		}
		continuation = records.Continue
		if continuation == "" {
			break
		}
	}
	log.FromContext(ctx).Info("resource intent collection completed", "scanned", result.Scanned, "deleted", result.Deleted, "kept", result.Kept, "failed", result.Failed)
	return result, errors.Join(failures...)
}

func (c *ResourceIntentCollector) collect(ctx context.Context, secret *corev1.Secret, now time.Time) (bool, error) {
	if secret.DeletionTimestamp != nil || secret.CreationTimestamp.IsZero() || now.Sub(secret.CreationTimestamp.Time) < c.MinAge {
		return false, nil
	}
	if secret.UID == "" || secret.ResourceVersion == "" || secret.Immutable == nil || !*secret.Immutable || secret.Type != corev1.SecretTypeOpaque {
		return false, managedWriteBlocked("invalid intent collection identity")
	}
	record, err := decodeResourceIntent(secret.Data)
	if err != nil || record.Version != 1 || record.Desired == nil || record.OwnerUID == "" || record.ResourceUID == "" || record.Pause == nil && record.SchemaHash == "" {
		return false, managedWriteBlocked("invalid intent collection record")
	}
	ref := metav1.GetControllerOf(secret)
	if ref == nil || ref.UID != record.OwnerUID || secret.Labels[intentRecordLabel] != string(record.OwnerUID) || ref.APIVersion != v1.GroupVersion.String() || ref.Name == "" {
		return false, managedWriteBlocked("intent collection owner mismatch")
	}
	var owner client.Object
	switch ref.Kind {
	case "Middleware":
		owner = &v1.Middleware{}
	case "MiddlewareOperator":
		owner = &v1.MiddlewareOperator{}
	default:
		return false, managedWriteBlocked("unsupported intent collection owner")
	}
	if err := c.Reader.Get(ctx, client.ObjectKey{Namespace: secret.Namespace, Name: ref.Name}, owner); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		} // owner garbage collection
		return false, err
	}
	if owner.GetUID() != ref.UID || owner.GetDeletionTimestamp() != nil {
		return false, nil
	}
	// Records created before the single-RV publication protocol may be reused
	// by an old writer. Never infer their safety from age or the current pointer.
	if record.PublicationProtocol != intentPublicationProtocol || record.BaseResourceVersion == "" {
		return false, nil
	}
	if record.Pause != nil {
		return c.collectPauseSnapshot(ctx, secret, record, owner)
	}
	gvk := record.Desired.GroupVersionKind()
	if gvk.Version == "" || gvk.Kind == "" || record.Desired.GetName() == "" {
		return false, managedWriteBlocked("invalid intent target")
	}
	mapping, err := c.Client.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, fmt.Errorf("resolve intent target API: %w", err)
	}
	if (mapping.Scope.Name() == meta.RESTScopeNameNamespace) != (record.Desired.GetNamespace() != "") {
		return false, managedWriteBlocked("intent target namespace does not match API scope")
	}
	target := &unstructured.Unstructured{}
	target.SetGroupVersionKind(gvk)
	if err := c.Reader.Get(ctx, client.ObjectKeyFromObject(record.Desired), target); err != nil {
		// An endpoint/CRD 404 is not evidence that this particular object is gone.
		var status apierrors.APIStatus
		if apierrors.IsNotFound(err) && errors.As(err, &status) {
			details := status.Status().Details
			if details != nil && details.Name == record.Desired.GetName() && details.Group == mapping.Resource.Group && details.Kind == mapping.Resource.Resource {
				return c.deleteRecord(ctx, secret)
			}
		}
		return false, fmt.Errorf("read intent target: %w", err)
	}
	if target.GetAnnotations()[AnnotationResourceIntent] == secret.Name {
		return false, nil
	}
	if target.GetUID() == "" || target.GetResourceVersion() == "" {
		return false, managedWriteBlocked("intent target lacks UID or resourceVersion")
	}
	if target.GetUID() != record.ResourceUID {
		return c.deleteRecord(ctx, secret)
	}
	if err := verifyIntentIdentity(owner, target, record.Desired, record.Configuration); err != nil {
		return false, err
	}
	if target.GetResourceVersion() == record.BaseResourceVersion {
		if err := c.fencePublication(ctx, target, secret); err != nil {
			return false, err
		}
	}
	// Either the object's RV already changed, or a metadata-only fence changed
	// it. All delayed publications of THIS record now fail their UID/RV guard.
	return c.deleteRecord(ctx, secret)
}

// Pause references are published in MID status, not in the primary CR. Fence
// the MID RV before deleting an abandoned preparation; never collect an active
// session, even if it has been paused for longer than MinAge.
func (c *ResourceIntentCollector) collectPauseSnapshot(ctx context.Context, secret *corev1.Secret, record *resourceIntent, owner client.Object) (bool, error) {
	mid, ok := owner.(*v1.Middleware)
	if !ok || mid.ResourceVersion == "" {
		return false, invalidPauseSnapshot("invalid collection owner")
	}
	gvk := record.Desired.GroupVersionKind()
	hash, err := pauseRecordHash(record)
	if err != nil {
		return false, err
	}
	snapshot := &v1.ReconcilePauseStatus{
		SnapshotSecret: secret.Name, Hash: hash, Generation: record.Pause.Generation,
		GVK:  v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind},
		Name: record.Desired.GetName(), Namespace: record.Desired.GetNamespace(), ResourceUID: string(record.ResourceUID),
	}
	w := &ManagedResourceWriter{Client: c.Client, Reader: c.Reader}
	if _, err := w.LoadReconcilePauseSnapshot(ctx, mid, snapshot); err != nil {
		return false, err
	}
	if mid.Status.ReconcilePause != nil && mid.Status.ReconcilePause.SnapshotSecret == secret.Name {
		return false, nil
	}
	if mid.ResourceVersion == record.BaseResourceVersion {
		// Read the exact API representation. Converting a typed MID can insert
		// omitted empty structs/nulls and make a metadata-only dry-run appear
		// to have modified business fields after API pruning.
		target := &unstructured.Unstructured{}
		target.SetGroupVersionKind(v1.GroupVersion.WithKind("Middleware"))
		if err := c.Reader.Get(ctx, client.ObjectKeyFromObject(mid), target); err != nil {
			return false, err
		}
		if target.GetUID() != mid.UID || target.GetResourceVersion() != mid.ResourceVersion {
			return false, invalidPauseSnapshot("owner changed before collection fence; retry collection")
		}
		if err := c.fencePublication(ctx, target, secret); err != nil {
			return false, err
		}
	}
	return c.deleteRecord(ctx, secret)
}

func (c *ResourceIntentCollector) deleteRecord(ctx context.Context, secret *corev1.Secret) (bool, error) {
	uid, rv := secret.UID, secret.ResourceVersion
	err := c.Client.Delete(ctx, secret, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

func (c *ResourceIntentCollector) fencePublication(ctx context.Context, target *unstructured.Unstructured, secret *corev1.Secret) error {
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(target.GetUID())},
		{"op": "test", "path": "/metadata/resourceVersion", "value": target.GetResourceVersion()},
	}
	if target.GetLabels() == nil {
		operations = append(operations, map[string]any{"op": "add", "path": "/metadata/labels", "value": map[string]string{LabelResourceIntentGCFence: string(secret.UID)}})
	} else {
		operations = append(operations, map[string]any{"op": "add", "path": "/metadata/labels/middleware.cn~1resource-intent-gc-fence", "value": string(secret.UID)})
	}
	raw, err := json.Marshal(operations)
	if err != nil {
		return err
	}
	patch := client.RawPatch(types.JSONPatchType, raw)
	preview := target.DeepCopy()
	if err := c.Client.Patch(ctx, preview, patch, client.FieldOwner(v1.FieldOwner), client.DryRunAll); err != nil {
		return err
	}
	if !intentFenceUnchanged(target, preview) {
		return managedWriteBlocked("admission changed business fields in collection dry-run")
	}
	fenced := target.DeepCopy()
	if err := c.Client.Patch(ctx, fenced, patch, client.FieldOwner(v1.FieldOwner)); err != nil {
		return err
	}
	if fenced.GetResourceVersion() == target.GetResourceVersion() || !intentFenceUnchanged(target, fenced) {
		return managedWriteBlocked("intent publication fence could not be confirmed")
	}
	return nil
}

func intentFenceUnchanged(before, after *unstructured.Unstructured) bool {
	return before.GetUID() == after.GetUID() && before.GetGeneration() == after.GetGeneration() &&
		before.GetAnnotations()[AnnotationResourceIntent] == after.GetAnnotations()[AnnotationResourceIntent] &&
		reflect.DeepEqual(before.GetOwnerReferences(), after.GetOwnerReferences()) &&
		reflect.DeepEqual(before.GetFinalizers(), after.GetFinalizers()) &&
		reflect.DeepEqual(before.Object["status"], after.Object["status"]) &&
		reflect.DeepEqual(intentObject(before).Object, intentObject(after).Object)
}
