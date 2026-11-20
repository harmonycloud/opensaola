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

package k8s

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type countSchemaRequest func(*http.Request) (*http.Response, error)

func (f countSchemaRequest) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIntentSchemaPreloadAndCRDRefreshAPI(t *testing.T) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	cfg = rest.CopyConfig(cfg)
	var core, apps, indices atomic.Int32
	cfg.Wrap(func(base http.RoundTripper) http.RoundTripper {
		return countSchemaRequest(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/openapi/v3/api/v1":
				core.Add(1)
			case "/openapi/v3/apis/apps/v1":
				apps.Add(1)
			case "/openapi/v3":
				indices.Add(1)
			}
			return base.RoundTrip(req)
		})
	})
	r, err := NewIntentSchemaResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	baseIndex := indices.Load()
	for _, kind := range []string{"ConfigMap", "Secret", "Service", "ConfigMap", "Secret", "Service", "ConfigMap", "Secret", "Service", "ConfigMap"} {
		if _, _, err := r.Resolve(ctx, schema.GroupVersionKind{Version: "v1", Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := r.Resolve(ctx, schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}); err != nil {
		t.Fatal(err)
	}
	if core.Load() != 1 || apps.Load() != 1 || indices.Load()-baseIndex != 11 {
		t.Fatalf("core documents=%d apps documents=%d write-time index requests=%d", core.Load(), apps.Load(), indices.Load()-baseIndex)
	}

	// Install after preloading, then change only one Kind in the same GV.
	crd := typedIntentDeletionCRD()
	list := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["volumes"]
	atomicList := list.DeepCopy()
	atomicList.XListMapKeys = nil
	atomicType := "atomic"
	atomicList.XListType = &atomicType
	crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["volumes"] = *atomicList
	peer := crd.DeepCopy()
	peer.Name = "peers." + typedDeletionGVK.Group
	peer.Spec.Names = apiextensionsv1.CustomResourceDefinitionNames{Plural: "peers", Singular: "peer", Kind: "Peer"}
	if _, err := envtest.InstallCRDs(cfg, envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{crd, peer}}); err != nil {
		t.Fatal(err)
	}
	peerGVK := typedDeletionGVK.GroupVersion().WithKind("Peer")
	// CRD Established precedes OpenAPI publication; the two kinds may
	// appear in separate revisions of the same group document.
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		for _, kind := range []schema.GroupVersionKind{typedDeletionGVK, peerGVK} {
			if _, _, err := r.Resolve(ctx, kind); err != nil {
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	first, hash, err := r.Resolve(ctx, typedDeletionGVK)
	if err != nil {
		t.Fatal(err)
	}
	_, peerHash, err := r.Resolve(ctx, peerGVK)
	if err != nil {
		t.Fatal(err)
	}
	object := func(a, b string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": typedDeletionGVK.GroupVersion().String(), "kind": typedDeletionGVK.Kind, "spec": map[string]any{"volumes": []any{map[string]any{"name": a}, map[string]any{"name": b}}}}}
	}
	declared, reordered := object("a", "b"), object("b", "a")
	if err := ValidateDeclaredResourceIntent(first, declared, reordered); err == nil {
		t.Fatal("atomic list accepted reordered declarations")
	}
	ext, err := apiextensionsclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	crds := ext.ApiextensionsV1().CustomResourceDefinitions()
	live, err := crds.Get(ctx, crd.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	live.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["volumes"] = list
	if _, err := crds.Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		current, changed, err := r.Resolve(ctx, typedDeletionGVK)
		if err != nil || changed == hash {
			return false, nil
		}
		return true, ValidateDeclaredResourceIntent(current, declared, reordered)
	}); err != nil {
		t.Fatalf("updated keyed list did not match by name: %v", err)
	}
	if _, got, err := r.Resolve(ctx, peerGVK); err != nil || got != peerHash {
		t.Fatalf("unrelated Kind's topology changed: hash=%q err=%v", got, err)
	}
}
