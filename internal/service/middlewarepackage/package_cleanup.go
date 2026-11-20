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

package middlewarepackage

import (
	"context"
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func handlePackageResources(ctx context.Context, cli client.Client, act consts.HandleAction, mp *v1.MiddlewarePackage) error {
	if act != consts.HandleActionDelete {
		return fmt.Errorf("unsupported package definition action %q", act)
	}
	definitions, err := cleanupDefinitions(ctx, cli, mp)
	if err != nil {
		return err
	}
	var firstErr error
	for _, def := range definitions {
		live, err := cli.Scheme().New(def.GroupVersionKind())
		if err != nil {
			return err
		}
		object, ok := live.(client.Object)
		if !ok {
			return fmt.Errorf("invalid package definition %s", def.GroupVersionKind())
		}
		if err := deletePackageResource(ctx, cli, mp, def.GetName(), object); err != nil {
			log.FromContext(ctx).Error(err, "failed to clean up package definition", "package", mp.Name, "name", def.GetName())
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// A rejected or damaged archive can still have a finalizer. Enumerate live
// metadata if its manifest cannot supply the cleanup inventory.
func cleanupDefinitions(ctx context.Context, cli client.Client, mp *v1.MiddlewarePackage) ([]*unstructured.Unstructured, error) {
	pkg, archiveErr := packages.Get(ctx, cli, mp.Name)
	if archiveErr == nil {
		var defs []*unstructured.Unstructured
		defs, archiveErr = pkg.Definitions()
		if archiveErr == nil {
			return defs, nil
		}
	}
	log.FromContext(ctx).Info("reading live package definition inventory for uninstall", "package", mp.Name, "archiveError", archiveErr.Error())
	reader := k8s.NewManagedResourceWriter(cli).Reader
	var defs []*unstructured.Unstructured
	for _, kind := range []string{"MiddlewareBaseline", "MiddlewareOperatorBaseline", "MiddlewareActionBaseline", "MiddlewareConfiguration"} {
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(v1.GroupVersion.WithKind(kind + "List"))
		if err := reader.List(ctx, list); err != nil {
			return nil, fmt.Errorf("list %s metadata for package %s: %w", kind, mp.Name, err)
		}
		for i := range list.Items {
			item := &list.Items[i]
			if refersToPackage(item, mp) {
				defs = append(defs, definitionKey{Kind: kind, Name: item.Name}.object())
			}
		}
	}
	return defs, nil
}

// Match partial evidence as well: inconsistent labels or owner references must
// reach the normal fail-closed ownership check before the catalog is deleted.
func refersToPackage(item *metav1.PartialObjectMetadata, mp *v1.MiddlewarePackage) bool {
	if item.Labels[v1.LabelPackageName] == mp.Name {
		return true
	}
	for _, owner := range item.OwnerReferences {
		if (mp.UID != "" && owner.UID == mp.UID) || (owner.APIVersion == v1.GroupVersion.String() && owner.Kind == "MiddlewarePackage" && owner.Name == mp.Name) {
			return true
		}
	}
	return false
}
