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

package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestApplyReconcileOverridesPreservesDeletionAcrossFutureRenders(t *testing.T) {
	t.Parallel()

	gvk := schema.GroupVersionKind{Group: "dr.test.io", Version: "v1", Kind: "Database"}
	baseSpec := []byte(`{"baselineOnly":true,"replicas":3,"storage":"10Gi"}`)
	mid := &v1.Middleware{
		Spec: v1.MiddlewareSpec{
			ReconcileOverrides: &v1.ReconcileOverrides{
				SpecPatch: runtime.RawExtension{Raw: []byte(`{"baselineOnly":null,"replicas":5}`)},
				BaseSpec:  runtime.RawExtension{Raw: baseSpec},
				GVK:       v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind},
				Namespace: "middleware",
				Name:      "demo",
			},
		},
	}
	cr := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"baselineOnly": true, "replicas": float64(3), "storage": "10Gi"},
	}}
	cr.SetGroupVersionKind(gvk)
	cr.SetNamespace("middleware")
	cr.SetName("demo")
	if err := ApplyReconcileOverrides(mid, cr); err != nil {
		t.Fatalf("ApplyReconcileOverrides() error = %v", err)
	}
	spec, found, err := unstructured.NestedMap(cr.Object, "spec")
	if err != nil || !found {
		t.Fatalf("get rendered spec: found=%t err=%v", found, err)
	}
	if _, exists := spec["baselineOnly"]; exists {
		t.Fatalf("baselineOnly was not removed: %#v", spec)
	}
	if got := spec["replicas"]; got != float64(5) {
		t.Fatalf("replicas = %#v, want 5", got)
	}
	if got := spec["storage"]; got != "10Gi" {
		t.Fatalf("unrelated field changed: storage = %#v", got)
	}
}

func TestApplyReconcileOverridesLetsLaterDesiredPathWin(t *testing.T) {
	t.Parallel()

	gvk := schema.GroupVersionKind{Group: "dr.test.io", Version: "v1", Kind: "Database"}
	mid := &v1.Middleware{Spec: v1.MiddlewareSpec{ReconcileOverrides: &v1.ReconcileOverrides{
		SpecPatch: runtime.RawExtension{Raw: []byte(`{"legacy":null,"replicas":5}`)},
		BaseSpec:  runtime.RawExtension{Raw: []byte(`{"legacy":true,"replicas":3,"storage":"10Gi"}`)},
		GVK:       v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind},
		Namespace: "middleware",
		Name:      "demo",
	}}}
	// The user later changed replicas in MID/Baseline from 3 to 6. That
	// explicit desired change wins, while the unrelated DR deletion remains.
	cr := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"legacy": true, "replicas": float64(6), "storage": "20Gi"},
	}}
	cr.SetGroupVersionKind(gvk)
	cr.SetNamespace("middleware")
	cr.SetName("demo")
	if err := ApplyReconcileOverrides(mid, cr); err != nil {
		t.Fatalf("ApplyReconcileOverrides() error = %v", err)
	}
	spec, _, err := primarySpec(cr)
	if err != nil {
		t.Fatalf("primarySpec() error = %v", err)
	}
	if _, exists := spec["legacy"]; exists {
		t.Fatalf("unrelated DR deletion was lost: %#v", spec)
	}
	if got := spec["replicas"]; got != float64(6) {
		t.Fatalf("later desired replicas = %#v, want 6", got)
	}
	if got := spec["storage"]; got != "20Gi" {
		t.Fatalf("unrelated desired storage = %#v, want 20Gi", got)
	}
}

func TestApplyReconcileOverridesRejectsDifferentTarget(t *testing.T) {
	t.Parallel()

	mid := &v1.Middleware{Spec: v1.MiddlewareSpec{ReconcileOverrides: &v1.ReconcileOverrides{
		SpecPatch: runtime.RawExtension{Raw: []byte(`{"replicas":5}`)},
		BaseSpec:  runtime.RawExtension{Raw: []byte(`{"replicas":3}`)},
		GVK:       v1.GVK{Group: "dr.test.io", Version: "v1", Kind: "Database"},
		Namespace: "middleware",
		Name:      "demo",
	}}}
	cr := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"replicas": float64(3)}}}
	cr.SetGroupVersionKind(schema.GroupVersionKind{Group: "dr.test.io", Version: "v1", Kind: "OtherDatabase"})
	cr.SetNamespace("middleware")
	cr.SetName("demo")
	if err := ApplyReconcileOverrides(mid, cr); !errors.Is(err, ErrReconcileOverrideTargetChanged) {
		t.Fatalf("ApplyReconcileOverrides() error = %v, want ErrReconcileOverrideTargetChanged", err)
	}
}

func TestVerifyPausedPrimaryCustomResourceForApply(t *testing.T) {
	t.Parallel()

	gvk := schema.GroupVersionKind{Group: "dr.test.io", Version: "v1", Kind: "Database"}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("DatabaseList"), &unstructured.UnstructuredList{})
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata":   map[string]any{"name": "demo", "namespace": "middleware", "uid": "dr-resource-uid"},
		"spec":       map[string]any{"replicas": int64(5)},
	}}
	live.SetGroupVersionKind(gvk)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(live).Build()
	_, raw, err := primarySpec(live)
	if err != nil {
		t.Fatalf("primarySpec(live) error = %v", err)
	}
	snapshot := &v1.ReconcilePauseStatus{
		GVK:                 v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind},
		Namespace:           "middleware",
		Name:                "demo",
		ResourceUID:         "dr-resource-uid",
		AdoptedLiveSpecHash: specHash(raw),
	}
	rendered := live.DeepCopy()
	if err := VerifyPausedPrimaryCustomResourceForApply(context.Background(), cli, snapshot, rendered); err != nil {
		t.Fatalf("VerifyPausedPrimaryCustomResourceForApply() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(gvk)
	if err := cli.Get(context.Background(), types.NamespacedName{Name: "demo", Namespace: "middleware"}, updated); err != nil {
		t.Fatalf("get live CR: %v", err)
	}
	updated.Object["spec"] = map[string]any{"replicas": int64(6)}
	if err := cli.Update(context.Background(), updated); err != nil {
		t.Fatalf("update live CR: %v", err)
	}
	if err := VerifyPausedPrimaryCustomResourceForApply(context.Background(), cli, snapshot, rendered); !errors.Is(err, ErrReconcilePauseLiveSpecChanged) {
		t.Fatalf("VerifyPausedPrimaryCustomResourceForApply() error = %v, want ErrReconcilePauseLiveSpecChanged", err)
	}
}

func TestPrimarySpecNormalizesRawExtension(t *testing.T) {
	t.Parallel()

	cr := &unstructured.Unstructured{Object: map[string]any{
		"spec": runtime.RawExtension{Raw: []byte(`{"replicas":3,"nested":{"enabled":true}}`)},
	}}
	spec, raw, err := primarySpec(cr)
	if err != nil {
		t.Fatalf("primarySpec() error = %v", err)
	}
	if got := spec["replicas"]; got != float64(3) {
		t.Fatalf("replicas = %#v, want 3", got)
	}
	if got, want := string(raw), `{"nested":{"enabled":true},"replicas":3}`; got != want {
		t.Fatalf("normalized raw spec = %s, want %s", got, want)
	}
}

func TestPauseSnapshotAndAdoptionBuildsPersistentOverride(t *testing.T) {
	configurePauseTestSchemas(t)
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	gvk := schema.GroupVersionKind{Group: "dr.test.io", Version: "v1", Kind: "Database"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("DatabaseList"), &unstructured.UnstructuredList{})

	const (
		name         = "demo"
		namespace    = "middleware"
		baselineName = "baseline-dr-adoption"
		packageName  = "package-dr-adoption"
	)
	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: baselineName, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec:       v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}},
	}
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec: v1.MiddlewareSpec{
			Baseline:   baselineName,
			Parameters: runtime.RawExtension{Raw: []byte(`{"legacy":true,"replicas":3,"storage":"10Gi"}`)},
		},
	}
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"uid":       "dr-resource-uid",
		},
		"spec": map[string]any{"legacy": true, "replicas": int64(3), "storage": "10Gi"},
	}}
	live.SetGroupVersionKind(gvk)
	mid.UID = "mid-uid"
	mid.ResourceVersion = "999"
	controller := true
	live.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Middleware", Name: mid.Name, UID: mid.UID, Controller: &controller}})
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline, mid, live).Build()

	snapshot, err := BuildReconcilePauseSnapshot(ctx, cli, mid)
	if err != nil {
		t.Fatalf("BuildReconcilePauseSnapshot() error = %v", err)
	}
	if snapshot.ResourceUID != "dr-resource-uid" || snapshot.Hash == "" {
		t.Fatalf("unexpected pause snapshot: %#v", snapshot)
	}

	// Simulate a DR writer changing replicas and removing a Baseline-derived
	// field, while a normal desired change independently changes storage.
	actual := &unstructured.Unstructured{}
	actual.SetGroupVersionKind(gvk)
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, actual); err != nil {
		t.Fatalf("get live primary CR: %v", err)
	}
	actual.Object["spec"] = map[string]any{"replicas": int64(5), "storage": "10Gi"}
	if err := cli.Update(ctx, actual); err != nil {
		t.Fatalf("update live primary CR: %v", err)
	}
	mid.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"legacy":true,"replicas":3,"storage":"20Gi"}`)}

	adoption, err := AdoptPausedPrimaryCustomResource(ctx, cli, mid, snapshot)
	if err != nil {
		t.Fatalf("AdoptPausedPrimaryCustomResource() error = %v", err)
	}
	if adoption == nil || adoption.Overrides == nil {
		t.Fatal("expected an override patch")
	}
	if got, want := string(adoption.Overrides.SpecPatch.Raw), `{"legacy":null,"replicas":5}`; got != want {
		t.Fatalf("override patch = %s, want %s", got, want)
	}

	mid.Spec.ReconcileOverrides = adoption.Overrides
	rendered, err := RenderPrimaryCustomResource(ctx, cli, mid)
	if err != nil {
		t.Fatalf("RenderPrimaryCustomResource() error = %v", err)
	}
	spec, _, err := primarySpec(rendered)
	if err != nil {
		t.Fatalf("primarySpec(rendered) error = %v", err)
	}
	want := map[string]any{"replicas": float64(5), "storage": "20Gi"}
	if !reflect.DeepEqual(spec, want) {
		t.Fatalf("rendered adopted spec = %#v, want %#v", spec, want)
	}
}

func TestPauseSnapshotAndAdoptionIgnoresObservedNull(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	gvk := schema.GroupVersionKind{Group: "mysql.test.io", Version: "v1", Kind: "MysqlCluster"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("MysqlClusterList"), &unstructured.UnstructuredList{})

	const (
		name         = "mysql"
		namespace    = "middleware"
		baselineName = "mysql-baseline"
		packageName  = "mysql-package"
	)
	parameters := []byte(`{"_statefulset":{"spec":{"template":{"metadata":{"labels":{"app":"mysql"}}}}}}`)
	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: baselineName, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec:       v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}},
	}
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec: v1.MiddlewareSpec{
			Baseline:   baselineName,
			Parameters: runtime.RawExtension{Raw: parameters},
		},
	}
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"uid":       "mysql-resource-uid",
		},
		"spec": map[string]any{
			"_statefulset": map[string]any{
				"spec": map[string]any{
					"selector": nil,
					"template": map[string]any{
						"metadata": map[string]any{"labels": map[string]any{"app": "mysql"}},
					},
				},
			},
		},
	}}
	live.SetGroupVersionKind(gvk)
	mid.UID = "mid-uid"
	mid.ResourceVersion = "999"
	controller := true
	live.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Middleware", Name: mid.Name, UID: mid.UID, Controller: &controller}})
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline, mid, live).Build()

	snapshot, err := BuildReconcilePauseSnapshot(ctx, cli, mid)
	if err != nil {
		t.Fatalf("BuildReconcilePauseSnapshot() error = %v", err)
	}
	adoption, err := AdoptPausedPrimaryCustomResource(ctx, cli, mid, snapshot)
	if err != nil {
		t.Fatalf("AdoptPausedPrimaryCustomResource() error = %v", err)
	}
	if adoption == nil || adoption.LiveSpecHash == "" {
		t.Fatalf("unexpected adoption result: %#v", adoption)
	}
	if adoption.Overrides != nil {
		t.Fatalf("unexpected override for observed selector null: %#v", adoption.Overrides)
	}
}

func TestPauseSnapshotAndAdoptionIncludesPureCUEPreAction(t *testing.T) {
	configurePauseTestSchemas(t)
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add OpenSaola scheme: %v", err)
	}
	gvk := schema.GroupVersionKind{Group: "preaction.test.io", Version: "v1", Kind: "Database"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind("DatabaseList"), &unstructured.UnstructuredList{})

	const (
		name         = "demo-preaction"
		namespace    = "middleware"
		baselineName = "baseline-preaction-adoption"
		actionName   = "preaction-adoption-patch"
		packageName  = "package-preaction-adoption"
	)
	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: baselineName, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec: v1.MiddlewareBaselineSpec{
			GVK:        v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind},
			Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":1}`)},
			PreActions: []v1.PreAction{{Name: actionName}},
		},
	}
	action := &v1.MiddlewareActionBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: actionName, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec: v1.MiddlewareActionBaselineSpec{
			BaselineType: v1.WorkflowPreAction,
			Steps: []v1.Step{{
				Name: "add-preaction-field",
				CUE: `
output: { spec: parameters.spec }
parameters: {
	spec: {
		parameters: {
			replicas: 1
			fromPreAction: true
		}
	}
	resource: {
		apiversion: "middleware.cn/v1"
		kind: "Middleware"
		name: "demo-preaction"
		namespace: "middleware"
	}
}`,
			}},
		},
	}
	mid := &v1.Middleware{
		TypeMeta:   metav1.TypeMeta{APIVersion: "middleware.cn/v1", Kind: "Middleware"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{v1.LabelPackageName: packageName}},
		Spec: v1.MiddlewareSpec{
			Baseline:   baselineName,
			Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":1}`)},
		},
	}
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"uid":       "preaction-resource-uid",
		},
		"spec": map[string]any{"replicas": int64(1), "fromPreAction": true},
	}}
	live.SetGroupVersionKind(gvk)
	mid.UID = "mid-uid"
	mid.ResourceVersion = "999"
	controller := true
	live.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Middleware", Name: mid.Name, UID: mid.UID, Controller: &controller}})
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline, action, mid, live).Build()

	snapshot, err := BuildReconcilePauseSnapshot(ctx, cli, mid)
	if err != nil {
		t.Fatalf("BuildReconcilePauseSnapshot() error = %v", err)
	}
	base := loadPauseActualSpec(t, cli, mid, snapshot)
	if want := map[string]any{"replicas": float64(1), "fromPreAction": true}; !reflect.DeepEqual(base, want) {
		t.Fatalf("snapshot actual spec = %#v, want %#v", base, want)
	}

	actual := &unstructured.Unstructured{}
	actual.SetGroupVersionKind(gvk)
	if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, actual); err != nil {
		t.Fatalf("get live primary CR: %v", err)
	}
	actual.Object["spec"] = map[string]any{"replicas": int64(5), "fromPreAction": true}
	if err := cli.Update(ctx, actual); err != nil {
		t.Fatalf("update live primary CR: %v", err)
	}

	adoption, err := AdoptPausedPrimaryCustomResource(ctx, cli, mid, snapshot)
	if err != nil {
		t.Fatalf("AdoptPausedPrimaryCustomResource() error = %v", err)
	}
	if adoption == nil || adoption.Overrides == nil {
		t.Fatal("expected an override patch")
	}
	if got, want := string(adoption.Overrides.SpecPatch.Raw), `{"replicas":5}`; got != want {
		t.Fatalf("override patch = %s, want %s", got, want)
	}
	base, err = decodeSpec(adoption.Overrides.BaseSpec.Raw)
	if err != nil {
		t.Fatalf("decode override base spec: %v", err)
	}
	if want := map[string]any{"replicas": float64(1), "fromPreAction": true}; !reflect.DeepEqual(base, want) {
		t.Fatalf("override base spec = %#v, want %#v", base, want)
	}
}

// The fake custom-resource API has granular maps and atomic lists. Real native
// list topology is tested separately against the API server.
type pauseTestSchemas struct{}

func (pauseTestSchemas) Resolve(context.Context, schema.GroupVersionKind) (managedfields.TypeConverter, string, error) {
	return managedfields.NewDeducedTypeConverter(), "test-schema", nil
}
func configurePauseTestSchemas(t *testing.T) {
	t.Helper()
	k8s.ConfigureResourceIntents(pauseTestSchemas{})
	t.Cleanup(func() { k8s.ConfigureResourceIntents(nil) })
}

func pauseAdoptionFixture(t *testing.T, desired, actual string) (client.Client, *v1.Middleware, *unstructured.Unstructured) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	gvk := schema.GroupVersionKind{Group: "pause.test.io", Version: "v1", Kind: "Database"}
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	baseline := &v1.MiddlewareBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: t.Name(), Labels: map[string]string{v1.LabelPackageName: t.Name()}},
		Spec:       v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}},
	}
	mid := &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: "test", Labels: baseline.Labels},
		Spec:       v1.MiddlewareSpec{Baseline: baseline.Name, Parameters: runtime.RawExtension{Raw: []byte(desired)}},
	}
	spec, err := decodeSpec([]byte(actual))
	if err != nil {
		t.Fatal(err)
	}
	live := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	live.SetGroupVersionKind(gvk)
	live.SetName(mid.Name)
	live.SetNamespace(mid.Namespace)
	live.SetUID("original-uid")
	mid.UID = "mid-uid"
	mid.ResourceVersion = "999"
	controller := true
	live.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Middleware", Name: mid.Name, UID: mid.UID, Controller: &controller}})
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline, mid, live).Build(), mid, live
}

func TestPauseAdoptionUsesActualBeforeAndAfter(t *testing.T) {
	configurePauseTestSchemas(t)
	for _, tc := range []struct{ name, desired, before, after, want string }{
		{"unchanged defaults and drift", `{"replicas":3}`, `{"replicas":2,"default":"yes","nested":{"selector":null}}`, `{"replicas":2,"default":"yes","nested":{"selector":null}}`, `{}`},
		{"only changed replica", `{"replicas":3}`, `{"replicas":2,"default":"yes"}`, `{"replicas":5,"default":"yes"}`, `{"replicas":5}`},
		{"changed observed default", `{"replicas":3}`, `{"replicas":3,"default":"before"}`, `{"replicas":3,"default":"after"}`, `{"default":"after"}`},
		{"removed desired field", `{"replicas":3,"legacy":true}`, `{"replicas":3,"legacy":true,"default":"yes"}`, `{"replicas":3,"default":"yes"}`, `{"legacy":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cli, mid, live := pauseAdoptionFixture(t, tc.desired, tc.before)
			snapshot, err := BuildReconcilePauseSnapshot(context.Background(), cli, mid)
			if err != nil {
				t.Fatal(err)
			}
			before := loadPauseActualSpec(t, cli, mid, snapshot)
			wantBefore, _ := decodeSpec([]byte(tc.before))
			if !reflect.DeepEqual(before, wantBefore) {
				t.Fatalf("snapshot is not actual before: %#v", before)
			}
			live.Object["spec"], err = decodeSpec([]byte(tc.after))
			if err != nil {
				t.Fatal(err)
			}
			if err := cli.Update(context.Background(), live); err != nil {
				t.Fatal(err)
			}
			adoption, err := AdoptPausedPrimaryCustomResource(context.Background(), cli, mid, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			got := "{}"
			if adoption.Overrides != nil {
				got = string(adoption.Overrides.SpecPatch.Raw)
			}
			want, _ := decodeSpec([]byte(tc.want))
			actual, _ := decodeSpec([]byte(got))
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("patch = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPauseAdoptionRetryDropsRevertedIntermediateChanges(t *testing.T) {
	configurePauseTestSchemas(t)
	cli, mid, live := pauseAdoptionFixture(t, `{"replicas":3,"storage":"10Gi"}`, `{"replicas":3,"storage":"20Gi","default":"yes"}`)
	gvk := live.GroupVersionKind()
	mid.Spec.ReconcileOverrides = &v1.ReconcileOverrides{
		GVK: v1.GVK{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind}, Namespace: mid.Namespace, Name: mid.Name,
		BaseSpec:  runtime.RawExtension{Raw: []byte(`{"replicas":3,"storage":"10Gi"}`)},
		SpecPatch: runtime.RawExtension{Raw: []byte(`{"storage":"20Gi"}`)},
	}
	snapshot, err := BuildReconcilePauseSnapshot(context.Background(), cli, mid)
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(live.Object, float64(5), "spec", "replicas"); err != nil {
		t.Fatal(err)
	}
	if err := cli.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	first, err := AdoptPausedPrimaryCustomResource(context.Background(), cli, mid, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	mid.Spec.ReconcileOverrides = first.Overrides
	if err := unstructured.SetNestedField(live.Object, float64(3), "spec", "replicas"); err != nil {
		t.Fatal(err)
	}
	if err := cli.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	second, err := AdoptPausedPrimaryCustomResource(context.Background(), cli, mid, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if second.Overrides == nil || string(second.Overrides.SpecPatch.Raw) != `{"storage":"20Gi"}` {
		t.Fatalf("retry retained a reverted change or lost prior override: %#v", second.Overrides)
	}
	mid.Spec.ReconcileOverrides = second.Overrides
	// A second pause has no actual changes; the already adopted override stays.
	next, err := BuildReconcilePauseSnapshot(context.Background(), cli, mid)
	if err != nil {
		t.Fatal(err)
	}
	third, err := AdoptPausedPrimaryCustomResource(context.Background(), cli, mid, next)
	if err != nil {
		t.Fatal(err)
	}
	if third.Overrides == nil || string(third.Overrides.SpecPatch.Raw) != `{"storage":"20Gi"}` {
		t.Fatal("next pause lost the previous override")
	}
}

func TestPauseAdoptionRejectsLegacyCorruptAndReplacedSnapshots(t *testing.T) {
	for _, scenario := range []string{"legacy desired snapshot", "corrupt actual snapshot", "recreated target", "missing target"} {
		t.Run(scenario, func(t *testing.T) {
			cli, mid, live := pauseAdoptionFixture(t, `{"replicas":3}`, `{"replicas":3,"default":"yes"}`)
			snapshot, err := BuildReconcilePauseSnapshot(context.Background(), cli, mid)
			if err != nil {
				t.Fatal(err)
			}
			want := ErrReconcilePauseSnapshotInvalid
			switch scenario {
			case "legacy desired snapshot":
				snapshot.SnapshotSecret = ""
			case "corrupt actual snapshot":
				snapshot.Hash = "corrupt"
			case "recreated target":
				live.SetUID("replacement-uid")
				if err := cli.Update(context.Background(), live); err != nil {
					t.Fatal(err)
				}
				want = ErrReconcilePauseResourceChanged
			case "missing target":
				if err := cli.Delete(context.Background(), live); err != nil {
					t.Fatal(err)
				}
				want = ErrReconcilePauseResourceMissing
			}
			if _, err := AdoptPausedPrimaryCustomResource(context.Background(), cli, mid, snapshot); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func loadPauseActualSpec(t *testing.T, cli client.Client, mid *v1.Middleware, snapshot *v1.ReconcilePauseStatus) map[string]any {
	t.Helper()
	data, err := k8s.NewManagedResourceWriter(cli).LoadReconcilePauseSnapshot(context.Background(), mid, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data.ActualSpec)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := decodeSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
