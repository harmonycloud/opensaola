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

	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/tidwall/gjson"

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

func GetStatefulSetsPhase(status []byte) string {
	readyReplicas := gjson.GetBytes(status, "readyReplicas").Int()
	replicas := gjson.GetBytes(status, "replicas").Int()
	currentReplicas := gjson.GetBytes(status, "currentReplicas").Int()
	updatedReplicas := gjson.GetBytes(status, "updatedReplicas").Int()

	// If desired replicas is 0, return Unknown phase
	if replicas == 0 {
		return string(v1.PhaseUnknown)
	}

	// Check if in creating phase: no current replicas and no ready replicas
	if currentReplicas == 0 && readyReplicas == 0 {
		// Further check conditions to confirm whether it is being created
		if isStatefulSetProgressing(status) {
			return string(v1.PhaseCreating)
		}
		return string(v1.PhaseFailed)
	}

	// Check if in updating phase: some replicas updated but not all ready
	if updatedReplicas > 0 && readyReplicas < replicas && currentReplicas > 0 {
		return string(v1.PhaseUpdating)
	}

	// Check if running normally: ready replicas equals desired replicas
	if readyReplicas == replicas && currentReplicas == replicas {
		return string(v1.PhaseRunning)
	}

	// All other cases are considered failed
	return string(v1.PhaseFailed)
}

// isStatefulSetProgressing checks whether a StatefulSet is in a progressing state.
func isStatefulSetProgressing(status []byte) bool {
	conditions := gjson.GetBytes(status, "conditions").Array()
	for _, condition := range conditions {
		conditionType := condition.Get("type").String()
		conditionStatus := condition.Get("status").String()
		conditionReason := condition.Get("reason").String()

		// Check the Progressing condition (if present)
		if conditionType == "Progressing" && conditionStatus == "True" {
			// Common reasons indicating creation in progress
			creatingReasons := []string{
				"StatefulSetCreated",
				"StatefulSetUpdated",
				"PartitionRolling",
			}

			for _, reason := range creatingReasons {
				if conditionReason == reason {
					return true
				}
			}
		}
	}

	// StatefulSet may not have a Progressing condition; use other indicators instead.
	// If there is an observed generation but no replicas, it may be in the process of being created.
	observedGeneration := gjson.GetBytes(status, "observedGeneration").Int()
	return observedGeneration > 0
}
