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
	"errors"
	"fmt"
	"strings"

	"github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Step4: MiddlewareOperator RBAC generation
// handleRBAC handles RBAC resources
func handleRBAC(ctx context.Context, cli client.Client, act consts.HandleAction, m *v1.MiddlewareOperator) (err error) {
	var (
		errs               []string
		conditionApplyRBAC = status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeApplyRBAC)
	)
	defer func() {
		if act != consts.HandleActionDelete {
			if len(errs) > 0 {
				err = errors.New(strings.Join(errs, ";"))
				log.FromContext(ctx).Error(err, "rbac error", "action", act)
				conditionApplyRBAC.Failed(ctx, err.Error(), m.Generation)
			} else {
				log.FromContext(ctx).Info("rbac finished", "action", act)
				conditionApplyRBAC.Success(ctx, m.Generation)
			}
			errUpdateStatus := k8s.UpdateMiddlewareOperatorStatus(ctx, cli, m)
			if errUpdateStatus != nil {
				err = errUpdateStatus
				log.FromContext(ctx).Error(err, "update middleware operator status error")
			}
		}
	}()

	// Generate RBAC
	for _, permission := range m.Spec.Permissions {
		err = handleServiceAccount(ctx, cli, permission, act, m)
		if err != nil {
			if act == consts.HandleActionDelete {
				log.FromContext(ctx).Info("failed to delete ServiceAccount",
					"warning", true,
					"name", m.Name,
					"namespace", m.Namespace,
					"permissionScope", m.Spec.PermissionScope,
					"serviceAccountName", permission.ServiceAccountName,
					"err", err.Error(),
				)
			}
			errs = append(errs, fmt.Sprintf("%s service account error: %v", act, err))
		}
		err = handleRole(ctx, cli, permission, act, m)
		if err != nil {
			if act == consts.HandleActionDelete {
				log.FromContext(ctx).Info("failed to delete Role/ClusterRole",
					"warning", true,
					"name", m.Name,
					"namespace", m.Namespace,
					"permissionScope", m.Spec.PermissionScope,
					"serviceAccountName", permission.ServiceAccountName,
					"err", err.Error(),
				)
			}
			errs = append(errs, fmt.Sprintf("%s role error: %v", act, err))
		}
		err = handleRoleBinding(ctx, cli, permission, act, m)
		if err != nil {
			if act == consts.HandleActionDelete {
				log.FromContext(ctx).Info("failed to delete RoleBinding/ClusterRoleBinding",
					"warning", true,
					"name", m.Name,
					"namespace", m.Namespace,
					"permissionScope", m.Spec.PermissionScope,
					"serviceAccountName", permission.ServiceAccountName,
					"err", err.Error(),
				)
			}
			errs = append(errs, fmt.Sprintf("%s role binding error: %v", act, err))
		}
	}
	// }
	return nil
}

// handleServiceAccount handles ServiceAccount resources
func handleServiceAccount(ctx context.Context, cli client.Client, permission v1.Permission,
	action consts.HandleAction, m *v1.MiddlewareOperator) error {
	sa := new(corev1.ServiceAccount)
	sa.Name = permission.ServiceAccountName
	sa.Namespace = m.Namespace
	sa.Labels = m.Labels
	switch action {
	case consts.HandleActionPublish:
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err := ctrl.SetControllerReference(m, sa, scheme)
		if err != nil {
			return fmt.Errorf("set controller reference error: %w", err)
		}
		err = k8s.CreateServiceAccount(ctx, cli, sa)
		if err != nil {
			return err
		}
	case consts.HandleActionUpdate:
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err := ctrl.SetControllerReference(m, sa, scheme)
		if err != nil {
			return fmt.Errorf("set controller reference error: %w", err)
		}
		err = k8s.CreateOrUpdateServiceAccount(ctx, cli, sa)
		if err != nil {
			return err
		}
	case consts.HandleActionDelete:
		err := k8s.DeleteServiceAccount(ctx, cli, sa)
		if err != nil && !apiErrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// handleRole handles Role or ClusterRole resources
func handleRole(ctx context.Context, cli client.Client, permission v1.Permission,
	action consts.HandleAction, m *v1.MiddlewareOperator) error {
	if m.Spec.PermissionScope == v1.PermissionScopeCluster {
		cr := new(rbacv1.ClusterRole)
		cr.Name = permission.ServiceAccountName
		cr.Rules = permission.Rules
		cr.Labels = m.Labels
		switch action {
		case consts.HandleActionPublish:
			err := k8s.CreateClusterRole(ctx, cli, cr)
			if err != nil {
				return err
			}
		case consts.HandleActionDelete:
			err := k8s.DeleteClusterRole(ctx, cli, cr.Name)
			if err != nil && !apiErrors.IsNotFound(err) {
				return err
			}
		case consts.HandleActionUpdate:
			err := k8s.CreateOrUpdateClusterRole(ctx, cli, cr)
			if err != nil {
				return err
			}
		}
	} else if m.Spec.PermissionScope == v1.PermissionScopeNamespace {
		role := new(rbacv1.Role)
		role.Name = permission.ServiceAccountName
		role.Namespace = m.Namespace
		role.Rules = permission.Rules
		role.Labels = m.Labels
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err := ctrl.SetControllerReference(m, role, scheme)
		if err != nil {
			return fmt.Errorf("set controller reference error: %w", err)
		}
		switch action {
		case consts.HandleActionPublish:
			err = k8s.CreateRole(ctx, cli, role)
			if err != nil {
				return err
			}
		case consts.HandleActionUpdate:
			err = k8s.CreateOrUpdateRole(ctx, cli, role)
			if err != nil {
				return err
			}
		case consts.HandleActionDelete:
			err = k8s.DeleteRole(ctx, cli, role.Name, role.Namespace)
			if err != nil && !apiErrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// handleRoleBinding handles RoleBinding or ClusterRoleBinding resources
func handleRoleBinding(ctx context.Context, cli client.Client, permission v1.Permission,
	action consts.HandleAction, m *v1.MiddlewareOperator) error {
	if m.Spec.PermissionScope == v1.PermissionScopeCluster {
		crb := new(rbacv1.ClusterRoleBinding)
		crb.Name = permission.ServiceAccountName
		crb.Subjects = append(crb.Subjects, rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      permission.ServiceAccountName,
			Namespace: m.Namespace,
		})
		crb.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     permission.ServiceAccountName,
		}
		crb.Labels = m.Labels

		switch action {
		case consts.HandleActionPublish:
			err := k8s.CreateClusterRoleBinding(ctx, cli, crb)
			if err != nil {
				return err
			}
		case consts.HandleActionDelete:
			err := k8s.DeleteClusterRoleBinding(ctx, cli, crb.Name)
			if err != nil && !apiErrors.IsNotFound(err) {
				return err
			}
		case consts.HandleActionUpdate:
			err := k8s.CreateOrUpdateClusterRoleBinding(ctx, cli, crb)
			if err != nil {
				return err
			}
		}
	} else if m.Spec.PermissionScope == v1.PermissionScopeNamespace {
		rb := new(rbacv1.RoleBinding)
		rb.Name = permission.ServiceAccountName
		rb.Namespace = m.Namespace
		rb.Subjects = append(rb.Subjects, rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      permission.ServiceAccountName,
			Namespace: m.Namespace,
		})
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     permission.ServiceAccountName,
		}
		rb.Labels = m.Labels
		scheme, schemeErr := ctxkeys.SchemeFrom(ctx)
		if schemeErr != nil {
			return fmt.Errorf("get scheme from context: %w", schemeErr)
		}
		err := ctrl.SetControllerReference(m, rb, scheme)
		if err != nil {
			return fmt.Errorf("set controller reference error: %w", err)
		}
		switch action {
		case consts.HandleActionPublish:
			err = k8s.CreateRoleBinding(ctx, cli, rb)
			if err != nil {
				return err
			}
		case consts.HandleActionDelete:
			err := k8s.DeleteRoleBinding(ctx, cli, rb.Name, rb.Namespace)
			if err != nil && !apiErrors.IsNotFound(err) {
				return err
			}
		case consts.HandleActionUpdate:
			err := k8s.CreateOrUpdateRoleBinding(ctx, cli, rb)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
