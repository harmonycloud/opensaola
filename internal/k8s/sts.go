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

	v1 "github.com/harmonycloud/opensaola/api/v1"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetStatefulSets(ctx context.Context, kubeClient client.Client, namespace string, lables client.MatchingLabels) (*appsv1.StatefulSetList, error) {
	statefulSets := &appsv1.StatefulSetList{}
	err := kubeClient.List(ctx, statefulSets, client.InNamespace(namespace), lables)
	return statefulSets, err
}

func GetStatefulSet(ctx context.Context, kubeClient client.Client, name, namespace string) (*appsv1.StatefulSet, error) {
	statefulSet := &appsv1.StatefulSet{}
	err := kubeClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, statefulSet)
	return statefulSet, err
}

// DeriveStatefulSetPhase derives OpenSaola's lifecycle phase from the complete
// StatefulSet object. Incomplete replica counters are rollout progress, not a
// failure; concrete Pod/PVC failures are handled by the synchronizer's
// readiness diagnostics.
func DeriveStatefulSetPhase(statefulSet *appsv1.StatefulSet, previousPhase v1.Phase) v1.Phase {
	if statefulSet == nil {
		return v1.PhaseUnknown
	}

	desired := desiredReplicas(statefulSet.Spec.Replicas)
	status := statefulSet.Status

	if status.ObservedGeneration >= statefulSet.Generation &&
		status.Replicas == desired &&
		status.ReadyReplicas == desired &&
		status.AvailableReplicas == desired &&
		statefulSetRevisionConverged(statefulSet, desired) {
		return v1.PhaseRunning
	}

	return workloadProgressPhase(statefulSet.Generation, previousPhase)
}

func statefulSetRevisionConverged(statefulSet *appsv1.StatefulSet, desired int32) bool {
	status := statefulSet.Status
	if desired == 0 {
		return status.CurrentReplicas == 0 && status.UpdatedReplicas == 0
	}

	strategy := statefulSet.Spec.UpdateStrategy
	if strategy.Type == appsv1.RollingUpdateStatefulSetStrategyType || strategy.Type == "" {
		partition := int32(0)
		if strategy.RollingUpdate != nil && strategy.RollingUpdate.Partition != nil {
			partition = *strategy.RollingUpdate.Partition
		}
		if partition > desired {
			partition = desired
		}
		if partition > 0 {
			return status.UpdatedReplicas >= desired-partition
		}
	}

	return status.CurrentReplicas == desired &&
		status.UpdatedReplicas == desired &&
		status.CurrentRevision != "" &&
		status.CurrentRevision == status.UpdateRevision
}
