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

	v1 "github.com/harmonycloud/opensaola/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("MO partial inventory recovery", func() {
	DescribeTable("completes a failed first publication", func(withdraw bool) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "mo-partial-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		missing := ns.Name + "-missing"
		labels := map[string]string{v1.LabelPackageName: ns.Name}
		newConfig := func(name, namespace string) *v1.MiddlewareConfiguration {
			m := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: ns.Name + "-" + name, Labels: labels, Annotations: map[string]string{v1.AnnotationConfigurationDisablePolicy: "delete"}}}
			m.Spec.Template = fmt.Sprintf("{{ if .Globe.enabled }}apiVersion: v1\nkind: ConfigMap\nmetadata: {name: %s, namespace: %s}\ndata: {test: complete}\n{{ end }}", name, namespace)
			Expect(k8sClient.Create(ctx, m)).To(Succeed())
			return m
		}
		a, b := newConfig("a", ns.Name), newConfig("b", missing)
		mob := newMOB(ns.Name, nil)
		mob.Labels = labels
		mob.Spec.Configurations = []v1.Configuration{{Name: a.Name}}
		mob.Spec.Deployment = &runtime.RawExtension{Raw: []byte(`{"spec":{"replicas":0,"selector":{"matchLabels":{"app":"operator"}},"template":{"metadata":{"labels":{"app":"operator"}},"spec":{"containers":[{"name":"operator","image":"registry.k8s.io/pause:3.10"}]}}}}`)}
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())
		mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "operator-" + ns.Name, Namespace: ns.Name, Labels: labels}}
		mo.Spec.Baseline = mob.Name
		mo.Spec.Configurations = []v1.Configuration{{Name: b.Name}}
		mo.Spec.Globe = &runtime.RawExtension{Raw: []byte(`{"enabled":true}`)}
		Expect(k8sClient.Create(ctx, mo)).To(Succeed())
		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mo)}
		reconcile := func() (ctrl.Result, error) {
			r := &MiddlewareOperatorReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(32)}
			return r.Reconcile(ctx, req)
		}
		initialGeneration := mo.Generation
		By("installing the finalizer and publishing A before B fails")
		_, err := reconcile()
		Expect(err).NotTo(HaveOccurred())
		_, err = reconcile()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(b.Name))
		getMO := func() *v1.MiddlewareOperator {
			current := &v1.MiddlewareOperator{}
			Expect(k8sClient.Get(ctx, req.NamespacedName, current)).To(Succeed())
			return current
		}
		mo = getMO()
		Expect(mo.Status.State).To(Equal(v1.StateUnavailable))
		Expect(mo.Status.ObservedGeneration).To(BeZero())
		Expect(mo.Status.RenderedConfigurationResourcesGeneration).To(BeZero())
		Expect(mo.Status.RenderedConfigurationResources).To(HaveLen(1))
		keyA := client.ObjectKey{Namespace: ns.Name, Name: "a"}
		keyB := client.ObjectKey{Namespace: missing, Name: "b"}
		var liveA corev1.ConfigMap
		Expect(k8sClient.Get(ctx, keyA, &liveA)).To(Succeed())
		receipt := mo.Status.RenderedConfigurationResources[0]
		Expect(receipt.ConfigurationUID).To(Equal(a.UID))
		Expect(receipt.ResourceUID).To(Equal(liveA.UID))
		Expect(receipt.OwnerUID).To(Equal(mo.UID))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, keyB, &corev1.ConfigMap{}))).To(BeTrue())
		if withdraw {
			By("withdrawing both rendered templates in the next MO generation")
			mo.Spec.Globe = &runtime.RawExtension{Raw: []byte(`{"enabled":false}`)}
			Expect(k8sClient.Update(ctx, mo)).To(Succeed())
			Expect(mo.Generation).To(BeNumerically(">", initialGeneration))
		} else {
			By("repairing only B's missing namespace and retrying the original MO generation")
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: missing}})).To(Succeed())
			Expect(mo.Generation).To(Equal(initialGeneration))
		}
		_, err = reconcile()
		Expect(err).NotTo(HaveOccurred())
		mo = getMO()
		Expect(mo.Status.PendingConfigurationWrite).To(BeNil())
		Expect(mo.Status.ObservedGeneration).To(Equal(mo.Generation))
		Expect(mo.Status.RenderedConfigurationResourcesGeneration).To(Equal(mo.Generation))
		if withdraw {
			Expect(mo.Status.RenderedConfigurationResources).To(BeEmpty())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, keyA, &corev1.ConfigMap{}))).To(BeTrue())
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, keyB, &corev1.ConfigMap{}))).To(BeTrue())
		} else {
			Expect(mo.Status.RenderedConfigurationResources).To(HaveLen(2))
			var aa, bb corev1.ConfigMap
			Expect(k8sClient.Get(ctx, keyA, &aa)).To(Succeed())
			Expect(k8sClient.Get(ctx, keyB, &bb)).To(Succeed())
			Expect(aa.UID).To(Equal(liveA.UID))
			Expect(mo.Status.RenderedConfigurationResources).To(ContainElement(HaveField("ResourceUID", bb.UID)))
		}
	}, Entry("after the user withdraws it", true), Entry("on the same generation after repairing B", false))
})
