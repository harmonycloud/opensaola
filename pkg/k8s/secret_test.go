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

package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetSecrets_NamespaceFilter(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	labels := map[string]string{"app": "test"}

	s1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "target-ns", Labels: labels},
	}
	s2 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s2", Namespace: "other-ns", Labels: labels},
	}

	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s1, s2).Build()

	list, err := GetSecrets(context.Background(), cli, "target-ns", client.MatchingLabels(labels))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(list.Items) != 1 {
		t.Fatalf("expected 1 secret in target-ns, got %d", len(list.Items))
	}
	if list.Items[0].Name != "s1" {
		t.Errorf("expected secret s1, got %s", list.Items[0].Name)
	}
}
