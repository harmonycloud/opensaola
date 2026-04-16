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
	"math/rand"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/harmonycloud/opensaola/api/v1"
)

// ---------- helpers ----------

func newMOB(name string, gvks []v1.GVK) *v1.MiddlewareOperatorBaseline {
	return &v1.MiddlewareOperatorBaseline{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1.MiddlewareOperatorBaselineSpec{GVKs: gvks},
	}
}

func reconcileMOB(name string) (reconcile.Result, error) {
	r := &MiddlewareOperatorBaselineReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	return r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
}

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func randomSuffix() string {
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}

var _ = Describe("MOB Envtest", func() {

	It("Envtest_MOB_ObservedGeneration_ValidSpec", func() {
		name := "mob-valid-" + randomSuffix()
		mob := newMOB(name, []v1.GVK{{Name: "test", Group: "apps", Version: "v1", Kind: "Deployment"}})
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		_, err := reconcileMOB(name)
		Expect(err).NotTo(HaveOccurred())

		got := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())

		Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
		Expect(got.Status.State).To(Equal(v1.StateAvailable))
		cond := findCondition(got.Status.Conditions, v1.CondTypeChecked)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	})

	It("Envtest_MOB_Idempotency", func() {
		name := "mob-idem-" + randomSuffix()
		mob := newMOB(name, []v1.GVK{{Name: "test", Group: "apps", Version: "v1", Kind: "Deployment"}})
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		_, err := reconcileMOB(name)
		Expect(err).NotTo(HaveOccurred())

		got := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
		rvBefore := got.ResourceVersion

		// Second reconcile should be a no-op (guard skips already-checked)
		_, err = reconcileMOB(name)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
		Expect(got.ResourceVersion).To(Equal(rvBefore))
	})

	It("Envtest_MOB_InvalidSpec_EmptyGVKs", func() {
		name := "mob-empty-gvks-" + randomSuffix()
		mob := newMOB(name, nil)
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		_, err := reconcileMOB(name)
		Expect(err).NotTo(HaveOccurred())

		got := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())

		cond := findCondition(got.Status.Conditions, v1.CondTypeChecked)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Message).To(ContainSubstring("gvks must not be empty"))
	})

	It("Envtest_MOB_InvalidSpec_GVKMissingFields", func() {
		name := "mob-bad-gvk-" + randomSuffix()
		mob := newMOB(name, []v1.GVK{{Name: "test", Group: "", Version: "v1", Kind: "Deployment"}})
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		_, err := reconcileMOB(name)
		Expect(err).NotTo(HaveOccurred())

		got := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())

		Expect(got.Status.State).To(Equal(v1.StateUnavailable))
		cond := findCondition(got.Status.Conditions, v1.CondTypeChecked)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Message).To(ContainSubstring("GVK must not be empty"))
	})

	It("Envtest_MOB_SpecUpdate_ReCheck", func() {
		name := "mob-recheck-" + randomSuffix()
		mob := newMOB(name, []v1.GVK{{Name: "test", Group: "apps", Version: "v1", Kind: "Deployment"}})
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		_, err := reconcileMOB(name)
		Expect(err).NotTo(HaveOccurred())

		// Update spec to trigger generation bump
		got := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
		genBefore := got.Generation

		got.Spec.GVKs = append(got.Spec.GVKs, v1.GVK{Name: "extra", Group: "batch", Version: "v1", Kind: "Job"})
		Expect(k8sClient.Update(ctx, got)).To(Succeed())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
		Expect(got.Generation).To(BeNumerically(">", genBefore))

		_, err = reconcileMOB(name)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
		Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
	})

	It("Envtest_MOB_NotFound_NoError", func() {
		_, err := reconcileMOB("mob-nonexistent-" + randomSuffix())
		Expect(err).NotTo(HaveOccurred())
	})
})
