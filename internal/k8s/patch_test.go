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
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

var originalData = `{
	"spec": {
		"replicas": 1,
		"selector": {
			"matchLabels": {
				"app.kubernetes.io/instance": "test-instance",
				"app.kubernetes.io/name": "postgresql-operator"
			}
		},
		"template": {
			"metadata": {
				"annotations": {
					"checksum/config": "f76a57ae0f5a93caf9e099340f4fbdc234d8b6ee81765814bb497c341cbfb68b"
				},
				"labels": {
					"app.kubernetes.io/instance": "test-instance",
					"app.kubernetes.io/name": "postgresql-operator"
				}
			},
			"spec": {
				"containers": [
					{
						"name": "postgresql-operator",
						"image": "test-repo/test-project/postgres-operator:v1.7.1-2.5.1",
						"resources": {
							"limits": {
								"cpu": "500m",
								"memory": "500Mi"
							},
							"requests": {
								"cpu": "100m",
								"memory": "250Mi"
							}
						}
					}
				]
			}
		}
	}
}`

var newData = `{
	"spec": {
		"replicas": 2,
		"selector": {
			"matchLabels": {
				"app.kubernetes.io/instance": "test-instance",
				"app.kubernetes.io/name": "postgresql-operator"
			}
		},
		"template": {
			"metadata": {
				"annotations": {
					"checksum/config": "f76a57ae0f5a93caf9e099340f4fbdc234d8b6ee81765814bb497c341cbfb68b"
				},
				"labels": {
					"app.kubernetes.io/instance": "test-instance",
					"app.kubernetes.io/name": "postgresql-operator"
				}
			},
			"spec": {
				"containers": [
					{
						"name": "postgresql-operator",
						"image": "test-repo/test-project/postgres-operator:v1.7.1-2.5.1",
						"resources": {
							"limits": {
								"cpu": "1000m",
								"memory": "1Gi"
							},
							"requests": {
								"cpu": "200m",
								"memory": "500Mi"
							}
						}
					}
				]
			}
		}
	}
}`

func TestPatch(t *testing.T) {
	// Create the original Deployment
	originalDeployment := &appsv1.Deployment{}
	if err := json.Unmarshal([]byte(originalData), originalDeployment); err != nil {
		t.Fatalf("Failed to unmarshal original deployment: %v", err)
	}

	// Create the modified Deployment
	newDeployment := &appsv1.Deployment{}
	if err := json.Unmarshal([]byte(newData), newDeployment); err != nil {
		t.Fatalf("Failed to unmarshal new deployment: %v", err)
	}

	// Create a Strategic Merge Patch
	patch, err := CreatePatch(originalDeployment, newDeployment, StrategicMergePatchType)
	if err != nil {
		t.Fatalf("Failed to create patch: %v", err)
	}

	// Apply the patch
	patchedDeployment, err := ApplyPatch(originalDeployment, patch)
	if err != nil {
		t.Fatalf("Failed to apply patch: %v", err)
	}

	// Validate the patch
	if err := ValidatePatch(originalDeployment, newDeployment, patch); err != nil {
		t.Fatalf("Patch validation failed: %v", err)
	}

	// Verify specific fields
	if patchedDeployment.(*appsv1.Deployment).Spec.Replicas == nil || *patchedDeployment.(*appsv1.Deployment).Spec.Replicas != 2 {
		t.Errorf("Expected replicas to be 2, got %v", patchedDeployment.(*appsv1.Deployment).Spec.Replicas)
	}

	containers := patchedDeployment.(*appsv1.Deployment).Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Errorf("Expected 1 container, got %d", len(containers))
	}

	container := containers[0]
	if container.Resources.Limits.Cpu().String() != "1" {
		t.Errorf("Expected CPU limit to be 1, got %v", container.Resources.Limits.Cpu())
	}
	if container.Resources.Limits.Memory().String() != "1Gi" {
		t.Errorf("Expected Memory limit to be 1Gi, got %v", container.Resources.Limits.Memory())
	}
	if container.Resources.Requests.Cpu().String() != "200m" {
		t.Errorf("Expected CPU request to be 200m, got %v", container.Resources.Requests.Cpu())
	}
	if container.Resources.Requests.Memory().String() != "500Mi" {
		t.Errorf("Expected Memory request to be 500Mi, got %v", container.Resources.Requests.Memory())
	}
}
