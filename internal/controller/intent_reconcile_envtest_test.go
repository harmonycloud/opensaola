//go:build envtest

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

package controller

import (
	"encoding/json"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Recorded resource intent controller integration", func() {
	for _, legacy := range []bool{false, true} {
		title := "publishes new resources then deletes an omitted env on the next generation"
		if legacy {
			title = "leaves old steady-state resources untouched then records normal updates"
		}
		It(title, func() {
			resolver, err := k8s.NewIntentSchemaResolver(cfg)
			Expect(err).NotTo(HaveOccurred())
			k8s.ConfigureManagedResources(k8sClient)
			k8s.ConfigureResourceIntents(resolver)
			DeferCleanup(func() { k8s.ConfigureResourceIntents(nil) })
			name := "intent-controller-" + randomSuffix()
			labels := map[string]string{v1.LabelPackageName: name}
			mb := &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}}}
			Expect(k8sClient.Create(ctx, mb)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, mb) })
			spec := map[string]any{"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": name}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": name}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "env": []any{map[string]any{"name": "KEEP", "value": "before"}, map[string]any{"name": "AUTH", "value": "on"}}}}}}}
			raw, err := json.Marshal(spec)
			Expect(err).NotTo(HaveOccurred())
			mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels, Finalizers: []string{v1.FinalizerMiddleware}}, Spec: v1.MiddlewareSpec{Baseline: name, Parameters: runtime.RawExtension{Raw: raw}}}
			Expect(k8sClient.Create(ctx, mid)).To(Succeed())
			primary := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": "default"}, "spec": runtime.DeepCopyJSONValue(spec)}}
			if legacy {
				primary.SetAnnotations(map[string]string{"middleware.cn/ssa-cli-migration": "unreleased-fixture"})
				Expect(ctrl.SetControllerReference(mid, primary, k8sClient.Scheme())).To(Succeed())
				Expect(k8sClient.Create(ctx, primary, client.FieldOwner("manager"))).To(Succeed())
				mid.Status.ObservedGeneration = mid.Generation
				mid.Status.State = v1.StateAvailable
				Expect(k8sClient.Status().Update(ctx, mid)).To(Succeed())
				oldRV := primary.GetResourceVersion()
				_, err = reconcileMiddleware(name, "default")
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary)).To(Succeed())
				Expect(primary.GetResourceVersion()).To(Equal(oldRV))
				Expect(primary.GetAnnotations()).NotTo(HaveKey(k8s.AnnotationResourceIntent))
				// A genuine spec change, not an upgrade-time bulk migration, bootstraps B.
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mid), mid)).To(Succeed())
				mid.Spec.Necessary = runtime.RawExtension{Raw: []byte(`{"intentTest":"bootstrap"}`)}
				Expect(k8sClient.Update(ctx, mid)).To(Succeed())
			}
			_, err = reconcileMiddleware(name, "default")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary)).To(Succeed())
			Expect(primary.GetAnnotations()[k8s.AnnotationResourceIntent]).NotTo(BeEmpty())
			Expect(k8sClient.Patch(ctx, primary, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"add","path":"/spec/template/spec/containers/0/env/-","value":{"name":"EXTERNAL","value":"preserve"}}]`)), client.FieldOwner("manager"))).To(Succeed())
			containers, _, err := unstructured.NestedSlice(spec, "template", "spec", "containers")
			Expect(err).NotTo(HaveOccurred())
			container, ok := containers[0].(map[string]any)
			Expect(ok).To(BeTrue())
			container["env"] = []any{map[string]any{"name": "KEEP", "value": "after"}}
			Expect(unstructured.SetNestedSlice(spec, containers, "template", "spec", "containers")).To(Succeed())
			raw, err = json.Marshal(spec)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mid), mid)).To(Succeed())
			mid.Spec.Parameters = runtime.RawExtension{Raw: raw}
			Expect(k8sClient.Update(ctx, mid)).To(Succeed())
			_, err = reconcileMiddleware(name, "default")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary)).To(Succeed())
			containers, _, err = unstructured.NestedSlice(primary.Object, "spec", "template", "spec", "containers")
			Expect(err).NotTo(HaveOccurred())
			container, ok = containers[0].(map[string]any)
			Expect(ok).To(BeTrue())
			env, ok := container["env"].([]any)
			Expect(ok).To(BeTrue())
			Expect(env).To(ConsistOf(map[string]any{"name": "KEEP", "value": "after"}, map[string]any{"name": "EXTERNAL", "value": "preserve"}))
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mid), mid)).To(Succeed())
			Expect(mid.Status.ObservedGeneration).To(Equal(mid.Generation))
			Expect(mid.Annotations).NotTo(HaveKey("middleware.cn/ssa-compatible-generation"))
			Expect(primary.GetAnnotations()).NotTo(HaveKey("middleware.cn/ssa-cli-converged"))
			rv := primary.GetResourceVersion()
			_, err = reconcileMiddleware(name, "default")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary)).To(Succeed())
			Expect(primary.GetResourceVersion()).To(Equal(rv))
		})
	}
})
