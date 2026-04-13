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

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetDaemonSets(ctx context.Context, kubeClient client.Client, namespace string, labels client.MatchingLabels) (*appsv1.DaemonSetList, error) {
	daemonSets := &appsv1.DaemonSetList{}
	err := kubeClient.List(ctx, daemonSets, client.InNamespace(namespace), labels)
	return daemonSets, err
}

func GetDaemonSet(ctx context.Context, kubeClient client.Client, name, namespace string) (*appsv1.DaemonSet, error) {
	daemonSet := &appsv1.DaemonSet{}
	err := kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, daemonSet)
	return daemonSet, err
}
