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
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/harmonycloud/opensaola/api/v1"
)

var _ = Describe("Envtest Manager", func() {
	It("Envtest_Manager_Starts_And_Reconciles_MOB", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())

		reconciler := &MiddlewareOperatorBaselineReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		ctxRun, cancelRun := context.WithCancel(ctx)
		defer cancelRun()

		done := make(chan struct{})
		go func() {
			_ = mgr.Start(ctxRun)
			close(done)
		}()

		name := "mob-mgr-" + randomSuffix()
		mob := newMOB(name, []v1.GVK{{Name: "test", Group: "apps", Version: "v1", Kind: "Deployment"}})
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		Eventually(func(g Gomega) {
			got := &v1.MiddlewareOperatorBaseline{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got.Status.ObservedGeneration).To(Equal(got.Generation))
			g.Expect(got.Status.State).To(Equal(v1.StateAvailable))
		}).WithTimeout(10 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())

		cancelRun()
		Eventually(done).WithTimeout(5 * time.Second).Should(BeClosed())
	})
})
