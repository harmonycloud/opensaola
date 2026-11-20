//go:build envtest

/*
Copyright 2026 The OpenSaola Authors.

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

package middlewareconfiguration

import (
	"context"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/k8s/kubeclient"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestRetireInventoryAfterCRDVersionChange(t *testing.T) {
	original := apiextensionsv1.CustomResourceDefinitionVersion{
		Name: "v1alpha1", Served: true, Storage: true,
		Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object"}},
	}
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "items.inventory.test"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "inventory.test", Scope: apiextensionsv1.NamespaceScoped,
			Names:    apiextensionsv1.CustomResourceDefinitionNames{Plural: "items", Singular: "item", Kind: "Item", ListKind: "ItemList"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{original},
		},
	}
	environment := &envtest.Environment{CRDs: []*apiextensionsv1.CustomResourceDefinition{crd}}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	ctx := t.Context()
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		t.Fatal(err)
	}
	mapper, err := kubeclient.NewManagerRESTMapper(config, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := client.New(config, client.Options{Scheme: scheme, HTTPClient: httpClient, Mapper: mapper})
	if err != nil {
		t.Fatal(err)
	}
	owner := inventoryTestOwner()
	var records []v1.RenderedConfigurationResource
	oldGVK := schema.GroupVersionKind{Group: crd.Spec.Group, Version: original.Name, Kind: crd.Spec.Names.Kind}
	newGVK := oldGVK
	newGVK.Version = "v1"
	for _, name := range []string{"original", "same-name-replacement"} {
		record := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
		record.Group, record.Version, record.Kind = oldGVK.Group, oldGVK.Version, oldGVK.Kind
		record.Namespace, record.Name = "default", name
		resource := inventoryLiveObject(owner, record, "", false)
		if err := cli.Create(ctx, resource); err != nil {
			t.Fatal(err)
		}
		record.ResourceUID = resource.GetUID()
		records = append(records, record)
	}
	owner.Status.RenderedConfigurationResources = records
	if err := cli.Get(ctx, client.ObjectKeyFromObject(crd), crd); err != nil {
		t.Fatal(err)
	}
	next := original
	next.Name, next.Storage = newGVK.Version, false
	crd.Spec.Versions[0].Served = false
	crd.Spec.Versions = append(crd.Spec.Versions, next)
	if err := cli.Update(ctx, crd); err != nil {
		t.Fatal(err)
	}
	// Observe the version change independently without teaching the manager's mapper about v1.
	fresh, err := client.New(config, client.Options{Scheme: scheme, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	err = wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		_, oldErr := k8s.GetCustomResource(ctx, cli, records[0].Name, "default", oldGVK)
		live, newErr := k8s.GetCustomResource(ctx, fresh, records[0].Name, "default", newGVK)
		return configurationVersionUnavailable(oldErr) && newErr == nil && live.GetUID() == records[0].ResourceUID, nil
	})
	if err != nil {
		t.Fatalf("CRD did not switch served versions: %v", err)
	}
	replacementRecord := records[1]
	resource, err := k8s.GetCustomResource(ctx, fresh, replacementRecord.Name, "default", newGVK)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Delete(ctx, resource); err != nil {
		t.Fatal(err)
	}
	replacement := inventoryLiveObject(owner, replacementRecord, "", false)
	replacement.SetGroupVersionKind(newGVK)
	if err := fresh.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.GetUID() == replacementRecord.ResourceUID {
		t.Fatal("replacement reused the old UID")
	}
	cfg := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: records[0].ConfigurationName}}
	handled, err := retireRecordedConfiguration(ctx, cli, owner, cfg)
	if !handled || err != nil {
		t.Fatalf("retire old inventory: handled=%t err=%v", handled, err)
	}
	if _, err := k8s.GetCustomResource(ctx, fresh, records[0].Name, "default", newGVK); !apierrors.IsNotFound(err) {
		t.Fatalf("published UID %s still exists via %s: %v", records[0].ResourceUID, newGVK, err)
	}
	retained := &unstructured.Unstructured{}
	retained.SetGroupVersionKind(newGVK)
	if err := fresh.Get(ctx, client.ObjectKeyFromObject(replacement), retained); err != nil {
		t.Fatal(err)
	}
	if retained.GetUID() != replacement.GetUID() {
		t.Fatalf("replacement changed: %s", retained.GetUID())
	}
	t.Logf("%s to %s: removed published UID %s; retained replacement UID %s", oldGVK, newGVK, records[0].ResourceUID, retained.GetUID())
}
