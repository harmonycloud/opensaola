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
	"fmt"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

/*
File: rbac.go defines RBAC-related operations
including ServiceAccount, ClusterRole, and ClusterRoleBinding operations.
*/

// CreateOrUpdateServiceAccount creates or updates a ServiceAccount.
func CreateOrUpdateServiceAccount(ctx context.Context, cli client.Client, serviceAccount *corev1.ServiceAccount) error {
	old, err := GetServiceAccount(ctx, cli, serviceAccount.Name, serviceAccount.Namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateServiceAccount(ctx, cli, serviceAccount)
	}
	return UpdateServiceAccount(ctx, cli, serviceAccount)
}

// GetServiceAccount retrieves a ServiceAccount.
func GetServiceAccount(ctx context.Context, cli client.Client, name, namespace string) (*corev1.ServiceAccount, error) {
	sa := new(corev1.ServiceAccount)
	err := cli.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, sa)
	if err != nil {
		return nil, fmt.Errorf("get service account %s error: %w", name, err)
	}
	return sa, nil
}

// CreateServiceAccount creates a ServiceAccount.
func CreateServiceAccount(ctx context.Context, cli client.Client, serviceAccount *corev1.ServiceAccount) error {
	var existServiceAccount = new(corev1.ServiceAccount)

	// Check if the ServiceAccount already exists
	err := cli.Get(ctx, client.ObjectKey{
		Namespace: serviceAccount.Namespace,
		Name:      serviceAccount.Name,
	}, existServiceAccount)
	if err != nil && apierrors.IsNotFound(err) {
		// Create the ServiceAccount
		err = cli.Create(ctx, serviceAccount)
		if err != nil {
			return fmt.Errorf("create service account error: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get service account error: %w", err)
	}
	log.FromContext(ctx).Info("Create ServiceAccount succeeded", "name", serviceAccount.Name, "namespace", serviceAccount.Namespace)
	return nil
}

// DeleteServiceAccount deletes a ServiceAccount.
func DeleteServiceAccount(ctx context.Context, cli client.Client, serviceAccount *corev1.ServiceAccount) error {
	// Delete the ServiceAccount
	err := cli.Delete(ctx, serviceAccount)
	if err != nil {
		return err
	}
	log.FromContext(ctx).Info("Delete ServiceAccount succeeded", "name", serviceAccount.Name, "namespace", serviceAccount.Namespace)
	return nil
}

// UpdateServiceAccount updates a ServiceAccount.
func UpdateServiceAccount(ctx context.Context, cli client.Client, serviceAccount *corev1.ServiceAccount) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now := new(corev1.ServiceAccount)
		if err := cli.Get(ctx, client.ObjectKey{
			Namespace: serviceAccount.Namespace,
			Name:      serviceAccount.Name,
		}, now); err != nil {
			return err
		}
		now.Labels = serviceAccount.Labels
		now.Annotations = serviceAccount.Annotations
		now.AutomountServiceAccountToken = serviceAccount.AutomountServiceAccountToken
		now.ImagePullSecrets = serviceAccount.ImagePullSecrets
		now.Secrets = serviceAccount.Secrets
		if err := cli.Update(ctx, now); err != nil {
			return fmt.Errorf("update service account error: %w", err)
		}
		log.FromContext(ctx).Info("Update ServiceAccount succeeded", "name", serviceAccount.Name, "namespace", serviceAccount.Namespace)
		return nil
	})
}

// CreateOrUpdateRole creates or updates a Role.
func CreateOrUpdateRole(ctx context.Context, cli client.Client, role *rbacv1.Role) error {
	old, err := GetRole(ctx, cli, role.Name, role.Namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateRole(ctx, cli, role)
	}
	return UpdateRole(ctx, cli, role)
}

// CreateRole creates a Role.
func CreateRole(ctx context.Context, cli client.Client, role *rbacv1.Role) error {
	var existRole = new(rbacv1.Role)

	// Check if the Role already exists
	err := cli.Get(ctx, client.ObjectKey{Name: role.Name}, existRole)
	if err != nil && apierrors.IsNotFound(err) {
		// Create the Role
		err = cli.Create(ctx, role)
		if err != nil {
			return fmt.Errorf("create role error: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get role error: %w", err)
	}
	if role.Name == existRole.Name && role.Namespace == existRole.Namespace && !cmp.Equal(role.Rules, existRole.Rules) {
		return fmt.Errorf("role with same name but different rules already exists: %s", role.Name)
	}
	log.FromContext(ctx).Info("Create Role succeeded", "name", role.Name, "namespace", role.Namespace)
	return nil
}

// UpdateRole updates a Role.
func UpdateRole(ctx context.Context, cli client.Client, role *rbacv1.Role) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now := new(rbacv1.Role)
		if err := cli.Get(ctx, client.ObjectKey{Name: role.Name, Namespace: role.Namespace}, now); err != nil {
			return err
		}
		now.Labels = role.Labels
		now.Annotations = role.Annotations
		now.Rules = role.Rules
		if err := cli.Update(ctx, now); err != nil {
			return fmt.Errorf("update role error: %w", err)
		}
		log.FromContext(ctx).Info("Update Role succeeded", "name", role.Name, "namespace", role.Namespace)
		return nil
	})
}

// GetRole retrieves a Role.
func GetRole(ctx context.Context, cli client.Client, name, namespace string) (*rbacv1.Role, error) {
	role := new(rbacv1.Role)
	// Get the Role
	err := cli.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, role)
	if err != nil {
		return nil, fmt.Errorf("get role %s error: %w", name, err)
	}
	return role, nil
}

// DeleteRole deletes a Role.
func DeleteRole(ctx context.Context, cli client.Client, name, namespace string) error {
	role, err := GetRole(ctx, cli, name, namespace)
	if err != nil {
		return fmt.Errorf("get role error: %w", err)
	}
	// Delete the Role
	err = cli.Delete(ctx, role)
	if err != nil {
		return err
	}
	log.FromContext(ctx).Info("Delete Role succeeded", "name", role.Name, "namespace", role.Namespace)
	return nil
}

// CreateOrUpdateClusterRole creates or updates a ClusterRole.
func CreateOrUpdateClusterRole(ctx context.Context, cli client.Client, clusterRole *rbacv1.ClusterRole) error {
	old, err := GetClusterRole(ctx, cli, clusterRole.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateClusterRole(ctx, cli, clusterRole)
	}
	return UpdateClusterRole(ctx, cli, clusterRole)
}

// CreateClusterRole creates a ClusterRole.
func CreateClusterRole(ctx context.Context, cli client.Client, clusterRole *rbacv1.ClusterRole) error {
	var existClusterRole = new(rbacv1.ClusterRole)

	// Check if the ClusterRole already exists
	err := cli.Get(ctx, client.ObjectKey{Name: clusterRole.Name}, existClusterRole)
	if err != nil && apierrors.IsNotFound(err) {
		// Create the ClusterRole
		err = cli.Create(ctx, clusterRole)
		if err != nil {
			return fmt.Errorf("create cluster role error: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get cluster role error: %w", err)
	}
	if existClusterRole.Name == clusterRole.Name && !cmp.Equal(clusterRole.Rules, existClusterRole.Rules) {
		return fmt.Errorf("cluster role with same name but different rules already exists: %s", clusterRole.Name)
	}
	log.FromContext(ctx).Info("Create ClusterRole succeeded", "name", clusterRole.Name)
	return nil
}

// UpdateClusterRole updates a ClusterRole.
func UpdateClusterRole(ctx context.Context, cli client.Client, cr *rbacv1.ClusterRole) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now := new(rbacv1.ClusterRole)
		if err := cli.Get(ctx, client.ObjectKey{Name: cr.Name}, now); err != nil {
			return err
		}
		now.Labels = cr.Labels
		now.Annotations = cr.Annotations
		now.Rules = cr.Rules
		now.AggregationRule = cr.AggregationRule
		if err := cli.Update(ctx, now); err != nil {
			return fmt.Errorf("update cluster role error: %w", err)
		}
		log.FromContext(ctx).Info("Update ClusterRole succeeded", "name", cr.Name)
		return nil
	})
}

// GetClusterRole retrieves a ClusterRole.
func GetClusterRole(ctx context.Context, cli client.Client, name string) (*rbacv1.ClusterRole, error) {
	cr := new(rbacv1.ClusterRole)
	// Get the ClusterRole
	err := cli.Get(ctx, client.ObjectKey{Name: name}, cr)
	if err != nil {
		return nil, fmt.Errorf("get cluster role %s error: %w", name, err)
	}
	return cr, nil
}

// DeleteClusterRole deletes a ClusterRole.
func DeleteClusterRole(ctx context.Context, cli client.Client, name string) error {
	cr, err := GetClusterRole(ctx, cli, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete cluster role error: %w", err)
	}
	// Delete the ClusterRole
	err = cli.Delete(ctx, cr)
	if err != nil {
		return err
	}
	log.FromContext(ctx).Info("Delete ClusterRole succeeded", "name", cr.Name, "namespace", cr.Namespace)
	return nil
}

// CreateOrUpdateRoleBinding creates or updates a RoleBinding.
func CreateOrUpdateRoleBinding(ctx context.Context, cli client.Client, roleBinding *rbacv1.RoleBinding) error {
	old, err := GetRoleBinding(ctx, cli, roleBinding.Name, roleBinding.Namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateRoleBinding(ctx, cli, roleBinding)
	}
	return UpdateRoleBinding(ctx, cli, roleBinding)
}

// CreateRoleBinding creates a RoleBinding.
func CreateRoleBinding(ctx context.Context, cli client.Client, rb *rbacv1.RoleBinding) error {
	var existRb = new(rbacv1.RoleBinding)

	// Check if the RoleBinding already exists
	err := cli.Get(ctx, client.ObjectKey{Name: rb.Name}, existRb)
	if err != nil && apierrors.IsNotFound(err) {
		// Create the RoleBinding
		err = cli.Create(ctx, rb)
		if err != nil {
			return fmt.Errorf("create role binding error: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get role error: %w", err)
	}
	if rb.Name == existRb.Name && rb.Namespace == existRb.Namespace &&
		(!cmp.Equal(rb.Subjects, existRb.Subjects) || !cmp.Equal(rb.RoleRef, existRb.RoleRef)) {
		return fmt.Errorf("role binding with same name but different spec already exists: %s", rb.Name)
	}
	log.FromContext(ctx).Info("Create RoleBinding succeeded", "name", rb.Name, "namespace", rb.Namespace)
	return nil
}

// UpdateRoleBinding updates a RoleBinding.
func UpdateRoleBinding(ctx context.Context, cli client.Client, rb *rbacv1.RoleBinding) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now := new(rbacv1.RoleBinding)
		if err := cli.Get(ctx, client.ObjectKey{Name: rb.Name, Namespace: rb.Namespace}, now); err != nil {
			return err
		}
		now.Labels = rb.Labels
		now.Annotations = rb.Annotations
		now.Subjects = rb.Subjects
		now.RoleRef = rb.RoleRef
		if err := cli.Update(ctx, now); err != nil {
			return fmt.Errorf("update role binding error: %w", err)
		}
		log.FromContext(ctx).Info("Update RoleBinding succeeded", "name", rb.Name, "namespace", rb.Namespace)
		return nil
	})
}

// GetRoleBinding retrieves a RoleBinding.
func GetRoleBinding(ctx context.Context, cli client.Client, name, namespace string) (*rbacv1.RoleBinding, error) {
	rb := new(rbacv1.RoleBinding)
	// Get the RoleBinding
	err := cli.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, rb)
	if err != nil {
		return nil, fmt.Errorf("get role binding %s error: %w", name, err)
	}
	return rb, nil
}

// DeleteRoleBinding deletes a RoleBinding.
func DeleteRoleBinding(ctx context.Context, cli client.Client, name, namespace string) error {
	rb, err := GetRoleBinding(ctx, cli, name, namespace)
	if err != nil {
		return fmt.Errorf("get role binding error: %w", err)
	}
	// Delete the RoleBinding
	err = cli.Delete(ctx, rb)
	if err != nil {
		return err
	}
	log.FromContext(ctx).Info("Delete RoleBinding succeeded", "name", rb.Name, "namespace", rb.Namespace)
	return nil
}

// CreateOrUpdateClusterRoleBinding creates or updates a ClusterRoleBinding.
func CreateOrUpdateClusterRoleBinding(ctx context.Context, cli client.Client, clusterRoleBinding *rbacv1.ClusterRoleBinding) error {
	old, err := GetClusterRoleBinding(ctx, cli, clusterRoleBinding.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if old == nil {
		return CreateClusterRoleBinding(ctx, cli, clusterRoleBinding)
	}
	return UpdateClusterRoleBinding(ctx, cli, clusterRoleBinding)
}

// CreateClusterRoleBinding creates a ClusterRoleBinding.
func CreateClusterRoleBinding(ctx context.Context, cli client.Client, crb *rbacv1.ClusterRoleBinding) error {
	var existCrb = new(rbacv1.ClusterRoleBinding)

	// Check if the ClusterRoleBinding already exists
	err := cli.Get(ctx, client.ObjectKey{Name: crb.Name}, existCrb)
	if err != nil && apierrors.IsNotFound(err) {
		// Create the ClusterRoleBinding
		err = cli.Create(ctx, crb)
		if err != nil {
			return fmt.Errorf("create cluster role binding error: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get service account error: %w", err)
	}
	if crb.Name == existCrb.Name &&
		(!cmp.Equal(crb.Subjects, existCrb.Subjects) || !cmp.Equal(crb.RoleRef, existCrb.RoleRef)) {
		return fmt.Errorf("cluster role binding with same name but different spec already exists: %s", crb.Name)
	}
	log.FromContext(ctx).Info("Create ClusterRoleBinding succeeded", "name", crb.Name)
	return nil
}

// UpdateClusterRoleBinding updates a ClusterRoleBinding.
func UpdateClusterRoleBinding(ctx context.Context, cli client.Client, crb *rbacv1.ClusterRoleBinding) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now := new(rbacv1.ClusterRoleBinding)
		if err := cli.Get(ctx, client.ObjectKey{Name: crb.Name}, now); err != nil {
			return err
		}
		now.Labels = crb.Labels
		now.Annotations = crb.Annotations
		now.Subjects = crb.Subjects
		now.RoleRef = crb.RoleRef
		if err := cli.Update(ctx, now); err != nil {
			return fmt.Errorf("update cluster role binding error: %w", err)
		}
		log.FromContext(ctx).Info("Update ClusterRoleBinding succeeded", "name", crb.Name)
		return nil
	})
}

// GetClusterRoleBinding retrieves a ClusterRoleBinding.
func GetClusterRoleBinding(ctx context.Context, cli client.Client, name string) (*rbacv1.ClusterRoleBinding, error) {
	crb := new(rbacv1.ClusterRoleBinding)
	// Get the ClusterRoleBinding
	err := cli.Get(ctx, client.ObjectKey{Name: name}, crb)
	if err != nil {
		return nil, fmt.Errorf("get cluster role binding %s error: %w", name, err)
	}
	return crb, nil
}

// DeleteClusterRoleBinding deletes a ClusterRoleBinding.
func DeleteClusterRoleBinding(ctx context.Context, cli client.Client, name string) error {
	crb, err := GetClusterRoleBinding(ctx, cli, name)
	if err != nil {
		return fmt.Errorf("get cluster role binding error: %w", err)
	}
	// Delete the ClusterRoleBinding
	err = cli.Delete(ctx, crb)
	if err != nil {
		return err
	}
	log.FromContext(ctx).Info("Delete ClusterRoleBinding succeeded", "name", crb.Name)
	return nil
}
