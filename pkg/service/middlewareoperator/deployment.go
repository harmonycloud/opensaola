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

package middlewareoperator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/k8s"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/consts"
	"github.com/OpenSaola/opensaola/pkg/service/status"
	"github.com/OpenSaola/opensaola/pkg/tools/ctxkeys"
	appsv1 "k8s.io/api/apps/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Step5: MiddlewareOperator Deployment generation
// buildDeployment builds the Deployment
func buildDeployment(ctx context.Context, cli client.Client, action consts.HandleAction, m *v1.MiddlewareOperator) (err error) {
	conditionApplyOperator := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeApplyOperator)
	// if conditionApplyOperator.Status != metav1.ConditionTrue {
	defer func() {
		if action != consts.HandleActionDelete {
			if err != nil {
				logger.Log.Errorf("build deployment error: %v", err)
				conditionApplyOperator.Failed(ctx, err.Error(), m.Generation)
			} else {
				logger.Log.Infof("build deployment finished")
				conditionApplyOperator.Success(ctx, m.Generation)
			}
			errUpdateStatus := k8s.UpdateMiddlewareOperatorStatus(ctx, cli, m)
			if errUpdateStatus != nil {
				err = errUpdateStatus
				logger.Log.Errorf("update middleware operator status error: %v", err)
			}
		}
	}()

	// Delete path only needs name/namespace
	if action == consts.HandleActionDelete {
		err = k8s.DeleteDeployment(ctx, cli, m.Name, m.Namespace)
		if err != nil && !apiErrors.IsNotFound(err) {
			return fmt.Errorf("delete deployment error: %w", err)
		}
		return nil
	}

	deployment := new(appsv1.Deployment)
	err = json.Unmarshal(m.Spec.Deployment.Raw, deployment)
	if err != nil {
		return fmt.Errorf("unmarshal deployment error: %w", err)
	}

	if deployment == nil || len(deployment.Spec.Template.Spec.Containers) == 0 {
		return nil
	}

	deployment.Name = m.Name
	deployment.Namespace = m.Namespace
	deployment.Labels = m.Labels

	switch action {
	case consts.HandleActionPublish:
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err = ctrl.SetControllerReference(m, deployment, scheme)
		if err != nil {
			return fmt.Errorf("set controller reference error: %w", err)
		}
		logger.Log.Infof("creating deployment: %s", deployment.Name)
		err = k8s.CreateDeployment(ctx, cli, deployment)
		if err != nil {
			return fmt.Errorf("create deployment error: %w", err)
		}
	case consts.HandleActionUpdate:
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err = ctrl.SetControllerReference(m, deployment, scheme)
		if err != nil {
			return fmt.Errorf("set controller reference error: %w", err)
		}
		err = k8s.CreateOrPatchDeployment(ctx, cli, deployment)
		if err != nil {
			return fmt.Errorf("update deployment error: %w", err)
		}
	case consts.HandleActionDelete:
		err = k8s.DeleteDeployment(ctx, cli, deployment.Name, deployment.Namespace)
		if err != nil && !apiErrors.IsNotFound(err) {
			return fmt.Errorf("delete deployment error: %w", err)
		}
	}

	// }
	return nil
}

// CompareDeployment compares the Deployment
func CompareDeployment(ctx context.Context, cli client.Client, deployment *appsv1.Deployment, m *v1.MiddlewareOperator) error {
	err := TemplateParseWithBaseline(ctx, cli, m)
	if err != nil {
		return err
	}

	// Get the Deployment as it was at publish time
	oldDeployment := new(appsv1.Deployment)
	err = json.Unmarshal(m.Spec.Deployment.Raw, &oldDeployment)
	if err != nil {
		return fmt.Errorf("unmarshal old deployment error: %w", err)
	}

	// Compare Deployment
	isSame, err := k8s.CompareDeploymentSpec(ctx, deployment, oldDeployment)
	if err != nil {
		return fmt.Errorf("compare deployment error: %w", err)
	}
	if !isSame {
		oldDeployment.Name = m.Name
		oldDeployment.Namespace = m.Namespace
		oldDeployment.Labels = m.Labels
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err = ctrl.SetControllerReference(m, oldDeployment, scheme)
		if err != nil {
			return fmt.Errorf("set controller reference error: %w", err)
		}
		// If not equal, update and restore the Deployment to its published state
		err = k8s.UpdateDeployment(ctx, cli, oldDeployment)
		if err != nil {
			return fmt.Errorf("update deployment error: %w", err)
		}
	}
	return nil
}
