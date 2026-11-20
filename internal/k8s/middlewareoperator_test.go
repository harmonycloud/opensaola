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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func newMiddlewareOperatorTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add middleware scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1.MiddlewareOperator{}).WithObjects(objects...).Build()
}

func TestCreateMiddlewareOperatorCreatesWhenAbsent(t *testing.T) {
	ctx := context.Background()
	cli := newMiddlewareOperatorTestClient(t)

	mo := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-operator", Namespace: "default"},
	}

	if err := CreateMiddlewareOperator(ctx, cli, mo); err != nil {
		t.Fatalf("CreateMiddlewareOperator returned error: %v", err)
	}

	got, err := GetMiddlewareOperator(ctx, cli, mo.Name, mo.Namespace)
	if err != nil {
		t.Fatalf("get created MiddlewareOperator: %v", err)
	}
	if got.Name != mo.Name || got.Namespace != mo.Namespace {
		t.Fatalf("created MiddlewareOperator mismatch: got %s/%s", got.Namespace, got.Name)
	}
}

func TestCreateMiddlewareOperatorAlreadyExistsUsesMiddlewareOperatorResource(t *testing.T) {
	ctx := context.Background()
	existing := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "redis-operator", Namespace: "default"},
	}
	cli := newMiddlewareOperatorTestClient(t, existing)

	err := CreateMiddlewareOperator(ctx, cli, existing.DeepCopy())
	if !apiErrors.IsAlreadyExists(err) {
		t.Fatalf("expected AlreadyExists error, got %v", err)
	}

	statusErr, ok := err.(*apiErrors.StatusError)
	if !ok {
		t.Fatalf("expected StatusError, got %T", err)
	}
	details := statusErr.ErrStatus.Details
	if details == nil {
		t.Fatal("expected status details")
	}
	gr := MiddlewareOperatorGroupResource()
	if details.Group != gr.Group || details.Kind != gr.Resource {
		t.Fatalf("expected group/resource %s/%s, got %s/%s", gr.Group, gr.Resource, details.Group, details.Kind)
	}
}

func TestCreateMiddlewareOperatorDoesNotTreatSameNamedMiddlewareAsExisting(t *testing.T) {
	ctx := context.Background()
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "default"},
	}
	cli := newMiddlewareOperatorTestClient(t, mid)

	mo := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "default"},
	}
	if err := CreateMiddlewareOperator(ctx, cli, mo); err != nil {
		t.Fatalf("CreateMiddlewareOperator returned error: %v", err)
	}

	if _, err := GetMiddlewareOperator(ctx, cli, mo.Name, mo.Namespace); err != nil {
		t.Fatalf("expected MiddlewareOperator to be created despite same-named Middleware: %v", err)
	}
}

func TestUpdateMiddlewareOperatorStatus_PreservesRuntimeOwnedFields(t *testing.T) {
	ctx := context.Background()
	diagnostic := "phase=workload-readiness; failedObject=v1/Pod middleware-operator/opensaola-install-crds; causeCategory=RegistryTLS; cause=x509: certificate signed by unknown authority"
	existing := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "opensaola", Namespace: "middleware-operator"},
		Status: v1.MiddlewareOperatorStatus{
			ObservedGeneration: 11,
			State:              v1.StateUnavailable,
			Reason:             diagnostic,
			Conditions: []metav1.Condition{{
				Type:               v1.CondTypeApplyOperator,
				Status:             metav1.ConditionFalse,
				Reason:             "ApplyOperatorFailed",
				Message:            diagnostic,
				ObservedGeneration: 11,
			}},
			OperatorStatus: map[string]appsv1.DeploymentStatus{
				"opensaola-controller-manager": {Replicas: 1, ReadyReplicas: 0},
			},
			OperatorAvailable: "0/1",
			Ready:             false,
			Runtime:           "phase=workload-readiness; failedObject=v1/Pod middleware-operator/opensaola-install-crds",
			RenderedConfigurationResources: []v1.RenderedConfigurationResource{{
				ConfigurationName: "old-config",
				Version:           "v1",
				Kind:              "ConfigMap",
				Name:              "old-config",
			}},
			RenderedConfigurationResourcesGeneration: 11,
		},
	}
	cli := newMiddlewareOperatorTestClient(t, existing)

	update := existing.DeepCopy()
	update.Status.State = v1.StateAvailable
	update.Status.Reason = "main controller converged"
	update.Status.Conditions = []metav1.Condition{{
		Type:               v1.CondTypeApplyOperator,
		Status:             metav1.ConditionTrue,
		Reason:             "ApplyOperatorSucceed",
		Message:            "operator applied",
		ObservedGeneration: 12,
	}}
	update.Status.ObservedGeneration = 12
	update.Status.OperatorStatus = nil
	update.Status.OperatorAvailable = ""
	update.Status.Ready = true
	update.Status.Runtime = ""
	update.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{{
		ConfigurationName: "new-config",
		Version:           "v1",
		Kind:              "ConfigMap",
		Name:              "new-config",
	}}
	update.Status.PendingConfigurationWrite = &v1.PendingConfigurationWrite{ID: "already-confirmed"}
	update.Status.RenderedConfigurationResourcesGeneration = 12

	if err := UpdateMiddlewareOperatorStatus(ctx, cli, update); err != nil {
		t.Fatalf("UpdateMiddlewareOperatorStatus returned error: %v", err)
	}

	got, err := GetMiddlewareOperator(ctx, cli, existing.Name, existing.Namespace)
	if err != nil {
		t.Fatalf("get updated MiddlewareOperator: %v", err)
	}
	if got.Status.ObservedGeneration != 12 {
		t.Fatalf("expected main observedGeneration to update, got %d", got.Status.ObservedGeneration)
	}
	if got.Status.State != v1.StateAvailable || got.Status.Reason != "main controller converged" {
		t.Fatalf("expected main-owned fields to update, got state=%q reason=%q", got.Status.State, got.Status.Reason)
	}
	if got.Status.OperatorAvailable != "0/1" || got.Status.Ready {
		t.Fatalf("expected runtime-owned availability fields to be preserved, got available=%q ready=%v", got.Status.OperatorAvailable, got.Status.Ready)
	}
	if _, ok := got.Status.OperatorStatus["opensaola-controller-manager"]; !ok {
		t.Fatalf("expected runtime-owned OperatorStatus to be preserved, got %#v", got.Status.OperatorStatus)
	}
	if got.Status.Runtime == "" || got.Status.Runtime != existing.Status.Runtime {
		t.Fatalf("expected runtime diagnostic to be preserved, got %q", got.Status.Runtime)
	}
	if got.Status.RenderedConfigurationResourcesGeneration != 11 {
		t.Fatalf("expected persisted configuration inventory generation 11, got %d", got.Status.RenderedConfigurationResourcesGeneration)
	}
	if inventory := got.Status.RenderedConfigurationResources; len(inventory) != 1 || inventory[0].ConfigurationName != "old-config" {
		t.Fatalf("expected persisted rendered configuration inventory, got %#v", inventory)
	}
	if got.Status.PendingConfigurationWrite != nil {
		t.Fatal("stale status restored an already confirmed Configuration write")
	}
}

func TestOperatorRuntimeStatusSingleDeploymentAndNoop(t *testing.T) {
	ctx := t.Context()
	mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "main", Namespace: "default", UID: "owner-1"}}
	key := client.ObjectKeyFromObject(mo)
	cli := newMiddlewareOperatorTestClient(t, mo)
	get := func() *v1.MiddlewareOperator {
		t.Helper()
		var now v1.MiddlewareOperator
		if err := cli.Get(ctx, key, &now); err != nil {
			t.Fatal(err)
		}
		return &now
	}
	write := func(name string, detail *appsv1.DeploymentStatus, summary *OperatorRuntimeSummary) {
		t.Helper()
		if err := UpdateMiddlewareOperatorRuntimeStatus(ctx, cli, key, mo.UID, name, detail, summary); err != nil {
			t.Fatal(err)
		}
	}
	detail := &appsv1.DeploymentStatus{Replicas: 3, ReadyReplicas: 3}
	healthy := &OperatorRuntimeSummary{Available: "3/3", Ready: true, Runtime: "Ready"}
	write("main", detail, healthy)
	rv := get().ResourceVersion
	write("main", detail, healthy)
	write("absent", nil, nil)
	if got := get(); got.ResourceVersion != rv {
		t.Fatalf("unchanged observations wrote status: rv %s -> %s", rv, got.ResourceVersion)
	}
	if err := UpdateMiddlewareOperatorRuntimeStatus(ctx, cli, key, mo.UID, "helper", detail, healthy); err == nil {
		t.Fatal("auxiliary Deployment was allowed to set the primary summary")
	}
	write("helper", &appsv1.DeploymentStatus{Replicas: 1}, nil)
	if got := get().Status; !got.Ready || got.OperatorAvailable != "3/3" || got.Runtime != "Ready" || got.OperatorStatus["main"].ReadyReplicas != 3 || got.OperatorStatus["helper"].Replicas != 1 {
		t.Fatalf("unexpected status after recording helper: %+v", got)
	}
	write("main", nil, &OperatorRuntimeSummary{Runtime: "Missing"})
	if got := get().Status; got.Ready || got.OperatorAvailable != "" || got.Runtime != "Missing" || len(got.OperatorStatus) != 1 || got.OperatorStatus["helper"].Replicas != 1 {
		t.Fatalf("unexpected status after removing primary: %+v", got)
	}
	write("helper", nil, nil)
	if got := get().Status; len(got.OperatorStatus) != 0 || got.Runtime != "Missing" {
		t.Fatalf("unexpected status after removing helper: %+v", got)
	}
}

// Interleave writes at the exact point where an auxiliary Deployment's status came
// from a stale read. The API must reject its first patch and retain both other writes.
func TestOperatorRuntimeStatusRetriesAgainstLatest(t *testing.T) {
	ctx := t.Context()
	mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "main", Namespace: "default", UID: "owner-1"}}
	key := client.ObjectKeyFromObject(mo)
	base := newMiddlewareOperatorTestClient(t, mo)
	attempts := 0
	cli := interceptor.NewClient(base, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			attempts++
			if attempts == 1 {
				detail := &appsv1.DeploymentStatus{Replicas: 2}
				summary := &OperatorRuntimeSummary{Available: "2/2", Ready: true, Runtime: "Ready"}
				if err := UpdateMiddlewareOperatorRuntimeStatus(ctx, c, key, mo.UID, "main", detail, summary); err != nil {
					return err
				}
				stale := mo.DeepCopy()
				stale.Status.Reason = "converged"
				if err := UpdateMiddlewareOperatorStatus(ctx, c, stale); err != nil {
					return err
				}
			}
			return c.SubResource(sub).Patch(ctx, obj, p, opts...)
		},
	})
	err := UpdateMiddlewareOperatorRuntimeStatus(ctx, cli, key, mo.UID, "helper", &appsv1.DeploymentStatus{Replicas: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var now v1.MiddlewareOperator
	if err := base.Get(ctx, key, &now); err != nil {
		t.Fatal(err)
	}
	s := now.Status
	if attempts != 2 || s.OperatorStatus["helper"].Replicas != 1 || s.OperatorStatus["main"].Replicas != 2 ||
		!s.Ready || s.OperatorAvailable != "2/2" || s.Runtime != "Ready" || s.Reason != "converged" {
		t.Fatalf("attempts = %d, final status = %+v", attempts, s)
	}
}

func TestOperatorRuntimeStatusIgnoresRetiredOwnerDuringWrite(t *testing.T) {
	for _, action := range []string{"delete", "replace", "terminate"} {
		t.Run(action, func(t *testing.T) {
			ctx := t.Context()
			mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "main", Namespace: "default", UID: "old"}}
			if action == "terminate" {
				mo.Finalizers = []string{"middleware.cn/finish"}
			}
			key := client.ObjectKeyFromObject(mo)
			base := newMiddlewareOperatorTestClient(t, mo)
			attempts := 0
			cli := interceptor.NewClient(base, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					attempts++
					if attempts == 1 {
						if err := c.Delete(ctx, mo); err != nil {
							return err
						}
						if action == "replace" {
							replacement := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, UID: "new"}}
							if err := c.Create(ctx, replacement); err != nil {
								return err
							}
						}
					}
					return c.SubResource(sub).Patch(ctx, obj, p, opts...)
				},
			})
			if err := UpdateMiddlewareOperatorRuntimeStatus(ctx, cli, key, mo.UID, "main", &appsv1.DeploymentStatus{Replicas: 1}, nil); err != nil {
				t.Fatal(err)
			}
			// A redelivered event for the same old owner is equally harmless.
			if err := UpdateMiddlewareOperatorRuntimeStatus(ctx, cli, key, mo.UID, "main", &appsv1.DeploymentStatus{Replicas: 1}, nil); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 {
				t.Fatalf("retired owner's status patch attempted %d times", attempts)
			}
			var now v1.MiddlewareOperator
			err := base.Get(ctx, key, &now)
			if action == "delete" {
				if !apiErrors.IsNotFound(err) {
					t.Fatalf("expected missing owner, got %v", err)
				}
			} else if err != nil || len(now.Status.OperatorStatus) != 0 {
				t.Fatalf("retired/replaced owner received status: %+v, error %v", now.Status, err)
			}
		})
	}
}
