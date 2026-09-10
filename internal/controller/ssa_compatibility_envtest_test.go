//go:build envtest

package controller

import (
	"context"
	"encoding/json"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("SSA compatibility startup", func() {
	It("migrates converged MID and MO resources on startup and preserves Configuration markers", func() {
		k8s.ConfigureManagedResources(k8sClient)
		suffix := randomSuffix()
		pkg := "ssa-pkg-" + suffix
		midName := "ssa-mid-" + suffix
		moName := "ssa-mo-" + suffix
		labels := map[string]string{v1.LabelPackageName: pkg}
		spec := `{"replicas":0,"selector":{"matchLabels":{"app":"ssa-test"}},"template":{"metadata":{"labels":{"app":"ssa-test"}},"spec":{"containers":[{"name":"main","image":"registry.k8s.io/pause:3.10","env":[{"name":"KEEP","value":"yes"}]}]}}}`
		mb := &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: "ssa-mb-" + suffix, Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}, Parameters: runtime.RawExtension{Raw: []byte(spec)}}}
		Expect(k8sClient.Create(ctx, mb)).To(Succeed())
		mcf := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "ssa-mcf-" + suffix, Labels: labels}, Spec: v1.MiddlewareConfigurationSpec{Template: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + moName + "-config\n  namespace: default\ndata:\n  keep: \"yes\"\n"}}
		Expect(k8sClient.Create(ctx, mcf)).To(Succeed())
		mob := &v1.MiddlewareOperatorBaseline{ObjectMeta: metav1.ObjectMeta{Name: "ssa-mob-" + suffix, Labels: labels}}
		// Decode through JSON to avoid coupling this test to the Configuration reference Go type.
		refs, _ := json.Marshal(map[string]any{"configurations": []any{map[string]any{"name": mcf.Name}}})
		Expect(json.Unmarshal(refs, &mob.Spec)).To(Succeed())
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())
		mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: midName, Namespace: "default", Labels: labels, Finalizers: []string{v1.FinalizerMiddleware}}, Spec: v1.MiddlewareSpec{Baseline: mb.Name}}
		mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: moName, Namespace: "default", Labels: labels, Finalizers: []string{v1.FinalizerMiddlewareOperator}}, Spec: v1.MiddlewareOperatorSpec{Baseline: mob.Name}}
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())
		Expect(k8sClient.Create(ctx, mo)).To(Succeed())
		mid.Status.ObservedGeneration = mid.Generation
		mid.Status.State = v1.StateAvailable
		Expect(k8sClient.Status().Update(ctx, mid)).To(Succeed())
		mo.Status.ObservedGeneration = mo.Generation
		mo.Status.State = v1.StateAvailable
		Expect(k8sClient.Status().Update(ctx, mo)).To(Succeed())
		primary := &unstructured.Unstructured{}
		Expect(json.Unmarshal([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{}}`), &primary.Object)).To(Succeed())
		primary.SetName(midName)
		primary.SetNamespace("default")
		var oldSpec map[string]any
		Expect(json.Unmarshal([]byte(spec), &oldSpec)).To(Succeed())
		primary.Object["spec"] = oldSpec
		containers, _, _ := unstructured.NestedSlice(primary.Object, "spec", "template", "spec", "containers")
		containers[0].(map[string]any)["env"] = append(containers[0].(map[string]any)["env"].([]any), map[string]any{"name": "AUTH", "value": "legacy"})
		Expect(unstructured.SetNestedSlice(primary.Object, containers, "spec", "template", "spec", "containers")).To(Succeed())
		Expect(ctrl.SetControllerReference(mid, primary, k8sClient.Scheme())).To(Succeed())
		Expect(k8sClient.Create(ctx, primary, client.FieldOwner("manager"))).To(Succeed())
		config := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": moName + "-config", "namespace": "default"}, "data": map[string]any{"keep": "yes", "stale": "legacy"}}}
		config.SetAnnotations(map[string]string{v1.LabelConfigurations: mcf.Name, v1.AnnotationConfigurationOwnerUID: string(mo.UID), v1.AnnotationConfigurationUID: string(mcf.UID)})
		Expect(ctrl.SetControllerReference(mo, config, k8sClient.Scheme())).To(Succeed())
		Expect(k8sClient.Create(ctx, config, client.FieldOwner("manager"))).To(Succeed())
		approve := func(owner client.Object, obj *unstructured.Unstructured, configuration string) {
			fields := obj.GetManagedFields()
			for i := range fields {
				if fields[i].Manager == "manager" && fields[i].Subresource == "" {
					fields[i].Manager = "opensaola"
					fields[i].Operation = metav1.ManagedFieldsOperationApply
				}
			}
			annotations := obj.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[k8s.AnnotationSSACliMigration] = "cli-fixture"
			raw, _ := json.Marshal([]map[string]any{{"op": "replace", "path": "/metadata/managedFields", "value": fields}, {"op": "add", "path": "/metadata/annotations", "value": annotations}})
			Expect(k8sClient.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, raw), client.FieldOwner("saola-ssa-migration"))).To(Succeed())
		}

		approve(mid, primary, "")
		approve(mo, config, mcf.Name)
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8sClient.Scheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
		Expect(err).NotTo(HaveOccurred())
		Expect((&MiddlewareReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: record.NewFakeRecorder(100)}).SetupWithManager(mgr)).To(Succeed())
		Expect((&MiddlewareOperatorReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: record.NewFakeRecorder(100)}).SetupWithManager(mgr)).To(Succeed())
		run, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(run) }()
		defer func() { cancel(); Eventually(done).WithTimeout(10 * time.Second).Should(Receive(BeNil())) }()
		Eventually(func(g Gomega) {
			p := &unstructured.Unstructured{}
			p.SetGroupVersionKind(primary.GroupVersionKind())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), p)).To(Succeed())
			containers, _, _ := unstructured.NestedSlice(p.Object, "spec", "template", "spec", "containers")
			g.Expect(containers[0].(map[string]any)["env"]).To(Equal([]any{map[string]any{"name": "KEEP", "value": "yes"}}))
			c := &unstructured.Unstructured{}
			c.SetGroupVersionKind(config.GroupVersionKind())
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), c)).To(Succeed())
			data, _, _ := unstructured.NestedStringMap(c.Object, "data")
			g.Expect(data).To(Equal(map[string]string{"keep": "yes"}))
			g.Expect(c.GetAnnotations()[v1.AnnotationConfigurationOwnerUID]).To(Equal(string(mo.UID)))
			g.Expect(c.GetAnnotations()[v1.AnnotationConfigurationUID]).To(Equal(string(mcf.UID)))
			got := &v1.Middleware{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: midName, Namespace: "default"}, got)).To(Succeed())
			g.Expect(k8s.SSACompatibilityCurrent(got)).To(BeTrue())
			g.Expect(got.Generation).To(Equal(mid.Generation))
		}).WithTimeout(20 * time.Second).Should(Succeed())
	})
})
