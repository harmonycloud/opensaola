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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	smdschema "sigs.k8s.io/structured-merge-diff/v6/schema"
)

func TestDeclaredIntentSchemaPolicyHash(t *testing.T) {
	legacy := "sha256:legacy-topology"
	declared := DeclaredResourceIntentSchemaHash(legacy)
	if declared == legacy || declared != DeclaredResourceIntentSchemaHash(legacy) || declared == DeclaredResourceIntentSchemaHash("sha256:different-topology") {
		t.Fatal("declared policy must be stable and distinct from legacy/topology changes")
	}
	parsed, err := declaredTestConverter(t).ObjectToTyped(intentTestObject(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDeclaredOpaqueTopology(parsed.Schema()); err != nil {
		t.Fatal(err)
	}
	if err := validateDeclaredOpaqueTopology(&smdschema.Schema{}); err == nil {
		t.Fatal("an unknown converter representation must not silently disable name compatibility")
	}
}

func intentSchemaDocument(t *testing.T, gvk schema.GroupVersionKind, atomicMap bool) []byte {
	t.Helper()
	settings := map[string]any{
		"type": "object", "additionalProperties": map[string]any{"type": "string"},
	}
	if atomicMap {
		settings["x-kubernetes-map-type"] = "atomic"
	}
	model := map[string]any{
		"type": "object",
		"x-kubernetes-group-version-kind": []any{map[string]any{
			"group": gvk.Group, "version": gvk.Version, "kind": gvk.Kind,
		}},
		"properties": map[string]any{
			"apiVersion": map[string]any{"type": "string"},
			"kind":       map[string]any{"type": "string"},
			"spec": map[string]any{
				"type": "object", "properties": map[string]any{"settings": settings},
			},
		},
	}
	document := map[string]any{
		"openapi":    "3.0.0",
		"components": map[string]any{"schemas": map[string]any{"example.Test": model}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func intentSchemaTestMap(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()
	current, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected schema object, got %T", value)
	}
	for _, key := range path {
		current, ok = current[key].(map[string]any)
		if !ok {
			t.Fatalf("expected schema object at %s", key)
		}
	}
	return current
}

// An empty hash exercises servers exposing an unversioned document URL.
func serveIntentSchemaIndex(w http.ResponseWriter, key, hash string) {
	url := "/openapi/v3/" + key
	if hash != "" {
		url += "?hash=" + hash
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"paths":{%q:{"serverRelativeURL":%q}}}`, key, url)
}

func TestIntentSchemaResolverRefreshesMergeTopology(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Example"}
	var document atomic.Pointer[[]byte]
	initial := intentSchemaDocument(t, gvk, false)
	document.Store(&initial)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != rest.DefaultKubernetesUserAgent() {
			http.Error(w, "unexpected discovery client identity", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/openapi/v3" {
			serveIntentSchemaIndex(w, "apis/example.test/v1", fmt.Sprintf("%x", sha256.Sum256(*document.Load())))
			return
		}
		if r.URL.Query().Get("hash") == "" || r.URL.Path != "/openapi/v3/apis/example.test/v1" || r.Header.Get("Accept") != "application/json" {
			http.Error(w, "unexpected schema request", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(*document.Load())
	}))
	defer server.Close()
	resolver, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1", "kind": "Example",
		"spec": map[string]any{"settings": map[string]any{"one": "1"}},
	}}
	granular, firstHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil {
		t.Fatal(err)
	}
	typed, err := granular.ObjectToTyped(object)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := typed.ToFieldSet()
	if err != nil || !fields.Has(fieldpath.MakePathOrDie("spec", "settings", "one")) {
		t.Fatalf("granular map key is not independently addressable: fields=%v err=%v", fields, err)
	}
	cached, cachedHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil || cachedHash != firstHash || cached != granular {
		t.Fatalf("unchanged schema was not reused: hash=%s err=%v", cachedHash, err)
	}
	updated := intentSchemaDocument(t, gvk, true)
	document.Store(&updated)
	changed, secondHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("changing map topology did not change the baseline schema hash")
	}
	typed, err = changed.ObjectToTyped(object)
	if err != nil {
		t.Fatal(err)
	}
	fields, err = typed.ToFieldSet()
	if err != nil || !fields.Has(fieldpath.MakePathOrDie("spec", "settings")) || fields.Has(fieldpath.MakePathOrDie("spec", "settings", "one")) {
		t.Fatalf("new atomic map topology not used: fields=%v err=%v", fields, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("wanted one schema fetch per revision, got %d", requests.Load())
	}
	// A previously cached schema is not permission to continue after discovery
	// becomes incomplete or malformed.
	empty := []byte(`{"components":{"schemas":{}}}`)
	document.Store(&empty)
	if _, _, err := resolver.Resolve(context.Background(), gvk); err == nil {
		t.Fatal("missing current schema silently reused an old deletion topology")
	}
}

func TestIntentSchemaResolverCorePathAndMissingGVK(t *testing.T) {
	gvk := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	document := intentSchemaDocument(t, gvk, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/v3" {
			serveIntentSchemaIndex(w, "api/v1", "revision1")
			return
		}
		if r.URL.Path != "/openapi/v3/api/v1" {
			http.Error(w, "unexpected core schema request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(document)
	}))
	defer server.Close()
	resolver, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolver.Resolve(context.Background(), gvk); err != nil {
		t.Fatal(err)
	}
	gvk.Kind = "Secret"
	if _, _, err := resolver.Resolve(context.Background(), gvk); err == nil {
		t.Fatal("a missing GVK was accepted")
	}
}

func TestIntentSchemaResolverHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	resolver, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err = resolver.Resolve(ctx, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline was not propagated: %v", err)
	}
}

func TestIntentSchemaResolverHashesOnlyReachableMergeTopology(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Example"}
	var document map[string]any
	if err := json.Unmarshal(intentSchemaDocument(t, gvk, false), &document); err != nil {
		t.Fatal(err)
	}
	models := intentSchemaTestMap(t, document, "components", "schemas")
	root := intentSchemaTestMap(t, models, "example.Test")
	properties := intentSchemaTestMap(t, root, "properties")
	specProperties := intentSchemaTestMap(t, properties, "spec", "properties")
	models["example.Settings"] = specProperties["settings"]
	specProperties["settings"] = map[string]any{"$ref": "#/components/schemas/example.Settings"}
	// A business field named description must remain part of the merge model.
	specProperties["description"] = map[string]any{"type": "string"}
	models["example.Unrelated"] = map[string]any{"type": "string"}
	var served atomic.Pointer[[]byte]
	publish := func() {
		t.Helper()
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		served.Store(&raw)
	}
	publish()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/openapi/v3" {
			serveIntentSchemaIndex(w, "apis/example.test/v1", "")
			return
		}
		_, _ = w.Write(*served.Load())
	}))
	defer server.Close()
	resolver, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	first, baselineHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil {
		t.Fatal(err)
	}
	root["description"] = "new documentation"
	root["title"] = "new title"
	root["example"] = map[string]any{"spec": map[string]any{"description": "documentation example"}}
	settings := intentSchemaTestMap(t, models, "example.Settings")
	settings["description"] = "changed referenced documentation"
	intentSchemaTestMap(t, settings, "additionalProperties")["minLength"] = 2
	models["example.Unrelated"] = map[string]any{"type": "object", "x-kubernetes-map-type": "atomic"}
	publish()
	second, unchangedHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedHash != baselineHash {
		t.Fatal("documentation, validation-only, or unrelated model changes invalidated the baseline")
	}
	if first == second {
		t.Fatal("converter cache ignored a change to the current schema document")
	}
	specProperties["description"] = map[string]any{"type": "boolean"}
	publish()
	_, businessFieldHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil || businessFieldHash == baselineHash {
		t.Fatalf("type of business field named description was ignored: %v", err)
	}
	specProperties["description"] = map[string]any{"type": "string"}
	settings["x-kubernetes-map-type"] = "atomic"
	publish()
	_, referencedHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil || referencedHash == baselineHash {
		t.Fatalf("referenced merge topology change was ignored: %v", err)
	}
	delete(models, "example.Settings")
	publish()
	if _, _, err := resolver.Resolve(context.Background(), gvk); err == nil {
		t.Fatal("a missing referenced merge type was accepted")
	}
	models["example.Settings"] = settings
	items := map[string]any{
		"type": "array", "x-kubernetes-list-type": "map", "x-kubernetes-list-map-keys": []any{"name"},
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"name": map[string]any{"type": "string"}, "scope": map[string]any{"type": "string"},
		}},
	}
	specProperties["items"] = items
	publish()
	_, oneKeyHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil {
		t.Fatal(err)
	}
	items["x-kubernetes-list-map-keys"] = []any{"name", "scope"}
	publish()
	_, twoKeyHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil || oneKeyHash == twoKeyHash {
		t.Fatalf("associative list key change was ignored: %v", err)
	}
	intentSchemaTestMap(t, items, "items", "properties", "scope")["default"] = "global"
	publish()
	_, keyDefaultHash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil || keyDefaultHash == twoKeyHash {
		t.Fatalf("associative list key default change was ignored: %v", err)
	}
}

func TestIntentSchemaResolverOpenAPIV2Compatibility(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Example"}
	var document map[string]any
	if err := json.Unmarshal(intentSchemaDocument(t, gvk, false), &document); err != nil {
		t.Fatal(err)
	}
	models := intentSchemaTestMap(t, document, "components", "schemas")
	root := intentSchemaTestMap(t, models, "example.Test")
	properties := intentSchemaTestMap(t, root, "properties", "spec", "properties")
	models["example.Settings"] = properties["settings"]
	properties["settings"] = map[string]any{"$ref": "#/components/schemas/example.Settings"}
	specModel := intentSchemaTestMap(t, root, "properties", "spec")
	specModel["default"] = map[string]any{}
	v3, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	// v2 drops object-reference defaults which v3 retains. They do not change
	// merge topology, unlike scalar associative-list key defaults tested above.
	delete(specModel, "default")
	v2, err := json.Marshal(map[string]any{"swagger": "2.0", "definitions": models})
	if err != nil {
		t.Fatal(err)
	}
	v2 = []byte(strings.ReplaceAll(string(v2), "#/components/schemas/", "#/definitions/"))
	var responseStatus atomic.Int32
	responseStatus.Store(http.StatusOK)
	var v2Requests atomic.Int32
	var rootRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openapi/v3/apis/example.test/v1":
			if responseStatus.Load() != http.StatusOK {
				http.Error(w, "OpenAPI v3 unavailable", int(responseStatus.Load()))
				return
			}
			_, _ = w.Write(v3)
		case "/openapi/v2":
			v2Requests.Add(1)
			_, _ = w.Write(v2)
		case "/openapi/v3":
			rootRequests.Add(1)
			if responseStatus.Load() == http.StatusNotFound {
				http.NotFound(w, r)
			} else {
				serveIntentSchemaIndex(w, "apis/example.test/v1", "")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	resolver, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, v3Hash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil {
		t.Fatal(err)
	}
	responseStatus.Store(http.StatusNotFound)
	converter, v2Hash, err := resolver.Resolve(context.Background(), gvk)
	if err != nil {
		t.Fatal(err)
	}
	if v3Hash != v2Hash || v2Requests.Load() != 1 || rootRequests.Load() != 2 {
		t.Fatal("equivalent v2 and v3 documents did not produce the same merge topology")
	}
	_, err = converter.ObjectToTyped(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1", "kind": "Example",
		"spec": map[string]any{"settings": map[string]any{"one": "1"}},
	}})
	if err != nil {
		t.Fatalf("v2 reference could not be converted: %v", err)
	}
	responseStatus.Store(http.StatusForbidden)
	if _, _, err := resolver.Resolve(context.Background(), gvk); err == nil {
		t.Fatal("forbidden v3 discovery should not permit fallback")
	}
	if v2Requests.Load() != 1 || rootRequests.Load() != 3 {
		t.Fatal("a v3 authorization failure was hidden by v2 fallback")
	}
}

func TestIntentSchemaResolverWaitsForNewCRDDiscovery(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Example"}
	document := intentSchemaDocument(t, gvk, false)
	var published atomic.Bool
	var rootRequests atomic.Int32
	var v2Requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openapi/v3/apis/example.test/v1":
			if !published.Load() {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(document)
		case "/openapi/v3":
			rootRequests.Add(1)
			if published.Load() {
				serveIntentSchemaIndex(w, "apis/example.test/v1", "revision1")
			} else {
				_, _ = w.Write([]byte(`{"paths":{}}`))
			}
		case "/openapi/v2":
			v2Requests.Add(1)
			http.Error(w, "unexpected v2 fallback", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	resolver, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolver.Resolve(context.Background(), gvk); !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("new CRD discovery gap must return a retryable error, got %v", err)
	}
	if rootRequests.Load() != 1 || v2Requests.Load() != 0 {
		t.Fatalf("GV-only 404 must check the root without falling back: roots=%d v2=%d", rootRequests.Load(), v2Requests.Load())
	}
	published.Store(true)
	if _, _, err := resolver.Resolve(context.Background(), gvk); err != nil {
		t.Fatalf("newly published CRD did not recover: %v", err)
	}
	if v2Requests.Load() != 0 {
		t.Fatal("CRD discovery recovery should never consult v2")
	}
}

func TestIntentSchemaPreloadSharesInflightDocument(t *testing.T) {
	core := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	apps := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}
	coreDoc, appsDoc := intentSchemaDocument(t, core, false), intentSchemaDocument(t, apps, false)
	started, proceed := make(chan struct{}), make(chan struct{})
	var coreReads, appsReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/openapi/v3":
			_, _ = w.Write([]byte(`{"paths":{"api/v1":{"serverRelativeURL":"/proxy/openapi/v3/api/v1?hash=core"},"apis/apps/v1":{"serverRelativeURL":"/openapi/v3/apis/apps/v1?hash=apps"}}}`))
		case "/proxy/openapi/v3/api/v1":
			if coreReads.Add(1) == 1 {
				close(started)
			}
			select {
			case <-proceed:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(coreDoc)
		case "/proxy/openapi/v3/apis/apps/v1":
			appsReads.Add(1)
			_, _ = w.Write(appsDoc)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer func() {
		select {
		case <-proceed:
		default:
			close(proceed)
		}
	}()
	r, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL + "/proxy"})
	if err != nil {
		t.Fatal(err)
	}
	preloaded := make(chan error, 1)
	go func() { preloaded <- r.Start(t.Context()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("preload did not fetch core/v1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := r.Resolve(ctx, core); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting lookup ignored deadline: %v", err)
	}
	const writers = 8
	results := make(chan error, writers)
	for range writers {
		go func() { _, _, err := r.Resolve(t.Context(), core); results <- err }()
	}
	close(proceed)
	if err := <-preloaded; err != nil {
		t.Fatal(err)
	}
	for range writers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if coreReads.Load() != 1 || appsReads.Load() != 1 {
		t.Fatalf("preload and concurrent lookups downloaded core=%d apps=%d", coreReads.Load(), appsReads.Load())
	}
}

func TestIntentSchemaPreloadFailureAndStaleRedirect(t *testing.T) {
	core := schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	apps := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}
	coreDoc, appsDoc := intentSchemaDocument(t, core, false), intentSchemaDocument(t, apps, false)
	var revision, appsReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v3":
			if revision.Load() == 3 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `{"paths":{"api/v1":{"serverRelativeURL":"/openapi/v3/api/v1?hash=%d"},"apis/apps/v1":{"serverRelativeURL":"/openapi/v3/apis/apps/v1?hash=apps"}}}`, revision.Load())
		case "/openapi/v3/api/v1":
			switch r.URL.Query().Get("hash") {
			case "0":
				http.NotFound(w, r)
			case "1":
				revision.Store(2)
				http.Redirect(w, r, "/openapi/v3/api/v1?hash=2", http.StatusMovedPermanently)
			case "2":
				_, _ = w.Write(coreDoc)
			}
		case "/openapi/v3/apis/apps/v1":
			appsReads.Add(1)
			_, _ = w.Write(appsDoc)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	r, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(t.Context()); err != nil || appsReads.Load() != 1 {
		t.Fatalf("core preload failure blocked apps: %v", err)
	}
	if _, _, err := r.Resolve(t.Context(), core); !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("advertised missing schema: %v", err)
	}
	revision.Store(1)
	if _, _, err := r.Resolve(t.Context(), core); err == nil {
		t.Fatal("redirected schema was accepted under its obsolete hash")
	}
	if _, _, err := r.Resolve(t.Context(), core); err != nil {
		t.Fatal(err)
	}
	revision.Store(3)
	if _, _, err := r.Resolve(t.Context(), core); !apierrors.IsForbidden(err) {
		t.Fatalf("cached schema concealed an index error: %v", err)
	}
}

func TestIntentSchemaConcurrentRevisionChange(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Example"}
	oldDoc, newDoc := intentSchemaDocument(t, gvk, false), intentSchemaDocument(t, gvk, true)
	releaseOld := make(chan struct{})
	started := make(chan struct{})
	var revision, downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/v3" {
			serveIntentSchemaIndex(w, "apis/example.test/v1", fmt.Sprint(revision.Load()))
			return
		}
		downloads.Add(1)
		if r.URL.Query().Get("hash") == "0" {
			close(started)
			select {
			case <-releaseOld:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(oldDoc)
			return
		}
		_, _ = w.Write(newDoc)
	}))
	defer server.Close()
	r, err := NewIntentSchemaResolver(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func() <-chan string {
		done := make(chan string, 1)
		go func() {
			_, hash, err := r.Resolve(t.Context(), gvk)
			if err != nil {
				t.Errorf("resolve schema: %v", err)
			}
			done <- hash
		}()
		return done
	}
	first := resolve()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(releaseOld)
		<-first
		t.Fatal("old fetch did not start")
	}
	revision.Store(1)
	second := resolve()
	close(releaseOld)
	oldHash, newHash := <-first, <-second
	_, latest, err := r.Resolve(t.Context(), gvk)
	if err != nil || latest != newHash || oldHash == newHash || downloads.Load() != 2 {
		t.Fatalf("revision change: old=%q new=%q cached=%q downloads=%d err=%v", oldHash, newHash, latest, downloads.Load(), err)
	}
}
