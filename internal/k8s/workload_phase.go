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

import v1 "github.com/harmonycloud/opensaola/api/v1"

func desiredReplicas(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
}

// workloadProgressPhase distinguishes an initial rollout from a later
// reconciliation. Kubernetes objects start at generation 1, while a workload
// that OpenSaola has already observed as Running or Updating is no longer in
// its initial creation lifecycle.
func workloadProgressPhase(generation int64, previousPhase v1.Phase) v1.Phase {
	if generation > 1 || previousPhase == v1.PhaseRunning || previousPhase == v1.PhaseUpdating {
		return v1.PhaseUpdating
	}
	return v1.PhaseCreating
}
