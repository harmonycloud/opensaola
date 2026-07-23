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

// Package k8s wraps operations on native Kubernetes resources.
package k8s

import (
	"context"
	"fmt"

	"github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/pkg/tools"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

/*
File: deploy.go defines Deployment-related operations
including but not limited to: resource creation, update, and deletion.
*/

// CreateOrUpdateDeployment creates or updates a Deployment.
func CreateOrUpdateDeployment(ctx context.Context, cli client.Client, deployment *appsv1.Deployment) error {
	old, err := GetDeployment(ctx, cli, deployment.Name, deployment.Namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateDeployment(ctx, cli, deployment)
	}
	return UpdateDeployment(ctx, cli, deployment)
}

// CreateOrPatchDeployment creates or patches a Deployment.
func CreateOrPatchDeployment(ctx context.Context, cli client.Client, deployment *appsv1.Deployment) error {
	old, err := GetDeployment(ctx, cli, deployment.Name, deployment.Namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateDeployment(ctx, cli, deployment)
	}
	return PatchDeployment(ctx, cli, deployment)
}

// PatchDeployment updates a Deployment using patch.
func PatchDeployment(ctx context.Context, cli client.Client, deployment *appsv1.Deployment) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the current Deployment
		current, err := GetDeployment(ctx, cli, deployment.Name, deployment.Namespace)
		if err != nil {
			return err
		}

		// Use Patch to update only the changed fields
		patch := client.MergeFrom(current.DeepCopy())

		originalSelector := current.Spec.Selector
		originalTemplateLabels := current.Spec.Template.Labels
		current.Spec = deployment.Spec
		current.Spec.Selector = originalSelector
		current.Spec.Template.Labels = originalTemplateLabels

		err = cli.Patch(ctx, current, patch)
		if err != nil {
			return err
		}

		log.FromContext(ctx).Info("Patch Deployment succeeded", "name", deployment.Name, "namespace", deployment.Namespace)
		return nil
	})
}

// CreateDeployment creates a Deployment.
func CreateDeployment(ctx context.Context, cli client.Client, deployment *appsv1.Deployment) error {
	// Check if the Deployment already exists
	err := cli.Get(ctx, client.ObjectKey{
		Namespace: deployment.Namespace,
		Name:      deployment.Name,
	}, &appsv1.Deployment{})
	if err != nil && apierrors.IsNotFound(err) {
		if err = cli.Create(ctx, deployment); err != nil {
			return err
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("get deployment error: %w", err)
	}
	return nil
}

// UpdateDeployment updates a Deployment.
func UpdateDeployment(ctx context.Context, cli client.Client, deployment *appsv1.Deployment) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the Deployment
		newDeployment, err := GetDeployment(ctx, cli, deployment.Name, deployment.Namespace)
		if err != nil {
			return err
		}
		newDeployment.Spec = deployment.Spec
		// Retry updating the Deployment
		err = cli.Update(ctx, newDeployment)
		if err != nil {
			return err
		}
		log.FromContext(ctx).Info("Update Deployment succeeded", "name", deployment.Name, "namespace", deployment.Namespace)
		return nil
	})
}

// GetDeployment retrieves a Deployment.
func GetDeployment(ctx context.Context, cli client.Client, name, namespace string) (*appsv1.Deployment, error) {
	deployment := new(appsv1.Deployment)
	err := cli.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, deployment)
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func GetDeployments(ctx context.Context, clusterClient client.Client, namespace string, labels client.MatchingLabels) (*appsv1.DeploymentList, error) {
	var deployments appsv1.DeploymentList
	if err := clusterClient.List(ctx, &deployments, client.InNamespace(namespace), labels); err != nil {
		return nil, fmt.Errorf("list deployments error: %w", err)
	}
	return &deployments, nil
}

// DeleteDeployment deletes a Deployment.
func DeleteDeployment(ctx context.Context, cli client.Client, name, namespace string) error {
	deployment := new(appsv1.Deployment)
	err := cli.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, deployment)
	if err != nil {
		return err
	}
	if err = cli.Delete(ctx, deployment); err != nil {
		return err
	}
	return nil
}

// CompareDeploymentSpec compares two DeploymentSpecs.
func CompareDeploymentSpec(ctx context.Context, new, old *appsv1.Deployment) (isSame bool, err error) {
	if (old == nil && new != nil) || (old != nil && new == nil) {
		return false, nil
	} else if old == nil {
		return true, nil
	}
	return tools.CompareJson(ctx, new.Spec, old.Spec)
}

// DeriveDeploymentPhase derives OpenSaola's lifecycle phase from the complete
// Deployment object. It intentionally compares status counters with
// spec.replicas: status.replicas is the number of Pods already created, not the
// desired replica count.
func DeriveDeploymentPhase(deployment *appsv1.Deployment, previousPhase v1.Phase) v1.Phase {
	if deployment == nil {
		return v1.PhaseUnknown
	}

	desired := desiredReplicas(deployment.Spec.Replicas)
	status := deployment.Status
	observed := status.ObservedGeneration >= deployment.Generation
	terminating := status.TerminatingReplicas != nil && *status.TerminatingReplicas > 0

	if observed && deploymentHasFailed(status.Conditions) {
		return v1.PhaseFailed
	}

	if observed &&
		status.Replicas == desired &&
		status.UpdatedReplicas == desired &&
		status.ReadyReplicas == desired &&
		status.AvailableReplicas == desired &&
		status.UnavailableReplicas == 0 &&
		!terminating {
		return v1.PhaseRunning
	}

	return workloadProgressPhase(deployment.Generation, previousPhase)
}

func deploymentHasFailed(conditions []appsv1.DeploymentCondition) bool {
	for _, condition := range conditions {
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return true
		}
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}
