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

package middlewareoperator

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestHandleResourceDeleteNoOperatorReturnsNil(t *testing.T) {
	mo := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-mo",
			Namespace:   "default",
			Annotations: map[string]string{v1.LabelNoOperator: "true"},
		},
	}

	if err := HandleResource(nil, nil, consts.HandleActionDelete, mo); err != nil {
		t.Fatalf("expected nil error for nooperator delete, got %v", err)
	}
}

func TestHandleResourceSuspendedSkipsChildWrites(t *testing.T) {
	mo := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suspended-mo",
			Namespace: "default",
			Annotations: map[string]string{
				v1.AnnotationSuspendReconcile: "true",
			},
		},
	}

	for _, action := range []consts.HandleAction{consts.HandleActionPublish, consts.HandleActionUpdate} {
		if err := HandleResource(context.Background(), nil, action, mo); err != nil {
			t.Fatalf("HandleResource(%s) error = %v, want suspended no-op", action, err)
		}
	}

	if err := CompareDeployment(context.Background(), nil, &appsv1.Deployment{}, mo); err != nil {
		t.Fatalf("CompareDeployment() error = %v, want suspended no-op", err)
	}
}

func TestPublishRetriesFailureToSaveConfigurationCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "ns", UID: "owner", Generation: 2}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(mo).WithObjects(mo).Build()
	statusErr := errors.New("status write interrupted")
	cli := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
		return statusErr
	}})
	if err := handleExtraResource(t.Context(), cli, consts.HandleActionPublish, mo); !errors.Is(err, statusErr) {
		t.Fatalf("expected status error for retry, got %v", err)
	}
	if err := base.Get(t.Context(), client.ObjectKeyFromObject(mo), mo); err != nil {
		t.Fatal(err)
	}
	if mo.Status.RenderedConfigurationResourcesGeneration != 2 || mo.Status.ObservedGeneration != 0 {
		t.Fatalf("unexpected status after interrupted publication: %#v", mo.Status)
	}
	if err := handleExtraResource(t.Context(), base, consts.HandleActionPublish, mo); err != nil {
		t.Fatal(err)
	}
}
