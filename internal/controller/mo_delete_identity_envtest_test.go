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
	"fmt"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("MO Configuration deletion", func() {
	var (
		owner         *v1.MiddlewareOperator
		configuration *v1.MiddlewareConfiguration
		targetName    string
		reconciler    *MiddlewareOperatorReconciler
	)
	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "mo-delete-"}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		suffix := namespace.Name
		targetName = "target-" + suffix
		labels := map[string]string{v1.LabelPackageName: "package-" + suffix}
		configuration = &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "config-" + suffix, Labels: labels}}
		owner = &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "operator-" + suffix, Namespace: namespace.Name, Labels: labels}}
		owner.Spec.Baseline = "baseline-" + suffix
		owner.Spec.Globe = &runtime.RawExtension{Raw: []byte(`{"targetVersion":"v1","targetKind":"ConfigMap","targetNamespace":"default"}`)}
		reconciler = &MiddlewareOperatorReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(32)}
	})
	reconcile := func() error {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(owner)})
		return err
	}
	publish := func(manifest string, target client.Object, key client.ObjectKey) {
		GinkgoHelper()
		configuration.Spec.Template = manifest
		Expect(k8sClient.Create(ctx, configuration)).To(Succeed())
		baseline := newMOB(owner.Spec.Baseline, nil)
		baseline.Labels = owner.Labels
		baseline.Spec.Configurations = []v1.Configuration{{Name: configuration.Name}}
		baseline.Spec.Deployment = &runtime.RawExtension{Raw: []byte(`{"spec":{"replicas":0,"selector":{"matchLabels":{"app":"operator"}},"template":{"metadata":{"labels":{"app":"operator"}},"spec":{"containers":[{"name":"operator","image":"registry.k8s.io/pause:3.10"}]}}}}`)}
		Expect(k8sClient.Create(ctx, baseline)).To(Succeed())
		Expect(k8sClient.Create(ctx, owner)).To(Succeed())
		Expect(reconcile()).To(Succeed()) // Add the finalizer.
		Expect(reconcile()).To(Succeed()) // Publish the target and record its identity.
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(owner), owner)).To(Succeed())
		Expect(owner.Finalizers).To(ContainElement(v1.FinalizerMiddlewareOperator))
		Expect(k8sClient.Get(ctx, key, target)).To(Succeed())
		Expect(owner.Status.RenderedConfigurationResources).To(HaveLen(1))
		entry := owner.Status.RenderedConfigurationResources[0]
		Expect(entry.OwnerUID).To(Equal(owner.UID))
		Expect(entry.ResourceUID).To(Equal(target.GetUID()))
		Expect(client.ObjectKey{Name: entry.Name, Namespace: entry.Namespace}).To(Equal(key))
	}
	discardInventory := func() {
		GinkgoHelper()
		owner.Status.RenderedConfigurationResources = nil
		owner.Status.RenderedConfigurationResourcesGeneration = 0
		Expect(k8sClient.Status().Update(ctx, owner)).To(Succeed())
	}
	changeTemplate := func(manifest string) {
		GinkgoHelper()
		configuration.Spec.Template = manifest
		Expect(k8sClient.Update(ctx, configuration)).To(Succeed())
	}
	expectDeleted := func(obj client.Object) {
		GinkgoHelper()
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "GET %T %s: %v", obj, client.ObjectKeyFromObject(obj), err)
	}
	finishDeletion := func() {
		GinkgoHelper()
		Expect(reconcile()).To(Succeed())
		expectDeleted(owner)
	}
	configMapManifest := func() string {
		return fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: default\n", targetName)
	}
	// Matching lifecycle markers make these decoys a meaningful check on addressing.
	createLike := func(original *corev1.ConfigMap, name, namespace string) *corev1.ConfigMap {
		GinkgoHelper()
		copy := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: original.Labels, Annotations: original.Annotations}}
		Expect(k8sClient.Create(ctx, copy)).To(Succeed())
		return copy
	}
	expectUnchanged := func(cm *corev1.ConfigMap) {
		GinkgoHelper()
		live := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cm), live)).To(Succeed())
		Expect(live.UID).To(Equal(cm.UID))
	}
	for _, history := range []string{"with inventory", "without inventory"} {
		Context(history, func() {
			DescribeTable("deletes the published cross-namespace object", func(template string) {
				manifest := fmt.Sprintf(template, targetName)
				target := &corev1.ConfigMap{}
				publish(manifest, target, client.ObjectKey{Name: targetName, Namespace: "default"})
				Expect(target.OwnerReferences).To(BeEmpty())
				if history == "without inventory" {
					discardInventory()
				}
				Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
				finishDeletion()
				expectDeleted(target)
			},
				Entry("plain YAML", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: default\n"),
				Entry("quoted YAML", "apiVersion: 'v1'\nkind: 'ConfigMap'\nmetadata:\n  name: %s\n  namespace: default\n"),
				Entry("one-line JSON", `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"%s","namespace":"default"}}`),
				Entry("templated apiVersion, kind and namespace", "apiVersion: '{{ .Globe.targetVersion }}'\nkind: '{{ .Globe.targetKind }}'\nmetadata:\n  name: %s\n  namespace: '{{ .Globe.targetNamespace }}'\n"),
				Entry("business data changes a value after namespace was rendered", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: '{{ .Globe.targetNamespace }}'\ndata:\n  body: |\n    {{ $_ := set .Globe \"targetNamespace\" \"different\" }}ready\n"),
			)
			It("uses the published UID when an earlier action changes the namespace", func() {
				target := &corev1.ConfigMap{}
				owner.Spec.Globe = &runtime.RawExtension{Raw: []byte(fmt.Sprintf(`{"targetNamespace":%q}`, owner.Namespace))}
				manifest := `{{ $_ := set .Globe "targetNamespace" "default" }}` + "\n" +
					strings.Replace(configMapManifest(), "namespace: default", `namespace: '{{ .Globe.targetNamespace }}'`, 1)
				publish(manifest, target, client.ObjectKey{Name: targetName, Namespace: "default"})
				decoy := createLike(target, targetName, owner.Namespace)
				if history == "without inventory" {
					discardInventory()
				}
				Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
				if history == "without inventory" {
					Expect(reconcile()).To(HaveOccurred())
					Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(owner), owner)).To(Succeed())
					Expect(owner.Finalizers).To(ContainElement(v1.FinalizerMiddlewareOperator))
					expectUnchanged(target)
					expectUnchanged(decoy)
					changeTemplate(configMapManifest())
				}
				finishDeletion()
				expectDeleted(target)
				expectUnchanged(decoy)
			})
			It("deletes a whole-object template only with a trusted identity", func() {
				target := &corev1.ConfigMap{}
				owner.Spec.Globe = &runtime.RawExtension{Raw: []byte(fmt.Sprintf(
					`{"object":{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":%q,"namespace":"default"}}}`, targetName))}
				publish("{{ toYaml .Globe.object }}", target, client.ObjectKey{Name: targetName, Namespace: "default"})
				if history == "without inventory" {
					discardInventory()
				}
				Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
				if history == "without inventory" {
					Expect(reconcile()).To(HaveOccurred())
					Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(owner), owner)).To(Succeed())
					Expect(owner.DeletionTimestamp).NotTo(BeNil())
					Expect(owner.Finalizers).To(ContainElement(v1.FinalizerMiddlewareOperator))
					expectUnchanged(target)
					changeTemplate(configMapManifest())
				}
				finishDeletion()
				expectDeleted(target)
			})
			It("retains both namespaces and the finalizer until the explicit namespace resolves", func() {
				target := &corev1.ConfigMap{}
				manifest := configMapManifest()
				publish(manifest, target, client.ObjectKey{Name: targetName, Namespace: "default"})
				decoy := createLike(target, targetName, owner.Namespace)
				if history == "without inventory" {
					discardInventory()
				}
				changeTemplate(strings.Replace(manifest, "namespace: default", `namespace: '{{ required "target namespace missing" .Globe.absent }}'`, 1))
				Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
				Expect(reconcile()).To(HaveOccurred())
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(owner), owner)).To(Succeed())
				Expect(owner.DeletionTimestamp).NotTo(BeNil())
				Expect(owner.Finalizers).To(ContainElement(v1.FinalizerMiddlewareOperator))
				expectUnchanged(target)
				expectUnchanged(decoy)
				changeTemplate(manifest)
				finishDeletion()
				expectDeleted(target)
				expectUnchanged(decoy)
			})
		})
	}
	It("can delete a legacy target when business fields no longer render", func() {
		target := &corev1.ConfigMap{}
		manifest := configMapManifest()
		publish(manifest, target, client.ObjectKey{Name: targetName, Namespace: "default"})
		discardInventory()
		changeTemplate(manifest + "data:\n  value: '{{ fail \"business value unavailable\" }}'\n")
		Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
		finishDeletion()
		expectDeleted(target)
	})
	It("deletes a cluster-scoped legacy target without evaluating namespace", func() {
		target := &rbacv1.ClusterRole{}
		manifest := fmt.Sprintf("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: %s\n", targetName)
		publish(manifest, target, client.ObjectKey{Name: targetName})
		discardInventory()
		changeTemplate(manifest + "  namespace: '{{ fail \"cluster resources have no namespace\" }}'\n")
		Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
		finishDeletion()
		expectDeleted(target)
	})
	It("deletes the recorded target after a template rename and preserves the newly named object", func() {
		original := &corev1.ConfigMap{}
		manifest := configMapManifest()
		publish(manifest, original, client.ObjectKey{Name: targetName, Namespace: "default"})
		renamed := createLike(original, targetName+"-new", "default")
		changeTemplate(strings.Replace(manifest, targetName, renamed.Name, 1))
		Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
		finishDeletion()
		expectDeleted(original)
		expectUnchanged(renamed)
	})
	It("leaves the recreated target with the same name and lifecycle markers untouched", func() {
		original := &corev1.ConfigMap{}
		publish(configMapManifest(), original, client.ObjectKey{Name: targetName, Namespace: "default"})
		Expect(k8sClient.Delete(ctx, original)).To(Succeed())
		replacement := createLike(original, targetName, "default")
		Expect(replacement.UID).NotTo(Equal(original.UID))
		Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
		finishDeletion()
		expectUnchanged(replacement)
	})
})
