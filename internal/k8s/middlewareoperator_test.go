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
)

func newMiddlewareOperatorTestClient(t *testing.T, objects ...client.Object) client.Client {
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
}
