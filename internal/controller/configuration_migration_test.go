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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPausedOperatorMigratesGCReferenceWithoutApplyingConfiguration(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{
		Name: "demo", Namespace: "tenant", UID: "owner", Generation: 2, Finalizers: []string{v1.FinalizerMiddlewareOperator},
		Annotations: map[string]string{v1.AnnotationSuspendReconcile: "true"},
	}}
	// A pending spec change on a paused instance must not postpone GC repair.
	mo.Status.ObservedGeneration = 1
	mo.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{{
		Version: "v1", Kind: "ConfigMap", Name: "settings", Namespace: mo.Namespace, Namespaced: true,
		ConfigurationName: "settings-template", ConfigurationUID: "source", OwnerUID: mo.UID, ResourceUID: "config",
		DisablePolicy: v1.ConfigurationDisablePolicyDelete,
	}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "settings", Namespace: mo.Namespace, UID: "config", Labels: map[string]string{v1.LabelApp: mo.Name},
		Annotations: map[string]string{
			v1.AnnotationConfigurationDeletePolicy: "orphan", v1.LabelConfigurations: "settings-template",
			v1.AnnotationConfigurationOwnerUID: string(mo.UID), v1.AnnotationConfigurationUID: "source",
		},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(mo, v1.GroupVersion.WithKind("MiddlewareOperator"))},
	}, Data: map[string]string{"mode": "paused-user-value"}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(mo).WithObjects(mo, cm).Build()
	reconciler := &MiddlewareOperatorReconciler{Client: cli, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}
	if _, err := reconciler.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mo)}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(cm), cm); err != nil {
		t.Fatal(err)
	}
	if len(cm.OwnerReferences) != 0 || cm.Data["mode"] != "paused-user-value" || cm.UID != "config" {
		t.Fatalf("expected only the owner reference to be removed: %#v", cm)
	}
	if err := cli.Get(t.Context(), client.ObjectKeyFromObject(mo), mo); err != nil {
		t.Fatal(err)
	}
	flag := mo.Status.RenderedConfigurationResources[0].ControllerReferenceRequired
	if flag == nil || *flag || mo.Status.ObservedGeneration != 1 || !v1.IsReconcileSuspended(mo.Annotations) {
		t.Fatalf("migration disturbed the pause or failed to update the inventory: %#v", mo)
	}
}
