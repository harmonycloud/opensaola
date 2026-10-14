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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Suspend reconcile", func() {
	BeforeEach(func() {
		resolver, err := k8s.NewIntentSchemaResolver(cfg)
		Expect(err).NotTo(HaveOccurred())
		k8s.ConfigureResourceIntents(resolver)
		DeferCleanup(func() { k8s.ConfigureResourceIntents(nil) })
	})
	for _, failure := range []string{"missing Secret", "corrupt hash"} {
		It("keeps merge protected without recapturing a "+failure, func() {
			name := "pause-secret-failure-" + randomSuffix()
			labels := map[string]string{v1.LabelPackageName: name}
			baseline := &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}}}
			Expect(k8sClient.Create(ctx, baseline)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, baseline) })
			spec := map[string]any{
				"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": name}},
				"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": name}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10"}}}},
			}
			raw, err := json.Marshal(spec)
			Expect(err).NotTo(HaveOccurred())
			mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels}, Spec: v1.MiddlewareSpec{Baseline: name, Parameters: runtime.RawExtension{Raw: raw}}}
			Expect(k8sClient.Create(ctx, mid)).To(Succeed())
			_, err = reconcileMiddleware(name, "default") // finalizer
			Expect(err).NotTo(HaveOccurred())
			_, err = reconcileMiddleware(name, "default") // initial publish
			Expect(err).NotTo(HaveOccurred())
			key := client.ObjectKeyFromObject(mid)
			Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
			mid.Annotations = map[string]string{v1.AnnotationSuspendReconcile: "true"}
			Expect(k8sClient.Update(ctx, mid)).To(Succeed())
			_, err = reconcileMiddleware(name, "default")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
			Expect(mid.Status.ReconcilePause).NotTo(BeNil())
			secretName := mid.Status.ReconcilePause.SnapshotSecret
			if failure == "missing Secret" {
				secret := &corev1.Secret{}
				Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: mid.Namespace, Name: secretName}, secret)).To(Succeed())
				Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			} else {
				mid.Status.ReconcilePause.Hash = "corrupt"
				Expect(k8sClient.Status().Update(ctx, mid)).To(Succeed())
			}
			primary := &unstructured.Unstructured{}
			primary.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
			Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, primary) })
			Expect(unstructured.SetNestedField(primary.Object, int64(2), "spec", "replicas")).To(Succeed())
			Expect(k8sClient.Update(ctx, primary)).To(Succeed())
			primaryRV := primary.GetResourceVersion()
			mid.Annotations[v1.AnnotationResumePolicy] = "merge"
			Expect(k8sClient.Update(ctx, mid)).To(Succeed())
			_, err = reconcileMiddleware(name, "default")
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
			Expect(mid.Annotations).To(HaveKey(v1.AnnotationSuspendReconcile))
			Expect(mid.Status.ReconcilePause.SnapshotSecret).To(Equal(secretName))
			Expect(mid.Spec.ReconcileOverrides).To(BeNil())
			Expect(findCondition(mid.Status.Conditions, v1.CondTypeReconcileAdoption).Status).To(Equal(metav1.ConditionFalse))
			Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
			Expect(primary.GetResourceVersion()).To(Equal(primaryRV))
		})
	}
	for _, legacy := range []bool{false, true} {
		for _, scenario := range []string{"unchanged actual", "replicas", "env changes", "second change before write"} {
			It(fmt.Sprintf("resumes full controller writes using only observed changes (legacy=%t): %s", legacy, scenario), func() {
				name := "pause-delta-" + randomSuffix()
				labels := map[string]string{v1.LabelPackageName: name}
				baseline := &v1.MiddlewareBaseline{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.MiddlewareBaselineSpec{GVK: v1.GVK{Group: "apps", Version: "v1", Kind: "Deployment"}}}
				Expect(k8sClient.Create(ctx, baseline)).To(Succeed())
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, baseline) })
				spec := map[string]any{
					"replicas": int64(0), "selector": map[string]any{"matchLabels": map[string]any{"app": name}},
					"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": name}}, "spec": map[string]any{"containers": []any{
						map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10", "ports": []any{map[string]any{"containerPort": int64(8080)}}, "env": []any{map[string]any{"name": "KEEP", "value": "old"}, map[string]any{"name": "REMOVE", "value": "old"}}},
					}}},
				}
				raw, err := json.Marshal(spec)
				Expect(err).NotTo(HaveOccurred())
				mid := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Labels: labels, Finalizers: []string{v1.FinalizerMiddleware}}, Spec: v1.MiddlewareSpec{Baseline: name, Parameters: runtime.RawExtension{Raw: raw}}}
				Expect(k8sClient.Create(ctx, mid)).To(Succeed())
				primary := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": "default"}, "spec": runtime.DeepCopyJSONValue(spec)}}
				if legacy {
					Expect(ctrl.SetControllerReference(mid, primary, k8sClient.Scheme())).To(Succeed())
					Expect(k8sClient.Create(ctx, primary, client.FieldOwner("manager"))).To(Succeed())
					mid.Status.ObservedGeneration = mid.Generation
					mid.Status.State = v1.StateAvailable
					Expect(k8sClient.Status().Update(ctx, mid)).To(Succeed())
				} else {
					_, err = reconcileMiddleware(name, "default")
					Expect(err).NotTo(HaveOccurred())
				}
				key := types.NamespacedName{Name: name, Namespace: "default"}
				Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, primary) })
				if legacy {
					Expect(primary.GetAnnotations()).NotTo(HaveKey(k8s.AnnotationResourceIntent))
				}
				// Default fields must really exist before the pause, not just be
				// introduced by the test fixture.
				containers, _, err := unstructured.NestedSlice(primary.Object, "spec", "template", "spec", "containers")
				Expect(err).NotTo(HaveOccurred())
				container, ok := containers[0].(map[string]any)
				Expect(ok).To(BeTrue())
				Expect(container["terminationMessagePath"]).To(Equal("/dev/termination-log"))
				beforeRaw, err := json.Marshal(primary.Object["spec"])
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
				mid.Annotations = map[string]string{v1.AnnotationSuspendReconcile: "true"}
				Expect(k8sClient.Update(ctx, mid)).To(Succeed())
				_, err = reconcileMiddleware(name, "default")
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
				data, err := k8s.NewManagedResourceWriter(k8sClient).LoadReconcilePauseSnapshot(ctx, mid, mid.Status.ReconcilePause)
				Expect(err).NotTo(HaveOccurred())
				snapshotRaw, err := json.Marshal(data.ActualSpec)
				Expect(err).NotTo(HaveOccurred())
				Expect(snapshotRaw).To(MatchJSON(beforeRaw))
				pauseSecretKey := client.ObjectKey{Namespace: mid.Namespace, Name: mid.Status.ReconcilePause.SnapshotSecret}
				statusRaw, err := json.Marshal(mid.Status.ReconcilePause)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(statusRaw)).NotTo(ContainSubstring("actualSpec"))
				Expect(string(statusRaw)).NotTo(ContainSubstring("previousOverrides"))
				pauseHash := mid.Status.ReconcilePause.Hash
				wantPatch := "{}"
				switch scenario {
				case "replicas", "second change before write":
					Expect(unstructured.SetNestedField(primary.Object, int64(2), "spec", "replicas")).To(Succeed())
					wantPatch = `{"replicas":2}`
				case "env changes":
					container["env"] = []any{map[string]any{"name": "KEEP", "value": "new"}, map[string]any{"name": "NEW", "value": "added"}}
					Expect(unstructured.SetNestedSlice(primary.Object, containers, "spec", "template", "spec", "containers")).To(Succeed())
					wantPatch = `{"template":{"spec":{"containers":[{"name":"main","image":"registry.k8s.io/pause:3.10","ports":[{"containerPort":8080}],"env":[{"name":"KEEP","value":"new"},{"name":"NEW","value":"added"}]}]}}}`
				}
				if scenario != "unchanged actual" {
					Expect(k8sClient.Update(ctx, primary)).To(Succeed())
				}
				pausedRaw, err := json.Marshal(primary.Object["spec"])
				Expect(err).NotTo(HaveOccurred())
				_, err = reconcileMiddleware(name, "default")
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
				stillPausedRaw, err := json.Marshal(primary.Object["spec"])
				Expect(err).NotTo(HaveOccurred())
				Expect(stillPausedRaw).To(MatchJSON(pausedRaw))
				Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
				Expect(mid.Status.ReconcilePause.Hash).To(Equal(pauseHash))
				mid.Annotations[v1.AnnotationResumePolicy] = "merge"
				Expect(k8sClient.Update(ctx, mid)).To(Succeed())
				result, err := reconcileMiddleware(name, "default")
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Requeue).To(BeTrue())
				Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
				if scenario == "unchanged actual" {
					Expect(mid.Spec.ReconcileOverrides).To(BeNil())
				} else {
					Expect(mid.Spec.ReconcileOverrides.SpecPatch.Raw).To(MatchJSON(wantPatch))
					Expect(string(mid.Spec.ReconcileOverrides.SpecPatch.Raw)).NotTo(ContainSubstring("terminationMessagePath"))
					Expect(string(mid.Spec.ReconcileOverrides.SpecPatch.Raw)).NotTo(ContainSubstring("terminationMessagePolicy"))
					Expect(string(mid.Spec.ReconcileOverrides.SpecPatch.Raw)).NotTo(ContainSubstring("protocol"))
				}
				if scenario == "second change before write" {
					Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
					Expect(unstructured.SetNestedField(primary.Object, int64(3), "spec", "replicas")).To(Succeed())
					Expect(k8sClient.Update(ctx, primary)).To(Succeed())
					result, err = reconcileMiddleware(name, "default")
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Requeue).To(BeTrue())
					Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
					Expect(mid.Spec.ReconcileOverrides.SpecPatch.Raw).To(MatchJSON(`{"replicas":3}`))
				}
				_, err = reconcileMiddleware(name, "default")
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("resume %s", scenario))
				Expect(k8sClient.Get(ctx, key, mid)).To(Succeed())
				Expect(mid.Status.ReconcilePause).To(BeNil())
				Expect(apierrors.IsNotFound(k8sClient.Get(ctx, pauseSecretKey, &corev1.Secret{}))).To(BeTrue())
				Expect(mid.Annotations).NotTo(HaveKey(v1.AnnotationResumePolicy))
				Expect(mid.Status.ObservedGeneration).To(Equal(mid.Generation))
				Expect(k8sClient.Get(ctx, key, primary)).To(Succeed())
				afterResume, err := json.Marshal(primary.Object["spec"])
				Expect(err).NotTo(HaveOccurred())
				if scenario != "second change before write" {
					Expect(afterResume).To(MatchJSON(pausedRaw))
				} else {
					value, _, err := unstructured.NestedInt64(primary.Object, "spec", "replicas")
					Expect(err).NotTo(HaveOccurred())
					Expect(value).To(Equal(int64(3)))
				}
				// Future reconciles do not revert adopted external changes.
				rv := primary.GetResourceVersion()
				_, err = reconcileMiddleware(name, "default")
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primary), primary)).To(Succeed())
				Expect(primary.GetResourceVersion()).To(Equal(rv))
			})
		}
	}
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
		Expect(ctrl.SetControllerReference(mid, primary, k8sClient.Scheme())).To(Succeed())
		Expect(k8sClient.Update(ctx, primary)).To(Succeed())

		first, err := reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Requeue).To(BeTrue())
		_, err = reconcileMiddleware(name, "default")
		Expect(err).NotTo(HaveOccurred())

		got := &v1.Middleware{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		Expect(got.Status.ReconcilePause).NotTo(BeNil())
		data, err := k8s.NewManagedResourceWriter(k8sClient).LoadReconcilePauseSnapshot(ctx, got, got.Status.ReconcilePause)
		Expect(err).NotTo(HaveOccurred())
		snapshotRaw, err := json.Marshal(data.ActualSpec)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(snapshotRaw)).To(ContainSubstring(`"replicas":1`))
		Expect(string(snapshotRaw)).To(ContainSubstring(`"terminationMessagePath":"/dev/termination-log"`))
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
		Expect(got.Spec.ReconcileOverrides.SpecPatch.Raw).To(MatchJSON(`{"replicas":5}`))
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
