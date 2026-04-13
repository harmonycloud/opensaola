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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetPVCs(ctx context.Context, kubeClient client.Client, namespace string, lables client.MatchingLabels) (*corev1.PersistentVolumeClaimList, error) {
	pvcs := &corev1.PersistentVolumeClaimList{}
	err := kubeClient.List(ctx, pvcs, client.InNamespace(namespace), lables)
	return pvcs, err
}
