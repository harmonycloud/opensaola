//go:build envtest

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
	"encoding/json"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Owned field deletion status", func() {
	It("keeps the generation pending after blocked deletion and retries without changing MID", func() {
		resolver, err := k8s.NewIntentSchemaResolver(cfg)
		Expect(err).NotTo(HaveOccurred())
		k8s.ConfigureManagedResources(k8sClient)
		k8s.ConfigureResourceIntents(resolver)
		DeferCleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
		name := "owned-deletion-" + randomSuffix()
		labels := map[string]string{v1.LabelPackageName: name}
		baseline := &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}}}
		Expect(k8sClient.Create(ctx, baseline)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, baseline) })
		container := map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "env": []any{map[string]any{"name": "OWN", "value": "sensitive-owned-value"}}}
		spec := map[string]any{
			"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": name}},
			"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": name}}, "spec": map[string]any{"containers": []any{container}}},
		}
		raw, err := json.Marshal(spec)
		Expect(err).NotTo(HaveOccurred())
		mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels, Finalizers: []string{v1.FinalizerMiddleware}}, Spec: v1.MiddlewareSpec{Baseline: name, Parameters: runtime.RawExtension{Raw: raw}}}
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())
		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		key := client.ObjectKeyFromObject(mid)
		Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
		previousGeneration := mid.Status.ObservedGeneration
		primary := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, primary) })
		primary.Spec.Template.Spec.Containers[0].Env[0].Value = "sensitive-external-value"
		Expect(k8sClient.Update(ctx, primary)).To(Succeed())
		before := primary.DeepCopy()

		delete(container, "env")
		Expect(unstructured.SetNestedStringMap(spec, map[string]string{"unrelated-write": "must-wait-for-deletion"}, "template", "metadata", "annotations")).To(Succeed())
		raw, err = json.Marshal(spec)
		Expect(err).NotTo(HaveOccurred())
		mid.Spec.Parameters = runtime.RawExtension{Raw: raw}
		Expect(k8sClient.Update(ctx, mid)).To(Succeed())
		pendingGeneration := mid.Generation
		_, err = reconcileMiddleware(name, "default")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("sensitive-"))
		Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
		condition := meta.FindStatusCondition(mid.Status.Conditions, v1.CondTypeApplyCluster)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Message).To(ContainSubstring("causeCategory=Conflict"))
		Expect(condition.Message).NotTo(ContainSubstring("sensitive-"))
		Expect(mid.Status.ObservedGeneration).To(Equal(previousGeneration))
		Expect(mid.Status.State).To(Equal(v1.StateUnavailable))
		Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
		Expect(primary.Spec).To(Equal(before.Spec))
		Expect(primary.ResourceVersion).To(Equal(before.ResourceVersion))
		Expect(primary.Annotations[k8s.AnnotationResourceIntent]).To(Equal(before.Annotations[k8s.AnnotationResourceIntent]))

		primary.Spec.Template.Spec.Containers[0].Env[0].Value = "sensitive-owned-value"
		Expect(k8sClient.Update(ctx, primary)).To(Succeed())
		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
		Expect(mid.Generation).To(Equal(pendingGeneration))
		Expect(mid.Status.ObservedGeneration).To(Equal(pendingGeneration))
		Expect(meta.FindStatusCondition(mid.Status.Conditions, v1.CondTypeApplyCluster).Status).To(Equal(metav1.ConditionTrue))
		Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
		Expect(primary.Spec.Template.Spec.Containers[0].Env).To(BeEmpty())
		Expect(primary.Spec.Template.Annotations["unrelated-write"]).To(Equal("must-wait-for-deletion"))
	})
})

var _ = Describe("MID native init container execution order", func() {
	It("merges explicitly sequenced baseline names and preserves injected steps", func() {
		resolver, err := k8s.NewIntentSchemaResolver(cfg)
		Expect(err).NotTo(HaveOccurred())
		k8s.ConfigureManagedResources(k8sClient)
		k8s.ConfigureResourceIntents(resolver)
		DeferCleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
		name := "init-order-" + randomSuffix()
		labels := map[string]string{v1.LabelPackageName: name}
		image := "registry.k8s.io/pause:3.10"
		base := map[string]any{"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": name}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": name}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": image, "volumeMounts": []any{map[string]any{"name": "log", "mountPath": "/a", "readOnly": true}}}}, "volumes": []any{map[string]any{"name": "log", "emptyDir": map[string]any{}}}, "initContainers": []any{map[string]any{"name": "prepare", "image": image}, map[string]any{"name": "migrate", "image": image}}}}}
		baseJSON, err := json.Marshal(base)
		Expect(err).NotTo(HaveOccurred())
		baseline := &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}, Parameters: runtime.RawExtension{Raw: baseJSON}}}
		Expect(k8sClient.Create(ctx, baseline)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, baseline) })
		mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels, Finalizers: []string{v1.FinalizerMiddleware}}, Spec: v1.MiddlewareSpec{Baseline: name}}
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())
		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		key := client.ObjectKeyFromObject(mid)
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, dep) })
		steps := dep.Spec.Template.Spec.InitContainers
		Expect(steps).To(HaveLen(2))
		Expect(steps[0].Name).To(Equal("prepare"))
		injected := steps[0].DeepCopy()
		injected.Name = "external"
		dep.Spec.Template.Spec.InitContainers = []corev1.Container{steps[0], *injected, steps[1]}
		Expect(k8sClient.Update(ctx, dep)).To(Succeed())
		Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
		mid.Spec.Parameters = runtime.RawExtension{Raw: []byte(`{"template":{"spec":{"containers":[{"name":"main","volumeMounts":[{"name":"log","mountPath":"/b"}]}],"initContainers":[{"name":"migrate"},{"name":"prepare"}]}}}`)}
		Expect(k8sClient.Update(ctx, mid)).To(Succeed())
		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, dep)).To(Succeed())
		steps = dep.Spec.Template.Spec.InitContainers
		Expect(steps).To(HaveLen(3))
		Expect(dep.Spec.Template.Spec.Containers[0].VolumeMounts).To(Equal([]corev1.VolumeMount{
			{Name: "log", MountPath: "/a", ReadOnly: true},
			{Name: "log", MountPath: "/b"},
		}))
		Expect([]string{steps[0].Name, steps[1].Name, steps[2].Name}).To(Equal([]string{"migrate", "external", "prepare"}))
		for _, step := range steps {
			Expect(step.Image).To(Equal(image))
		}
		Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
		Expect(mid.Status.ObservedGeneration).To(Equal(mid.Generation))
	})
})
