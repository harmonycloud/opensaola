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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/harmonycloud/opensaola/api/v1"
)

func reconcileMiddleware(name, ns string) (reconcile.Result, error) {
	r := &MiddlewareReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	return r.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	})
}

var _ = Describe("Finalizer Envtest", func() {

	It("Envtest_Finalizer_Middleware_Added", func() {
		name := "mid-fin-add-" + randomSuffix()
		mid := &v1.Middleware{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       v1.MiddlewareSpec{Baseline: "test"},
		}
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())

		// Reconcile — finalizer should be added before service layer runs
		_, err := reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())

		got := &v1.Middleware{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(got, v1.FinalizerMiddleware)).To(BeTrue())
	})

	It("Envtest_Finalizer_Middleware_Deletion", func() {
		name := "mid-fin-del-" + randomSuffix()
		mid := &v1.Middleware{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       v1.MiddlewareSpec{Baseline: "test"},
		}
		// Pre-set finalizer directly to avoid depending on the first reconcile writing back status/Checked condition which triggers the full publish flow.
		// This test only verifies that the deletion path is re-entrant and can remove the finalizer; it does not verify the publish path.
		controllerutil.AddFinalizer(mid, v1.FinalizerMiddleware)
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())

		got := &v1.Middleware{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(got, v1.FinalizerMiddleware)).To(BeTrue())

		// Delete the CR — blocked by finalizer
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())

		// Reconcile again — should remove finalizer and allow deletion.
		// Note: Reconcile attempts to read baseline/secret dependencies; in envtest we do not want to set up full dependencies for the finalizer test case,
		// so we only require that the finalizer is removed and the object is eventually deleted.
		_, _ = reconcileMiddleware(name, "default")

		Eventually(func() (bool, error) {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)
			if errors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return !controllerutil.ContainsFinalizer(got, v1.FinalizerMiddleware), nil
		}).Should(BeTrue())
	})
})
