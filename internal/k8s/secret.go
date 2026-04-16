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

	"github.com/harmonycloud/opensaola/internal/cache"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
)

// CreateSecret creates a Secret resource.
func CreateSecret(ctx context.Context, cli client.Client, s *corev1.Secret) error {
	existSecret := new(corev1.Secret)
	// Check if the CR already exists
	err := cli.Get(ctx, client.ObjectKey{
		Name:      s.GetName(),
		Namespace: s.GetNamespace(),
	}, existSecret)
	if err != nil && apierrors.IsNotFound(err) {
		if err = cli.Create(ctx, s); err != nil {
			return err
		}
		log.FromContext(ctx).Info(fmt.Sprintf("Create %s succeeded", s.Kind), "name", s.GetName(), "namespace", s.GetNamespace())
		return nil
	} else if err != nil {
		return fmt.Errorf("get %s error: %w", s.Kind, err)
	}
	return nil
}

// DeleteSecret deletes a Secret resource.
func DeleteSecret(ctx context.Context, cli client.Client, name, namespace string) error {
	existSecret := new(corev1.Secret)
	// Check if the CR exists
	err := cli.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, existSecret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	err = cli.Delete(ctx, existSecret)
	if err != nil {
		return err
	}
	log.FromContext(ctx).Info(fmt.Sprintf("Delete %s succeeded", existSecret.Kind), "name", existSecret.GetName(), "namespace", existSecret.GetNamespace())
	return nil
}

// UpdateSecret updates only the metadata (labels/annotations) of a Secret
// using MergePatch. This avoids sending the data field, which would fail
// on immutable Secrets when the informer cache has stripped Secret.Data
// via TransformFunc.
// (see English comment above)
func UpdateSecret(ctx context.Context, cli client.Client, s *corev1.Secret) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now := new(corev1.Secret)
		err := cli.Get(ctx, client.ObjectKey{
			Name:      s.GetName(),
			Namespace: s.GetNamespace(),
		}, now)
		if err != nil {
			return err
		}

		patch := client.MergeFrom(now.DeepCopy())
		now.SetLabels(s.GetLabels())
		now.SetAnnotations(s.GetAnnotations())
		return cli.Patch(ctx, now, patch)
	})
}

var SecretCache = cache.New[string, corev1.Secret](0)

// GetSecret retrieves a Secret resource.
func GetSecret(ctx context.Context, cli client.Client, name, namespace string) (*corev1.Secret, error) {
	s := new(corev1.Secret)
	err := cli.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, s)
	if err != nil {
		return nil, err
	}
	SecretCache.Set(types.NamespacedName{Name: name, Namespace: namespace}.String(), *s)
	return s, nil
}

// GetSecrets retrieves a list of Secret resources.
func GetSecrets(ctx context.Context, cli client.Client, namespace string, labelSelector client.MatchingLabels) (*corev1.SecretList, error) {
	if cli == nil {
		return nil, fmt.Errorf("k8s client is nil")
	}
	list := new(corev1.SecretList)

	err := cli.List(ctx, list, client.InNamespace(namespace), labelSelector)
	if err != nil {
		return nil, fmt.Errorf("list %s error: %w", list.Kind, err)
	}
	return list, nil
}
