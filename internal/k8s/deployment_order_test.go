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

package k8s

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDeploymentStartupDrift(t *testing.T) {
	desired := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "ns"}}
	pod := &desired.Spec.Template.Spec
	pod.Containers = []corev1.Container{{Name: "operator", Command: []string{"sh", "-c"}, Args: []string{"source", "destination"}}}
	pod.InitContainers = []corev1.Container{{Name: "prepare", Args: []string{"a", "b"}}, {Name: "start"}}

	for _, tc := range []struct {
		name   string
		mutate func(*corev1.PodSpec)
		drift  bool
	}{
		{"defaults and injected init", func(p *corev1.PodSpec) { p.RestartPolicy = corev1.RestartPolicyAlways }, false},
		{"command order", func(p *corev1.PodSpec) { slices.Reverse(p.Containers[0].Command) }, true},
		{"argument order", func(p *corev1.PodSpec) { slices.Reverse(p.Containers[0].Args) }, true},
		{"extra argument", func(p *corev1.PodSpec) { p.Containers[0].Args = append(p.Containers[0].Args, "z") }, true},
		{"init arguments", func(p *corev1.PodSpec) { slices.Reverse(p.InitContainers[0].Args) }, true},
		{"init order", func(p *corev1.PodSpec) { slices.Reverse(p.InitContainers) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := desired.DeepCopy()
			p := &live.Spec.Template.Spec
			injected := corev1.Container{Name: "injected", Image: "helper"}
			p.InitContainers = slices.Insert(p.InitContainers, 1, injected)
			tc.mutate(p)
			same, err := CompareDeploymentSpec(t.Context(), live, desired)
			if err != nil || same == tc.drift {
				t.Fatalf("same=%v err=%v, want drift=%v", same, err, tc.drift)
			}
			if !tc.drift {
				return
			}
			cli := fake.NewClientBuilder().WithObjects(live).Build()
			key := client.ObjectKeyFromObject(live)
			if err := cli.Get(t.Context(), key, live); err != nil {
				t.Fatal(err)
			}
			if err := RestoreDeploymentSpec(t.Context(), cli, live, desired); err != nil {
				t.Fatal(err)
			}
			if err := cli.Get(t.Context(), key, live); err != nil {
				t.Fatal(err)
			}
			got := live.Spec.Template.Spec.InitContainers
			if len(got) != 3 || got[1].Name != injected.Name || got[1].Image != injected.Image {
				t.Fatalf("lost injected step: %v", got)
			}
			if same, err := CompareDeploymentSpec(t.Context(), live, desired); err != nil || !same {
				t.Fatalf("repair did not converge: same=%v err=%v", same, err)
			}
		})
	}
}
