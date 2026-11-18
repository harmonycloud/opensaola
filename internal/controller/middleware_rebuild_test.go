/*
Copyright 2026 The OpenSaola Authors.

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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middleware"
	"github.com/harmonycloud/opensaola/internal/service/middlewareaction"
	"github.com/harmonycloud/opensaola/internal/service/watcher"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type rebuildFixture struct {
	ctx      context.Context
	cli      client.Client
	r        *MiddlewareReconciler
	recorder *record.FakeRecorder
	mid      *v1.Middleware
	baseline *v1.MiddlewareBaseline
	primary  *unstructured.Unstructured
	creates  int
	patches  int
	failNext bool
}

func newRebuildFixture(t *testing.T) *rebuildFixture {
	t.Helper()
	k8s.ConfigureManagedResources(nil)
	k8s.ConfigureResourceIntents(controllerPauseTestSchemas{})
	t.Cleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	gvk := schema.GroupVersionKind{Group: "rebuild.test.io", Version: "v1", Kind: "Database"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("DatabaseList"), &unstructured.UnstructuredList{})
	labels := map[string]string{v1.LabelPackageName: t.Name()}
	f := &rebuildFixture{ctx: t.Context(), recorder: record.NewFakeRecorder(50)}
	f.mid = &v1.Middleware{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1.GroupVersion.String(), Kind: "Middleware"},
		ObjectMeta: metav1.ObjectMeta{Name: "stable", Namespace: "test", UID: "mid-1", Generation: 7, Finalizers: []string{v1.FinalizerMiddleware}, Labels: labels},
		Spec:       v1.MiddlewareSpec{Baseline: "rebuild", Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":1}`)}},
		Status: v1.MiddlewareStatus{State: v1.StateAvailable, ObservedGeneration: 7, Conditions: []metav1.Condition{
			{Type: v1.CondTypeChecked, Status: metav1.ConditionTrue, ObservedGeneration: 7, Reason: "CheckedSuccess", LastTransitionTime: metav1.Now()},
			{Type: v1.CondTypeApplyCluster, Status: metav1.ConditionTrue, ObservedGeneration: 7, Reason: "ApplyClusterSuccess", LastTransitionTime: metav1.Now()},
		}},
	}
	f.baseline = &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: "rebuild", Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}}}
	f.primary = &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind, "metadata": map[string]any{"name": "stable", "namespace": "test", "uid": "old-primary"}, "spec": map[string]any{"replicas": int64(1)}}}
	if err := ctrl.SetControllerReference(f.mid, f.primary, scheme); err != nil {
		t.Fatal(err)
	}
	f.cli = fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1.Middleware{}).WithObjects(f.mid, f.baseline, f.primary).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, next client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := next.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			// Typed fake reads clear TypeMeta; real API reads used by CUE
			// rendering include the Middleware kind and API version.
			if _, ok := obj.(*v1.Middleware); ok {
				obj.GetObjectKind().SetGroupVersionKind(v1.GroupVersion.WithKind("Middleware"))
			}
			return nil
		},
		Create: func(ctx context.Context, next client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetObjectKind().GroupVersionKind() == gvk && len((&client.CreateOptions{}).ApplyOptions(opts).DryRun) == 0 {
				f.creates++
				if f.failNext {
					f.failNext = false
					return errors.New("injected primary write failure")
				}
				obj.SetUID("rebuilt-primary")
			}
			return next.Create(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, next client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if obj.GetObjectKind().GroupVersionKind() != gvk {
				return next.Patch(ctx, obj, patch, opts...)
			}
			// A fake client's dry-run does not populate the accepted object.
			// Apply the same patch to an isolated copy for that response.
			if len((&client.PatchOptions{}).ApplyOptions(opts).DryRun) > 0 {
				if f.failNext {
					f.failNext = false
					f.patches++
					return errors.New("injected primary write failure")
				}
				live := &unstructured.Unstructured{}
				live.SetGroupVersionKind(gvk)
				if err := next.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
					return err
				}
				preview := fake.NewClientBuilder().WithScheme(scheme).WithObjects(live).Build()
				return preview.Patch(ctx, obj, patch)
			}
			if f.creates == 0 {
				f.patches++
			} else {
				// Only the history receipt may be published after create-only recovery.
				data, err := patch.Data(obj)
				if err != nil {
					return err
				}
				var operations []struct{ Path string }
				if err := json.Unmarshal(data, &operations); err != nil {
					return err
				}
				for _, op := range operations {
					if op.Path == "/spec" || strings.HasPrefix(op.Path, "/spec/") {
						f.patches++
						break
					}
				}
			}
			return next.Patch(ctx, obj, patch, opts...)
		},
	}).Build()
	f.r = &MiddlewareReconciler{Client: f.cli, Scheme: scheme, Recorder: f.recorder}
	// Reuse an inert watcher so unit tests never discover a kubeconfig or talk
	// to a cluster. The API-backed queue test runs a real informer separately.
	cw := watcher.NewCRWatcher(gvk, f.mid.Namespace)
	watcher.CustomResourceWatcherMap.Store(cw.GetKey(), cw)
	t.Cleanup(func() { cw.Stop(); watcher.CustomResourceWatcherMap.CompareAndDelete(cw.GetKey(), cw) })
	return f
}

func (f *rebuildFixture) reconcile(t *testing.T) (ctrl.Result, error) {
	t.Helper()
	return f.r.Reconcile(f.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.mid)})
}

func (f *rebuildFixture) current(t *testing.T) *v1.Middleware {
	t.Helper()
	got := &v1.Middleware{}
	if err := f.cli.Get(f.ctx, client.ObjectKeyFromObject(f.mid), got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMiddlewareRebuildsDeletedPrimaryWithoutReplayingConfigurations(t *testing.T) {
	f := newRebuildFixture(t)
	f.mid = f.current(t)
	f.mid.Spec.Configurations = []v1.Configuration{{Name: "must-not-be-rendered"}}
	if err := f.cli.Update(f.ctx, f.mid); err != nil {
		t.Fatal(err)
	}
	if err := f.cli.Delete(f.ctx, f.primary); err != nil {
		t.Fatal(err)
	}
	result, err := f.reconcile(t)
	if err != nil {
		t.Fatal(err)
	}
	got := f.primary.DeepCopy()
	if err := f.cli.Get(f.ctx, client.ObjectKeyFromObject(got), got); err != nil {
		t.Fatalf("primary not rebuilt: %v", err)
	}
	if got.GetUID() != "rebuilt-primary" || f.creates != 1 || f.patches != 0 {
		t.Fatalf("recovery did not use create-only: uid=%s creates=%d patches=%d", got.GetUID(), f.creates, f.patches)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("fallback interval=%s", result.RequeueAfter)
	}
	current := f.current(t)
	if current.Status.State != v1.StateAvailable || current.Status.ObservedGeneration != 7 {
		t.Fatalf("unexpected completed status: %#v", current.Status)
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || f.patches != 0 {
		t.Fatal("steady-state reconcile replayed the recovered primary")
	}
}

func TestMiddlewareMissingPrimaryFailureRetriesSameGeneration(t *testing.T) {
	f := newRebuildFixture(t)
	if err := f.cli.Delete(f.ctx, f.primary); err != nil {
		t.Fatal(err)
	}
	f.failNext = true
	if _, err := f.reconcile(t); err == nil {
		t.Fatal("expected injected failure")
	}
	current := f.current(t)
	condition := meta.FindStatusCondition(current.Status.Conditions, v1.CondTypeApplyCluster)
	if current.Status.State != v1.StateUnavailable || current.Status.ObservedGeneration != 7 || condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != middleware.PrimaryResourceRebuildFailedReason {
		t.Fatalf("failed recovery reported success: %#v", current.Status)
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatalf("same-generation retry: %v", err)
	}
	if f.current(t).Status.State != v1.StateAvailable || f.creates != 2 {
		t.Fatal("same-generation missing-resource retry did not converge")
	}
}

func TestMiddlewareMissingPrimaryReplaysPureCUEAndAdoptedOverrides(t *testing.T) {
	f := newRebuildFixture(t)
	f.baseline.Spec.PreActions = []v1.PreAction{{Name: "pure"}}
	if err := f.cli.Update(f.ctx, f.baseline); err != nil {
		t.Fatal(err)
	}
	action := &v1.MiddlewareActionBaseline{ObjectMeta: metav1.ObjectMeta{Name: "pure", Labels: f.mid.Labels}, Spec: v1.MiddlewareActionBaselineSpec{BaselineType: v1.WorkflowPreAction, Steps: []v1.Step{{Name: "patch", CUE: `
output: { spec: parameters.spec }
parameters: {
  spec: { parameters: { fromPreAction: true } }
  resource: { apiversion: "middleware.cn/v1", kind: "Middleware", name: "stable", namespace: "test" }
}`}}}}
	if err := f.cli.Create(f.ctx, action); err != nil {
		t.Fatal(err)
	}
	current := f.current(t)
	current.Spec.ReconcileOverrides = &v1.ReconcileOverrides{
		BaseSpec:  runtime.RawExtension{Raw: []byte(`{"replicas":1,"fromPreAction":true}`)},
		SpecPatch: runtime.RawExtension{Raw: []byte(`{"replicas":5}`)},
		GVK:       v1.GVK{Group: "rebuild.test.io", Version: "v1", Kind: "Database"}, Namespace: "test", Name: "stable",
	}
	if err := f.cli.Update(f.ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := f.cli.Delete(f.ctx, f.primary); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if err := f.cli.Get(f.ctx, client.ObjectKeyFromObject(f.primary), f.primary); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.primary.Object["spec"])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"fromPreAction":true,"replicas":5}` {
		t.Fatalf("recovery lost rendered/adopted values: %s", raw)
	}
	if f.creates != 1 || f.patches != 0 {
		t.Fatal("pure render recovery issued non-create writes")
	}
}

func TestMiddlewareMissingPrimaryRejectsUnsafePreAction(t *testing.T) {
	f := newRebuildFixture(t)
	f.baseline.Spec.PreActions = []v1.PreAction{{Name: "unsafe"}}
	if err := f.cli.Update(f.ctx, f.baseline); err != nil {
		t.Fatal(err)
	}
	action := &v1.MiddlewareActionBaseline{ObjectMeta: metav1.ObjectMeta{Name: "unsafe", Labels: f.mid.Labels}, Spec: v1.MiddlewareActionBaselineSpec{BaselineType: v1.WorkflowPreAction, Steps: []v1.Step{{Name: "external", CMD: v1.CMD{Command: []string{"false"}}}}}}
	if err := f.cli.Create(f.ctx, action); err != nil {
		t.Fatal(err)
	}
	// Existing resources skip rendering, including unsafe PreActions.
	if _, err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if err := f.cli.Delete(f.ctx, f.primary); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, err := f.reconcile(t)
		if !errors.Is(err, middlewareaction.ErrPreActionNotRenderSafe) {
			t.Fatalf("expected safe-render rejection, got %v", err)
		}
	}
	if f.creates != 0 || f.patches != 0 || f.current(t).Status.State != v1.StateUnavailable {
		t.Fatal("unsafe rebuild performed a write or reported success")
	}
	// Another actor can restore the owned primary after safe rendering failed.
	// We never submitted a create, so the next observation must not render again.
	restored := f.primary.DeepCopy()
	restored.SetResourceVersion("")
	if err := f.cli.Create(f.ctx, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatalf("observe concurrently restored primary: %v", err)
	}
	if f.current(t).Status.State != v1.StateAvailable || f.creates != 1 || f.patches != 0 {
		t.Fatal("concurrent restoration did not clear the failed safe-render check")
	}
}

type transientPrimaryReader struct {
	client.Reader
	failed bool
}

func (r *transientPrimaryReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*unstructured.Unstructured); ok && !r.failed {
		r.failed = true
		return errors.New("temporary primary read failure")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func TestHealthyPrimaryReadRetrySkipsUnsafePreActions(t *testing.T) {
	f := newRebuildFixture(t)
	f.baseline.Spec.PreActions = []v1.PreAction{{Name: "unsafe"}}
	if err := f.cli.Update(f.ctx, f.baseline); err != nil {
		t.Fatal(err)
	}
	action := &v1.MiddlewareActionBaseline{ObjectMeta: metav1.ObjectMeta{Name: "unsafe", Labels: f.mid.Labels}, Spec: v1.MiddlewareActionBaselineSpec{BaselineType: v1.WorkflowPreAction, Steps: []v1.Step{{Name: "external", CMD: v1.CMD{Command: []string{"false"}}}}}}
	if err := f.cli.Create(f.ctx, action); err != nil {
		t.Fatal(err)
	}
	k8s.ConfigureManagedResources(&transientPrimaryReader{Reader: f.cli})
	if _, err := f.reconcile(t); err == nil || !strings.Contains(err.Error(), "temporary primary read failure") {
		t.Fatalf("observation error = %v", err)
	}
	failed := meta.FindStatusCondition(f.current(t).Status.Conditions, v1.CondTypeApplyCluster)
	if failed == nil || failed.Reason != middleware.PrimaryResourceCheckFailedReason {
		t.Fatalf("transient observation failure recorded as %v", failed)
	}
	select {
	case event := <-f.recorder.Events:
		if !strings.Contains(event, "Warning "+middleware.PrimaryResourceCheckFailedReason+" ") {
			t.Fatalf("unexpected check failure event: %s", event)
		}
	default:
		t.Fatal("expected warning for transient observation failure")
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatalf("retry after transient GET error: %v", err)
	}
	got := f.current(t)
	cond := meta.FindStatusCondition(got.Status.Conditions, v1.CondTypeApplyCluster)
	if cond == nil || cond.Status != metav1.ConditionTrue || got.Status.State != v1.StateAvailable || got.Status.ObservedGeneration != got.Generation || f.creates != 0 || f.patches != 0 {
		t.Fatalf("healthy observation did not clear the failure: condition=%v state=%s creates=%d patches=%d", cond, got.Status.State, f.creates, f.patches)
	}
}

func TestMiddlewareMissingPrimaryDoesNotBypassPause(t *testing.T) {
	f := newRebuildFixture(t)
	current := f.current(t)
	current.Annotations = map[string]string{v1.AnnotationSuspendReconcile: "true"}
	if err := f.cli.Update(f.ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := f.cli.Delete(f.ctx, f.primary); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.creates != 0 || f.patches != 0 || !hasReconcilePaused(f.current(t).Status.Conditions) {
		t.Fatal("missing-resource recovery bypassed pause")
	}
}

func TestMiddlewareSteadyStateRejectsForeignPrimary(t *testing.T) {
	f := newRebuildFixture(t)
	refs := f.primary.GetOwnerReferences()
	refs[0].UID = types.UID("other-mid")
	f.primary.SetOwnerReferences(refs)
	if err := f.cli.Update(f.ctx, f.primary); err != nil {
		t.Fatal(err)
	}
	_, err := f.reconcile(t)
	if err == nil || !strings.Contains(err.Error(), "different controller owner") {
		t.Fatalf("foreign primary not rejected: %v", err)
	}
	if f.creates != 0 || f.patches != 0 || f.current(t).Status.State != v1.StateUnavailable {
		t.Fatal("foreign primary was changed or reported available")
	}
}

func TestMiddlewareFailedApplyDoesNotAdvanceGenerationAndRetries(t *testing.T) {
	f := newRebuildFixture(t)
	current := f.current(t)
	current.Generation = 8
	current.Spec.Parameters.Raw = []byte(`{"replicas":2}`)
	if err := f.cli.Update(f.ctx, current); err != nil {
		t.Fatal(err)
	}
	f.failNext = true
	if _, err := f.reconcile(t); err == nil {
		t.Fatal("expected injected failure")
	}
	current = f.current(t)
	condition := meta.FindStatusCondition(current.Status.Conditions, v1.CondTypeApplyCluster)
	if current.Status.State != v1.StateUnavailable || current.Status.ObservedGeneration != 7 || condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("failed apply advanced generation: %#v", current.Status)
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatalf("same-generation apply retry: %v", err)
	}
	current = f.current(t)
	if current.Status.State != v1.StateAvailable || current.Status.ObservedGeneration != 8 || f.patches != 2 {
		t.Fatalf("retry did not converge: %#v; patches=%d", current.Status, f.patches)
	}
	if err := f.cli.Get(f.ctx, client.ObjectKeyFromObject(f.primary), f.primary); apierrors.IsNotFound(err) {
		t.Fatal("retry lost primary")
	}
}

func TestMiddlewareFailedResumeRetainsApprovalAndRetriesSameGeneration(t *testing.T) {
	f := newRebuildFixture(t)
	current := f.current(t)
	current.Annotations = map[string]string{v1.AnnotationResumePolicy: string(v1.ReconcileResumePolicyApply)}
	if err := f.cli.Update(f.ctx, current); err != nil {
		t.Fatal(err)
	}
	meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: v1.CondTypeReconcilePaused, Status: metav1.ConditionTrue, ObservedGeneration: 7, Reason: "Paused", LastTransitionTime: metav1.Now()})
	if err := f.cli.Status().Update(f.ctx, current); err != nil {
		t.Fatal(err)
	}
	f.failNext = true
	if _, err := f.reconcile(t); err == nil {
		t.Fatal("expected injected resume failure")
	}
	current = f.current(t)
	condition := meta.FindStatusCondition(current.Status.Conditions, v1.CondTypeApplyCluster)
	if current.Status.State != v1.StateUnavailable || !hasReconcilePaused(current.Status.Conditions) || current.Annotations[v1.AnnotationResumePolicy] != "apply" || condition == nil || condition.Status != metav1.ConditionFalse {
		t.Fatalf("failed resume lost approval or reported success: %#v", current)
	}
	if _, err := f.reconcile(t); err != nil {
		t.Fatalf("same-generation resume retry: %v", err)
	}
	current = f.current(t)
	if current.Status.State != v1.StateAvailable || hasReconcilePaused(current.Status.Conditions) || current.Annotations[v1.AnnotationResumePolicy] != "" || f.patches != 2 {
		t.Fatalf("resume did not finalize after successful retry: %#v", current)
	}
}
