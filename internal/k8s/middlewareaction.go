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

	"github.com/harmonycloud/opensaola/api/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func MiddlewareActionGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "middleware.cn",
		Version: "v1",
		Kind:    "MiddlewareAction",
	}
}

func MiddlewareActionGroupResource() schema.GroupResource {
	return schema.GroupResource{
		Group:    MiddlewareActionGroupVersionKind().Group,
		Resource: MiddlewareActionGroupVersionKind().Kind,
	}
}

// CreateMiddlewareAction creates a MiddlewareAction.
func CreateMiddlewareAction(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	// First check if it already exists
	_, err := GetMiddleware(ctx, cli, m.Name, m.Namespace)
	if err == nil {
		return errors.NewAlreadyExists(MiddlewareGroupResource(), m.Name)
	}
	return cli.Create(ctx, m)
}

func DeleteMiddlewareAction(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	return cli.Delete(ctx, m)
}

func UpdateMiddlewareAction(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		now, err := GetMiddlewareAction(ctx, cli, m.Name, m.Namespace)
		if err != nil {
			return err
		}
		now.Spec = m.Spec
		return cli.Update(ctx, now)
	})
}

func GetMiddlewareAction(ctx context.Context, cli client.Client, name, namespace string) (*v1.MiddlewareAction, error) {
	m := new(v1.MiddlewareAction)
	err := cli.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func ListMiddlewareActions(ctx context.Context, cli client.Client, namespace string, labelsSelector client.MatchingLabels) ([]v1.MiddlewareAction, error) {
	list := new(v1.MiddlewareActionList)
	err := cli.List(ctx, list, client.InNamespace(namespace), labelsSelector)
	if err != nil {
		return nil, err
	}

	var results []v1.MiddlewareAction
	for _, item := range list.Items {
		results = append(results, item)
	}
	return results, nil
}

// UpdateMiddlewareActionStatus claims a new execution with the resourceVersion
// read during preparation. After dispatch, an unrelated metadata edit is safe
// to retry only while the same Action still has our previous receipt.
func UpdateMiddlewareActionStatus(ctx context.Context, cli client.Client, m *v1.MiddlewareAction, previous *v1.MiddlewareActionStatus) error {
	m.Status.ObservedGeneration = m.Generation
	if previous == nil {
		return cli.Status().Update(ctx, m)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := cli.Status().Update(ctx, m)
		if !errors.IsConflict(err) {
			return err
		}
		var now v1.MiddlewareAction
		reader := client.Reader(cli)
		if statusAPIReader != nil {
			reader = statusAPIReader
		}
		if readErr := reader.Get(ctx, client.ObjectKeyFromObject(m), &now); readErr != nil {
			return readErr
		}
		if now.UID == m.UID && now.Generation == m.Generation && equality.Semantic.DeepEqual(now.Status, *previous) {
			m.ResourceVersion = now.ResourceVersion
		}
		return err
	})
}
