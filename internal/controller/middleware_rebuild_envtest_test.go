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
	"encoding/json"
	"fmt"
	"sync"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/k8s/kubeclient"
	"github.com/harmonycloud/opensaola/internal/service/middleware"
	"github.com/harmonycloud/opensaola/internal/service/synchronizer"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("Missing primary resource event recovery", func() {
	It("routes real informer deletion through the owner queue and retries only safe primary recovery", func() {
		name := "rebuild-queue-" + randomSuffix()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ns) })
		// Template rendering in earlier ordinary tests also initializes the
		// shared config singleton. Verify the informer client observes this exact
		// temporary API, rather than waiting for an Add that can never arrive.
		dynamicClient, err := kubeclient.GetDynClient()
		Expect(err).NotTo(HaveOccurred())
		dynamicNamespace, err := dynamicClient.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(dynamicNamespace.GetUID()).To(Equal(ns.UID))
		labels := map[string]string{v1.LabelPackageName: name}
		baseline := &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}}}
		Expect(k8sClient.Create(ctx, baseline)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, baseline) })
		spec := map[string]any{"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": name}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": name}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10"}}}}}
		raw, err := json.Marshal(spec)
		Expect(err).NotTo(HaveOccurred())
		mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: name, Labels: labels}, Spec: v1.MiddlewareSpec{Baseline: name, Parameters: runtime.RawExtension{Raw: raw}}}

		// Production watcher Add delivery establishes that its initial LIST has
		// observed the primary. Deleting afterwards exercises its real watch,
		// not a manually injected controller request or the five-minute fallback.
		listed := make(chan struct{})
		var once sync.Once
		debouncer := synchronizer.RegisterDebouncer(name, name, func() { once.Do(func() { close(listed) }) })
		DeferCleanup(func() { synchronizer.UnregisterDebouncerIfCurrent(name, name, debouncer) })
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: k8sClient.Scheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
			Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{name: {}}},
		})
		Expect(err).NotTo(HaveOccurred())
		r := &MiddlewareReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorderFor("middleware-rebuild-test")}
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(runCtx) }()
		DeferCleanup(func() {
			stop()
			Eventually(done).WithTimeout(10 * time.Second).Should(Receive(BeNil()))
			stopSuiteWatchers()
		})
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())
		key := client.ObjectKeyFromObject(mid)
		primary := &appsv1.Deployment{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
			g.Expect(mid.Status.ObservedGeneration).To(Equal(mid.Generation))
			g.Expect(mid.Status.State).To(Equal(v1.StateAvailable))
			g.Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
			g.Expect(primary.Annotations[k8s.AnnotationResourceIntent]).NotTo(BeEmpty())
		}).WithTimeout(20 * time.Second).Should(Succeed())
		Eventually(listed).WithTimeout(10 * time.Second).Should(BeClosed())
		generation := mid.Generation
		oldUID := primary.UID
		Expect(k8sClient.Delete(ctx, primary)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
			g.Expect(primary.UID).NotTo(Equal(oldUID))
			g.Expect(primary.Annotations[k8s.AnnotationResourceIntent]).NotTo(BeEmpty())
			g.Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
			g.Expect(mid.Generation).To(Equal(generation))
			g.Expect(mid.Status.State).To(Equal(v1.StateAvailable))
		}).WithTimeout(15 * time.Second).Should(Succeed())

		// Replacing the baseline with an unsafe PreAction does not enqueue this
		// owner. The next delete must fail safely through the dedicated path.
		action := &v1.MiddlewareActionBaseline{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.MiddlewareActionBaselineSpec{BaselineType: v1.WorkflowPreAction, ActionType: "audit", Steps: []v1.Step{{Name: "external", CMD: v1.CMD{Command: []string{"printf", "must-not-run"}}}}}}
		Expect(k8sClient.Create(ctx, action)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, action) })
		baseline.Spec.PreActions = []v1.PreAction{{Name: name}}
		baseline.Spec.Configurations = []v1.Configuration{{Name: "must-not-be-republished"}}
		Expect(k8sClient.Update(ctx, baseline)).To(Succeed())
		Eventually(func(g Gomega) {
			cached := &v1.MiddlewareBaseline{}
			g.Expect(mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(baseline), cached)).To(Succeed())
			g.Expect(cached.Spec.PreActions).To(HaveLen(1))
		}).WithTimeout(5 * time.Second).Should(Succeed())
		Expect(k8sClient.Delete(ctx, primary)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
			condition := meta.FindStatusCondition(mid.Status.Conditions, v1.CondTypeApplyCluster)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(condition.Reason).To(Equal(middleware.PrimaryResourceCheckFailedReason))
			g.Expect(condition.Message).To(ContainSubstring("non-CUE"))
			g.Expect(condition.ObservedGeneration).To(Equal(generation))
			g.Expect(mid.Status.State).To(Equal(v1.StateUnavailable))
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, primary))).To(BeTrue())
		}).WithTimeout(15 * time.Second).Should(Succeed())
		// Replacing the unsafe action with a pure CUE patch lets the queued
		// failure retry recover at the same MID generation. Its rendered field
		// is required, while the unrelated Configuration must not be replayed.
		action.Spec.Steps = []v1.Step{{Name: "pure", CUE: fmt.Sprintf(`
output: { spec: parameters.spec }
parameters: {
  spec: { parameters: { template: { metadata: { annotations: { "rebuild-test": "pure-cue" } } } } }
  resource: { apiversion: "middleware.cn/v1", kind: "Middleware", name: %q, namespace: %q }
}`, name, name)}}
		Expect(k8sClient.Update(ctx, action)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
			g.Expect(primary.Spec.Template.Annotations["rebuild-test"]).To(Equal("pure-cue"))
			g.Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
			g.Expect(mid.Generation).To(Equal(generation))
			g.Expect(mid.Status.State).To(Equal(v1.StateAvailable))
			g.Expect(meta.FindStatusCondition(mid.Status.Conditions, v1.CondTypeApplyCluster).Status).To(Equal(metav1.ConditionTrue))
		}).WithTimeout(20 * time.Second).Should(Succeed())
	})
})
