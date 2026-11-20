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
	"context"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("MO primary runtime", func() {
	var owner *v1.MiddlewareOperator
	var r *MiddlewareOperatorRuntimeReconciler

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "runtime-primary-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ns) })
		owner = createRuntimeOwner(ns.Name)
		r = &MiddlewareOperatorRuntimeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	reconcile := func(dep *appsv1.Deployment) {
		GinkgoHelper()
		for _, req := range runtimeDeploymentRequests(ctx, dep) {
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
	}
	readStatus := func(g Gomega) v1.MiddlewareOperatorStatus {
		GinkgoHelper()
		live := &v1.MiddlewareOperator{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(owner), live)).To(Succeed())
		g.Expect(live.Status.State).To(Equal(v1.StateUnavailable))
		g.Expect(live.Status.ObservedGeneration).To(Equal(owner.Generation))
		g.Expect(live.Status.Reason).To(Equal("main-controller-reason"))
		return live.Status
	}

	DescribeTable("reports the same primary status in either event order",
		func(primaryFirst bool) {
			primary := createRuntimeDeployment(owner, owner.Name, nil)
			aux := createRuntimeDeployment(owner, "aux", nil)
			setRuntimeReplicas(primary, 2, 2)
			setRuntimeReplicas(aux, 3, 0)
			applyBoth := func() {
				if primaryFirst {
					reconcile(primary)
					reconcile(aux)
				} else {
					reconcile(aux)
					reconcile(primary)
				}
			}
			applyBoth()
			status := readStatus(Default)
			Expect(status.OperatorStatus).To(HaveLen(2))
			Expect(status.OperatorStatus).To(HaveKeyWithValue(primary.Name, primary.Status))
			Expect(status.OperatorStatus).To(HaveKeyWithValue(aux.Name, aux.Status))
			Expect(status.OperatorAvailable).To(Equal("2/2"))
			Expect(status.Ready).To(BeTrue())
			Expect(status.Runtime).To(Equal("Ready"))

			setRuntimeReplicas(primary, 2, 0)
			setRuntimeReplicas(aux, 3, 3)
			applyBoth()
			status = readStatus(Default)
			Expect(status.OperatorStatus).To(HaveKeyWithValue(primary.Name, primary.Status))
			Expect(status.OperatorStatus).To(HaveKeyWithValue(aux.Name, aux.Status))
			Expect(status.OperatorAvailable).To(Equal("0/2"))
			Expect(status.Ready).To(BeFalse())
			Expect(status.Runtime).NotTo(BeEmpty())
			Expect(status.Runtime).NotTo(Equal("Ready"))
		},
		Entry("primary then auxiliary", true),
		Entry("auxiliary then primary", false),
	)

	DescribeTable("keeps an auxiliary-only deployment out of the summary",
		func(sameName, noOperator bool) {
			if noOperator {
				owner.Annotations = map[string]string{v1.LabelNoOperator: "true"}
				Expect(k8sClient.Update(ctx, owner)).To(Succeed())
			}
			name := "aux"
			var annotations map[string]string
			if sameName {
				name = owner.Name
				annotations = map[string]string{v1.LabelConfigurations: "configured"}
			}
			aux := createRuntimeDeployment(owner, name, annotations)
			setRuntimeReplicas(aux, 4, 4)
			reconcile(aux)
			status := readStatus(Default)
			Expect(status.OperatorStatus).To(HaveKeyWithValue(name, aux.Status))
			Expect(status.Ready).To(BeFalse())
			Expect(status.Runtime).To(BeEmpty())
			Expect(status.OperatorAvailable).To(BeEmpty())
		},
		Entry("different name", false, false),
		Entry("same name with a Configuration source", true, false),
		Entry("no-operator with a different name", false, true),
		Entry("no-operator with a same-name Configuration", true, true),
	)

	DescribeTable("repairs a same-name auxiliary detail after a controller restart",
		func(deletedWhileStopped bool) {
			aux := createRuntimeDeployment(owner, owner.Name, map[string]string{v1.LabelConfigurations: "configured"})
			setRuntimeReplicas(aux, 2, 2)
			reconcile(aux)
			if deletedWhileStopped {
				Expect(k8sClient.Delete(ctx, aux)).To(Succeed())
			} else {
				// The stopped controller missed a real status transition.
				setRuntimeReplicas(aux, 2, 0)
			}
			// Replay the owner from the initial informer list; a missed delete
			// cannot be recovered by listing live Deployments alone.
			live := &v1.MiddlewareOperator{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(owner), live)).To(Succeed())
			for _, req := range recordedRuntimeRequests(ctx, live) {
				_, err := r.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
			}
			status := readStatus(Default)
			if deletedWhileStopped {
				Expect(status.OperatorStatus).To(BeEmpty())
			} else {
				Expect(status.OperatorStatus).To(HaveKeyWithValue(aux.Name, aux.Status))
			}
			Expect(status.Ready).To(BeFalse())
			Expect(status.OperatorAvailable).To(BeEmpty())
			Expect(status.Runtime).To(BeEmpty())
		},
		Entry("updated while stopped", false),
		Entry("deleted while stopped", true),
	)

	It("ignores a same-name deployment outside the owner's namespace and non-controller references", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "runtime-foreign-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ns) })
		foreign := createRuntimeOwner(ns.Name)
		cross := createRuntimeDeployment(foreign, foreign.Name, nil)
		cross.OwnerReferences[0].UID = owner.UID
		Expect(k8sClient.Update(ctx, cross)).To(Succeed())
		setRuntimeReplicas(cross, 1, 1)
		reconcile(cross)
		Expect(readStatus(Default).OperatorStatus).To(BeEmpty())
		foreignLive := &v1.MiddlewareOperator{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foreign), foreignLive)).To(Succeed())
		Expect(foreignLive.Status).To(Equal(foreign.Status))

		dep := createRuntimeDeployment(owner, owner.Name, nil)
		dep.OwnerReferences[0].Controller = ptr.To(false)
		Expect(k8sClient.Update(ctx, dep)).To(Succeed())
		setRuntimeReplicas(dep, 1, 1)
		reconcile(dep)
		status := readStatus(Default)
		Expect(status.Ready).To(BeFalse())
		Expect(status.Runtime).To(BeEmpty())
		Expect(status.OperatorAvailable).To(BeEmpty())
	})

	It("ignores an old owner's delayed event after same-name owner recreation", func() {
		dep := createRuntimeDeployment(owner, owner.Name, nil)
		setRuntimeReplicas(dep, 1, 1)
		queued := runtimeDeploymentRequests(ctx, dep)
		Expect(queued).To(HaveLen(1))
		reconcile(dep)
		Expect(readStatus(Default).Ready).To(BeTrue())

		oldUID := owner.UID
		Expect(k8sClient.Delete(ctx, owner)).To(Succeed())
		owner = createRuntimeOwner(owner.Namespace)
		Expect(owner.UID).NotTo(Equal(oldUID))
		before := readStatus(Default)
		_, err := r.Reconcile(ctx, queued[0])
		Expect(err).NotTo(HaveOccurred())
		reconcile(dep)
		Expect(readStatus(Default)).To(Equal(before))
		Expect(k8sClient.Delete(ctx, dep)).To(Succeed())
		_, err = r.Reconcile(ctx, queued[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(readStatus(Default)).To(Equal(before))
	})

	It("reads the current primary when a delayed delete follows its replacement", func() {
		primary := createRuntimeDeployment(owner, owner.Name, nil)
		setRuntimeReplicas(primary, 1, 1)
		queued := runtimeDeploymentRequests(ctx, primary)
		Expect(queued).To(HaveLen(1))
		reconcile(primary)
		Expect(k8sClient.Delete(ctx, primary)).To(Succeed())
		replacement := createRuntimeDeployment(owner, owner.Name, nil)
		Expect(replacement.UID).NotTo(Equal(primary.UID))
		setRuntimeReplicas(replacement, 3, 3)
		_, err := r.Reconcile(ctx, queued[0])
		Expect(err).NotTo(HaveOccurred())
		status := readStatus(Default)
		Expect(status.Ready).To(BeTrue())
		Expect(status.OperatorAvailable).To(Equal("3/3"))
		Expect(status.OperatorStatus).To(HaveKeyWithValue(replacement.Name, replacement.Status))
	})

	It("updates, deletes and restores runtime status through the real informer queue", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: k8sClient.Scheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
			Cache:      cache.Options{DefaultNamespaces: map[string]cache.Config{owner.Namespace: {}}},
			Controller: controllerconfig.Controller{SkipNameValidation: ptr.To(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		watched := &MiddlewareOperatorRuntimeReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}
		Expect(watched.SetupWithManager(mgr)).To(Succeed())
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(runCtx) }()
		DeferCleanup(func() {
			stop()
			Eventually(done).WithTimeout(10 * time.Second).Should(Receive(BeNil()))
		})

		primary := createRuntimeDeployment(owner, owner.Name, nil)
		aux := createRuntimeDeployment(owner, "aux", nil)
		setRuntimeReplicas(primary, 2, 2)
		setRuntimeReplicas(aux, 3, 0)
		Eventually(func(g Gomega) {
			status := readStatus(g)
			g.Expect(status.OperatorStatus).To(HaveKeyWithValue(primary.Name, primary.Status))
			g.Expect(status.OperatorStatus).To(HaveKeyWithValue(aux.Name, aux.Status))
			g.Expect(status.OperatorAvailable).To(Equal("2/2"))
			g.Expect(status.Ready).To(BeTrue())
			g.Expect(status.Runtime).To(Equal("Ready"))
		}).WithTimeout(15 * time.Second).Should(Succeed())

		Expect(k8sClient.Delete(ctx, aux)).To(Succeed())
		Eventually(func(g Gomega) {
			status := readStatus(g)
			g.Expect(status.OperatorStatus).To(HaveLen(1))
			g.Expect(status.OperatorStatus).To(HaveKeyWithValue(primary.Name, primary.Status))
			g.Expect(status.Ready).To(BeTrue())
			g.Expect(status.OperatorAvailable).To(Equal("2/2"))
			g.Expect(status.Runtime).To(Equal("Ready"))
		}).WithTimeout(15 * time.Second).Should(Succeed())

		// The MO annotation itself is the sole event for this transition;
		// the underlying Deployment's resourceVersion stays unchanged.
		toggleNoOperator := func(enabled bool) {
			GinkgoHelper()
			current := &v1.MiddlewareOperator{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(owner), current)).To(Succeed())
			before := current.DeepCopy()
			if enabled {
				current.Annotations = map[string]string{v1.LabelNoOperator: "true"}
			} else {
				delete(current.Annotations, v1.LabelNoOperator)
			}
			Expect(k8sClient.Patch(ctx, current, client.MergeFrom(before))).To(Succeed())
		}
		toggleNoOperator(true)
		Eventually(func(g Gomega) {
			status := readStatus(g)
			g.Expect(status.OperatorStatus).To(HaveKeyWithValue(primary.Name, primary.Status))
			g.Expect(status.Ready).To(BeFalse())
			g.Expect(status.OperatorAvailable).To(BeEmpty())
			g.Expect(status.Runtime).To(BeEmpty())
		}).WithTimeout(15 * time.Second).Should(Succeed())
		toggleNoOperator(false)
		Eventually(func(g Gomega) {
			status := readStatus(g)
			g.Expect(status.Ready).To(BeTrue())
			g.Expect(status.Runtime).To(Equal("Ready"))
			g.Expect(status.OperatorAvailable).To(Equal("2/2"))
		}).WithTimeout(15 * time.Second).Should(Succeed())

		Expect(k8sClient.Delete(ctx, primary)).To(Succeed())
		Eventually(func(g Gomega) {
			status := readStatus(g)
			g.Expect(status.OperatorStatus).To(BeEmpty())
			g.Expect(status.Ready).To(BeFalse())
			g.Expect(status.OperatorAvailable).To(BeEmpty())
			g.Expect(status.Runtime).To(Equal("DeploymentMissing"))
		}).WithTimeout(15 * time.Second).Should(Succeed())
		replacement := createRuntimeDeployment(owner, owner.Name, nil)
		Expect(replacement.UID).NotTo(Equal(primary.UID))
		setRuntimeReplicas(replacement, 3, 3)
		Eventually(func(g Gomega) {
			status := readStatus(g)
			g.Expect(status.OperatorStatus).To(HaveKeyWithValue(replacement.Name, replacement.Status))
			g.Expect(status.OperatorAvailable).To(Equal("3/3"))
			g.Expect(status.Ready).To(BeTrue())
			g.Expect(status.Runtime).To(Equal("Ready"))
		}).WithTimeout(15 * time.Second).Should(Succeed())
	})
})

func createRuntimeOwner(namespace string) *v1.MiddlewareOperator {
	GinkgoHelper()
	mo := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: namespace}, Spec: v1.MiddlewareOperatorSpec{Baseline: "test"}}
	Expect(k8sClient.Create(ctx, mo)).To(Succeed())
	mo.Status = v1.MiddlewareOperatorStatus{State: v1.StateUnavailable, ObservedGeneration: mo.Generation, Reason: "main-controller-reason"}
	Expect(k8sClient.Status().Update(ctx, mo)).To(Succeed())
	return mo
}

func createRuntimeDeployment(owner *v1.MiddlewareOperator, name string, annotations map[string]string) *appsv1.Deployment {
	GinkgoHelper()
	labels := map[string]string{"app": name}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: owner.Namespace, Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(owner, v1.GroupVersion.WithKind("MiddlewareOperator"))},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "registry.k8s.io/pause:3.10"}}},
			},
		},
	}
	Expect(k8sClient.Create(ctx, dep)).To(Succeed())
	return dep
}

// Envtest runs the API server without a Deployment controller. Publish the
// status Kubernetes would have observed through its real status subresource.
func setRuntimeReplicas(dep *appsv1.Deployment, total, available int32) {
	GinkgoHelper()
	dep.Status = appsv1.DeploymentStatus{
		ObservedGeneration: dep.Generation, Replicas: total, UpdatedReplicas: total,
		ReadyReplicas: available, AvailableReplicas: available, UnavailableReplicas: total - available,
	}
	Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())
}
