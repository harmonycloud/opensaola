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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/opensaola/opensaola/api/v1"
)

func reconcileMO(namespace, name string) (reconcile.Result, error) {
	r := &MiddlewareOperatorReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	return r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}})
}

var _ = Describe("MO Envtest", func() {
	It("Envtest_MO_NoOperator_ReconcileAsAvailable", func() {
		name := "mo-no-operator-" + randomSuffix()
		namespace := "default"
		mo := &v1.MiddlewareOperator{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Annotations: map[string]string{v1.LabelNoOperator: "true"},
			},
		}
		Expect(k8sClient.Create(ctx, mo)).To(Succeed())

		first, err := reconcileMO(namespace, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Requeue).To(BeTrue())

		second, err := reconcileMO(namespace, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Requeue).To(BeFalse())
		Expect(second.RequeueAfter).To(BeZero())

		got := &v1.MiddlewareOperator{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, got)).To(Succeed())
		Expect(got.Status.State).To(Equal(v1.StateAvailable))
		Expect(got.Status.Reason).To(BeEmpty())
		Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))

		cond := findCondition(got.Status.Conditions, v1.CondTypeChecked)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	})
})
