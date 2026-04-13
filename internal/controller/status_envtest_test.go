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

	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/k8s"
)

var _ = Describe("Status Update Envtest", func() {

	It("Envtest_StatusUpdate_MOB_BasicFlow", func() {
		name := "mob-status-basic-" + randomSuffix()
		mob := newMOB(name, []v1.GVK{{Name: "s", Group: "apps", Version: "v1", Kind: "Deployment"}})
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		// Fetch to get server-assigned generation
		got := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())

		got.Status.State = v1.StateAvailable
		got.Status.Conditions = []metav1.Condition{{
			Type:               v1.CondTypeChecked,
			Status:             metav1.ConditionTrue,
			Reason:             v1.CondReasonCheckedSuccess,
			Message:            "success",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: got.Generation,
		}}

		Expect(k8s.UpdateMiddlewareOperatorBaselineStatus(ctx, k8sClient, got)).To(Succeed())

		fresh := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, fresh)).To(Succeed())
		Expect(fresh.Status.State).To(Equal(v1.StateAvailable))
		Expect(fresh.Status.ObservedGeneration).To(Equal(fresh.Generation))
		Expect(fresh.Status.Conditions).To(HaveLen(1))
	})

	It("Envtest_StatusUpdate_Middleware_DeepEqualDedup", func() {
		name := "mid-dedup-" + randomSuffix()
		mid := &v1.Middleware{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       v1.MiddlewareSpec{Baseline: "test"},
		}
		Expect(k8sClient.Create(ctx, mid)).To(Succeed())

		// First status write
		got := &v1.Middleware{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		got.Status.State = v1.StateAvailable
		Expect(k8s.UpdateMiddlewareStatus(ctx, k8sClient, got)).To(Succeed())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		rvAfterFirst := got.ResourceVersion

		// Second write with identical status — deepEqual should skip
		Expect(k8s.UpdateMiddlewareStatus(ctx, k8sClient, got)).To(Succeed())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, got)).To(Succeed())
		Expect(got.ResourceVersion).To(Equal(rvAfterFirst))
	})

	It("Envtest_StatusUpdate_MOB_ConflictRetry", func() {
		name := "mob-conflict-" + randomSuffix()
		mob := newMOB(name, []v1.GVK{{Name: "c", Group: "apps", Version: "v1", Kind: "Deployment"}})
		Expect(k8sClient.Create(ctx, mob)).To(Succeed())

		// Fetch a stale copy
		stale := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, stale)).To(Succeed())

		// Bump the resource version by updating spec
		fresh := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, fresh)).To(Succeed())
		fresh.Spec.GVKs = append(fresh.Spec.GVKs, v1.GVK{Name: "extra", Group: "batch", Version: "v1", Kind: "Job"})
		Expect(k8sClient.Update(ctx, fresh)).To(Succeed())

		// Use stale object to update status — retry logic should re-fetch and succeed
		stale.Status.State = v1.StateAvailable
		Expect(k8s.UpdateMiddlewareOperatorBaselineStatus(ctx, k8sClient, stale)).To(Succeed())

		got := &v1.MiddlewareOperatorBaseline{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, got)).To(Succeed())
		Expect(got.Status.State).To(Equal(v1.StateAvailable))
	})
})
