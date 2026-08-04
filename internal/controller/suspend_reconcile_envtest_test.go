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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/harmonycloud/opensaola/api/v1"
)

var _ = Describe("Suspend reconcile", func() {
	It("pauses Middleware writes and blocks an unsafe direct resume", func() {
		name := "mid-suspend-" + randomSuffix()
		mid := &v1.Middleware{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Annotations: map[string]string{
					v1.AnnotationSuspendReconcile: "true",
				},
			},
			// The baseline intentionally does not exist. Reaching normal resource
			// reconciliation would fail, which makes this a strong pause gate test.
			Spec: v1.MiddlewareSpec{Baseline: "missing-baseline"},
		}
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())

		first, err := reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Requeue).To(BeTrue())

		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())

		got := &v1.Middleware{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		paused := findCondition(got.Status.Conditions, v1.CondTypeReconcilePaused)
		Expect(paused).NotTo(BeNil())
		Expect(paused.Status).To(Equal(metav1.ConditionTrue))
		Expect(got.Status.ObservedGeneration).To(BeZero())
		Expect(findCondition(got.Status.Conditions, v1.CondTypeTemplateParseWithBaseline)).To(BeNil())
		Expect(findCondition(got.Status.Conditions, v1.CondTypeApplyCluster)).To(BeNil())

		delete(got.Annotations, v1.AnnotationSuspendReconcile)
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		// A direct removal used to replay stale desired state. It must now keep
		// the durable pause guard and require an explicit merge/apply policy.
		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		paused = findCondition(got.Status.Conditions, v1.CondTypeReconcilePaused)
		Expect(paused).NotTo(BeNil())
		Expect(paused.Status).To(Equal(metav1.ConditionTrue))
		adoption := findCondition(got.Status.Conditions, v1.CondTypeReconcileAdoption)
		Expect(adoption).NotTo(BeNil())
		Expect(adoption.Status).To(Equal(metav1.ConditionFalse))
		Expect(adoption.Reason).To(Equal(v1.CondReasonReconcileAdoptionFailed))
		Expect(findCondition(got.Status.Conditions, v1.CondTypeTemplateParseWithBaseline)).To(BeNil())
		Expect(findCondition(got.Status.Conditions, v1.CondTypeApplyCluster)).To(BeNil())
	})

	It("captures a durable MID snapshot and adopts a live primary CR change with merge", func() {
		name := "mid-suspend-merge-" + randomSuffix()
		baselineName := "mb-suspend-merge-" + randomSuffix()
		packageName := "pkg-suspend-merge-" + randomSuffix()
		baseline := &v1.MiddlewareBaseline{
			ObjectMeta: metav1.ObjectMeta{
				Name:   baselineName,
				Labels: map[string]string{v1.LabelPackageName: packageName},
			},
			Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}},
		}
		Expect(k8sClient.Create(ctx, baseline)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, baseline) })

		replicas := int32(1)
		primary := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "database", Image: "example.invalid/database:latest"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, primary)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, primary) })

		mid := &v1.Middleware{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   "default",
				Labels:      map[string]string{v1.LabelPackageName: packageName},
				Annotations: map[string]string{v1.AnnotationSuspendReconcile: "true"},
			},
			Spec: v1.MiddlewareSpec{
				Baseline:   baselineName,
				Parameters: runtime.RawExtension{Raw: []byte(`{"replicas":1}`)},
			},
		}
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())

		first, err := reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Requeue).To(BeTrue())
		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())

		got := &v1.Middleware{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		Expect(got.Status.ReconcilePause).NotTo(BeNil())
		Expect(got.Status.ReconcilePause.DesiredSpec.Raw).To(MatchJSON(`{"replicas":1}`))
		Expect(got.Status.ReconcilePause.ResourceUID).NotTo(BeEmpty())

		// The only deliberate DR change is the replica count. merge must not
		// write it back before the generated override is committed.
		replicas = 5
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, primary)).To(Succeed())
		primary.Spec.Replicas = &replicas
		Expect(k8sClient.Update(ctx, primary)).To(Succeed())
		got.Annotations[v1.AnnotationResumePolicy] = string(v1.ReconcileResumePolicyMerge)
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		result, err := reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue).To(BeTrue())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		Expect(got.Annotations).NotTo(HaveKey(v1.AnnotationSuspendReconcile))
		Expect(got.Spec.ReconcileOverrides).NotTo(BeNil())
		Expect(got.Spec.ReconcileOverrides.SpecPatch.Raw).To(ContainSubstring(`"replicas":5`))
		Expect(got.Spec.ReconcileOverrides.BaseSpec.Raw).To(MatchJSON(`{"replicas":1}`))
		Expect(got.Status.ReconcilePause.AdoptedLiveSpecHash).NotTo(BeEmpty())
		Expect(findCondition(got.Status.Conditions, v1.CondTypeReconcileAdoption).Status).To(Equal(metav1.ConditionTrue))

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, primary)).To(Succeed())
		Expect(*primary.Spec.Replicas).To(Equal(int32(5)))
	})

	It("skips MiddlewareOperator deployment drift reconciliation while suspended", func() {
		name := "mo-suspend-" + randomSuffix()
		mo := &v1.MiddlewareOperator{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Annotations: map[string]string{
					v1.AnnotationSuspendReconcile: "true",
				},
			},
		}
		Expect(k8sClient.Create(ctx, mo)).To(Succeed())

		replicas := int32(2)
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "operator", Image: "example.invalid/operator:latest"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, deployment)).To(Succeed())

		reconciler := &MiddlewareOperatorReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		Expect(reconciler.handleDeployment(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}})).To(Succeed())

		got := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		Expect(got.Spec.Replicas).NotTo(BeNil())
		Expect(*got.Spec.Replicas).To(Equal(int32(2)))
	})
})
