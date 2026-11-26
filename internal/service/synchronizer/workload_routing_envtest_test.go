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

package synchronizer

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestWorkloadStatusReadIsolation(t *testing.T) {
	g := gomega.NewWithT(t)
	env := &envtest.Environment{
		UseExistingCluster:    ptr.To(false),
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	g.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { g.Expect(env.Stop()).To(gomega.Succeed()) })
	scheme := runtime.NewScheme()
	g.Expect(clientscheme.AddToScheme(scheme)).To(gomega.Succeed())
	g.Expect(v1.AddToScheme(scheme)).To(gomega.Succeed())
	api, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	const ns = "routing"
	gvk := appsv1.SchemeGroupVersion.WithKind("Deployment")
	reads := map[string]*atomic.Int64{"target": {}, "bystander": {}}
	var lastRead atomic.Int64
	observed := interceptor.NewClient(api, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if cr, ok := obj.(*unstructured.Unstructured); ok && cr.GroupVersionKind() == gvk && key.Namespace == ns {
				if counter := reads[key.Name]; counter != nil {
					counter.Add(1)
					lastRead.Store(time.Now().UnixNano())
				}
			}
			return nil
		},
	})
	ctx := t.Context()
	manager, err := StartNsInformerManager(ctx, cfg)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	manager.UseStatusDebouncers()
	var workers sync.WaitGroup
	t.Cleanup(func() {
		workers.Wait() // t.Context is canceled before cleanup.
		StopNsInformerManagerIfCurrent(manager)
	})

	g.Expect(api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(gomega.Succeed())
	template := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "fixture", Image: "registry.k8s.io/pause:3.10"}}},
	}
	for _, name := range []string{"target", "bystander"} {
		meta := metav1.ObjectMeta{Namespace: ns, Name: name}
		mid := &v1.Middleware{ObjectMeta: meta, Spec: v1.MiddlewareSpec{Baseline: "fixture"}}
		g.Expect(api.Create(ctx, mid)).To(gomega.Succeed())
		dep := &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: *template.DeepCopy(),
		}}
		dep.Spec.Template.Labels = dep.Spec.Selector.MatchLabels
		g.Expect(api.Create(ctx, dep)).To(gomega.Succeed())
		// Exclude the independent first-generation readiness timer from the measurement.
		dep.Spec.MinReadySeconds++
		g.Expect(api.Update(ctx, dep)).To(gomega.Succeed())
		if name == "target" {
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "target-rs", Namespace: ns,
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(dep, gvk)}},
				Spec: appsv1.ReplicaSetSpec{Selector: dep.Spec.Selector, Template: dep.Spec.Template}}
			g.Expect(api.Create(ctx, rs)).To(gomega.Succeed())
		}

		primary := &unstructured.Unstructured{}
		primary.SetGroupVersionKind(gvk)
		primary.SetName(name)
		primary.SetNamespace(ns)
		workers.Go(func() {
			if err := SyncCustomResourceV2(ctx, observed, primary, mid); err != nil {
				t.Errorf("start %s sync: %v", name, err)
			}
		})
	}

	// Both MIDs must have published their Deployment before checking isolation.
	for name := range reads {
		g.Eventually(func() bool {
			mid := &v1.Middleware{}
			if err := api.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, mid); err != nil {
				return false
			}
			included := mid.Status.CustomResources.Include.Deployments
			return len(included) == 1 && included[0].Name == name
		}, 15*time.Second).Should(gomega.BeTrue(), name)
	}

	for _, tc := range []struct {
		name string
		obj  client.Object
	}{
		{"Deployment", &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "target"}}},
		{"ReplicaSet", &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "target-rs"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := gomega.NewWithT(t)
			check.Eventually(func() bool { return time.Since(time.Unix(0, lastRead.Load())) > time.Second },
				10*time.Second, 25*time.Millisecond).Should(gomega.BeTrue())
			target, other := reads["target"].Load(), reads["bystander"].Load()
			// Only the API server is running. Changing replicas emits exactly this workload's event.
			check.Expect(api.Patch(ctx, tc.obj, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"replicas":2}}`)))).To(gomega.Succeed())
			check.Eventually(reads["target"].Load, 10*time.Second).Should(gomega.BeNumerically(">", target))
			check.Consistently(reads["bystander"].Load, time.Second, 25*time.Millisecond).Should(gomega.Equal(other))
		})
	}
}
