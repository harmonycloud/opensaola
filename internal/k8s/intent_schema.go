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
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/handler3"
	"k8s.io/kube-openapi/pkg/schemaconv"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/controller-runtime/pkg/log"
	smdschema "sigs.k8s.io/structured-merge-diff/v6/schema"
)

const intentSchemaTimeout = 15 * time.Second

// DeclaredResourceIntentSchemaHash binds the API topology to the platform's
// declaration-first semantics. Resolve deliberately keeps returning the legacy
// topology hash so version-one records can be validated and migrated without
// interpreting their deletion history under the new rules. The topology hash
// already includes the named __untyped_deduced_ references that select opaque
// list compatibility; typed atomic arrays use different references.
func DeclaredResourceIntentSchemaHash(legacyHash string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("opensaola-declared-intent-v2\x00"+legacyHash)))
}

// IntentSchemaResolver returns the API server's current merge topology. The hash
// binds a saved intent to that topology; a mismatch must not be guessed through.
type IntentSchemaResolver interface {
	Resolve(context.Context, schema.GroupVersionKind) (managedfields.TypeConverter, string, error)
}

type intentSchemaCacheEntry struct {
	version   string // discovery URL, or a body digest for unversioned documents
	converter managedfields.TypeConverter
	topology  *smdschema.Schema
	roots     map[schema.GroupVersionKind][]string
	hashes    map[schema.GroupVersionKind]string
}

type intentSchemaGroup struct {
	users int // protected by the resolver mutex
	gate  chan struct{}
	entry *intentSchemaCacheEntry
}

// LiveIntentSchemaResolver provides schema lookup and manager startup preloading.
type LiveIntentSchemaResolver struct {
	client rest.Interface
	mu     sync.Mutex
	groups map[schema.GroupVersion]*intentSchemaGroup
}

// NewIntentSchemaResolver creates a cache scoped to one API server.
func NewIntentSchemaResolver(config *rest.Config) (*LiveIntentSchemaResolver, error) {
	if config == nil {
		return nil, fmt.Errorf("intent schema resolver requires a REST config")
	}
	config = rest.CopyConfig(config)
	if config.Timeout <= 0 || config.Timeout > intentSchemaTimeout {
		config.Timeout = intentSchemaTimeout
	}
	// NewDiscoveryClientForConfig normally sets this before constructing
	// the transport. Keep the same identity in API server audit records.
	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}
	// A stale discovery hash redirects to the new revision. Never store that
	// response under the obsolete URL; the next lookup reads the current index.
	discoveryHTTP := *httpClient
	discoveryHTTP.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if via[0].URL.Query().Get("hash") != "" {
			return apierrors.NewServiceUnavailable("OpenAPI document changed during lookup; retry discovery")
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many discovery redirects")
		}
		return nil
	}
	client, err := discovery.NewDiscoveryClientForConfigAndClient(config, &discoveryHTTP)
	if err != nil {
		return nil, fmt.Errorf("create intent schema discovery client: %w", err)
	}
	return &LiveIntentSchemaResolver{client: client.RESTClient(), groups: make(map[schema.GroupVersion]*intentSchemaGroup)}, nil
}

// NeedLeaderElection allows each replica to warm its own read-only cache at startup.
func (*LiveIntentSchemaResolver) NeedLeaderElection() bool { return false }

// Start preloads common native schemas alongside the manager's controllers.
// On-demand lookups use the same cache and retry failed preloads independently.
func (r *LiveIntentSchemaResolver) Start(ctx context.Context) error {
	for _, gvk := range []schema.GroupVersionKind{
		{Version: "v1", Kind: "ConfigMap"},
		{Group: "apps", Version: "v1", Kind: "StatefulSet"},
	} {
		if ctx.Err() != nil {
			break
		}
		if _, _, err := r.Resolve(ctx, gvk); err != nil && ctx.Err() == nil {
			log.FromContext(ctx).Error(err, "OpenAPI preload failed; will retry on demand", "groupVersion", gvk.GroupVersion().String())
		}
	}
	return nil
}

// Serialize discovery and publication within one group so a delayed old
// response cannot replace a newer converter. Failed lookups for unknown GVs
// leave no cache entries. A canceled waiter does not cancel the active fetch.
func (r *LiveIntentSchemaResolver) acquire(ctx context.Context, gv schema.GroupVersion) (*intentSchemaGroup, func(), error) {
	r.mu.Lock()
	group := r.groups[gv]
	if group == nil {
		group = &intentSchemaGroup{gate: make(chan struct{}, 1)}
		r.groups[gv] = group
	}
	group.users++
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		group.users--
		if group.users == 0 && group.entry == nil {
			delete(r.groups, gv)
		}
	}
	select {
	case group.gate <- struct{}{}:
		return group, func() {
			<-group.gate
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, nil, ctx.Err()
	}
}

func (r *LiveIntentSchemaResolver) Resolve(ctx context.Context, gvk schema.GroupVersionKind) (managedfields.TypeConverter, string, error) {
	if gvk.Version == "" || gvk.Kind == "" {
		return nil, "", fmt.Errorf("intent schema requires a complete GVK: %s", gvk)
	}
	ctx, cancel := context.WithTimeout(ctx, intentSchemaTimeout)
	defer cancel()
	group, unlock, err := r.acquire(ctx, gvk.GroupVersion())
	if err != nil {
		return nil, "", err
	}
	defer unlock()

	// client-go's OpenAPIV3 helpers use context.TODO. Keep caller deadlines.
	raw, err := r.client.Get().AbsPath("/openapi/v3").SetHeader("Accept", "application/json").DoRaw(ctx)
	if apierrors.IsNotFound(err) {
		// v2 has a single cluster-wide document; retain only one converter.
		v2, unlockV2, err := r.acquire(ctx, schema.GroupVersion{})
		if err != nil {
			return nil, "", err
		}
		defer unlockV2()
		return v2.resolve(ctx, gvk, r.client.Get().AbsPath("/openapi/v2"), "", true)
	}
	if err != nil {
		return nil, "", fmt.Errorf("read OpenAPI index for %s: %w", gvk, err)
	}
	var index handler3.OpenAPIV3Discovery
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, "", fmt.Errorf("decode OpenAPI index: %w", err)
	}
	if index.Paths == nil {
		return nil, "", fmt.Errorf("OpenAPI index is missing paths")
	}
	key := "api/" + gvk.Version
	if gvk.Group != "" {
		key = "apis/" + gvk.GroupVersion().String()
	}
	item, ok := index.Paths[key]
	if !ok {
		return nil, "", apierrors.NewServiceUnavailable(fmt.Sprintf("OpenAPI v3 schema for %s is not published yet; retry after discovery catches up", gvk))
	}
	locator, err := url.ParseRequestURI(item.ServerRelativeURL)
	if err != nil || !strings.HasPrefix(item.ServerRelativeURL, "/") || strings.HasPrefix(item.ServerRelativeURL, "//") {
		return nil, "", fmt.Errorf("invalid OpenAPI document URL for %s", gvk)
	}
	// Discovery behind a proxy may include the configured client prefix.
	prefix := strings.TrimSuffix(r.client.Get().AbsPath("/").URL().Path, "/")
	if prefix != "" && strings.HasPrefix(locator.Path, prefix+"/") {
		locator.Path = strings.TrimPrefix(locator.Path, prefix)
	}
	request := r.client.Get().RequestURI(locator.RequestURI())
	version := ""
	if locator.Query().Get("hash") != "" {
		version = item.ServerRelativeURL
	}
	return group.resolve(ctx, gvk, request, version, false)
}

func (g *intentSchemaGroup) resolve(ctx context.Context, gvk schema.GroupVersionKind, request *rest.Request, version string, v2 bool) (managedfields.TypeConverter, string, error) {
	if version != "" && g.entry != nil && g.entry.version == version {
		return g.entry.lookup(gvk)
	}
	raw, err := request.SetHeader("Accept", "application/json").DoRaw(ctx)
	if !v2 && apierrors.IsNotFound(err) {
		return nil, "", apierrors.NewServiceUnavailable(fmt.Sprintf("OpenAPI v3 schema for %s is not published yet; retry after discovery catches up", gvk))
	}
	if err != nil {
		return nil, "", fmt.Errorf("read intent schema for %s: %w", gvk, err)
	}
	if version == "" {
		version = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		if g.entry != nil && g.entry.version == version {
			return g.entry.lookup(gvk)
		}
	}
	entry, err := parseIntentSchema(raw, version, v2)
	if err != nil {
		return nil, "", fmt.Errorf("parse intent schema for %s: %w", gvk, err)
	}
	converter, hash, err := entry.lookup(gvk)
	if err == nil {
		g.entry = entry
	}
	return converter, hash, err
}

func (e *intentSchemaCacheEntry) lookup(gvk schema.GroupVersionKind) (managedfields.TypeConverter, string, error) {
	if hash, ok := e.hashes[gvk]; ok {
		return e.converter, hash, nil
	}
	roots := e.roots[gvk]
	if len(roots) != 1 {
		return nil, "", fmt.Errorf("expected exactly one OpenAPI model for %s, found %d", gvk, len(roots))
	}
	hash, err := intentMergeTopologyHash(e.topology, roots[0])
	if err != nil {
		return nil, "", err
	}
	e.hashes[gvk] = hash
	return e.converter, hash, nil
}

func parseIntentSchema(raw []byte, version string, v2 bool) (*intentSchemaCacheEntry, error) {
	var document struct {
		Components struct {
			Schemas map[string]*spec.Schema `json:"schemas"`
		} `json:"components"`
		Definitions map[string]*spec.Schema `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	models := document.Components.Schemas
	if v2 {
		models = document.Definitions
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("OpenAPI schemas missing")
	}
	roots, err := intentSchemaRoots(models)
	if err != nil {
		return nil, err
	}
	converter, err := managedfields.NewTypeConverter(models, false)
	if err != nil {
		return nil, fmt.Errorf("convert intent schema: %w", err)
	}
	topology, err := schemaconv.ToSchemaFromOpenAPI(models, false)
	if err != nil {
		return nil, fmt.Errorf("convert intent topology: %w", err)
	}
	if err := validateDeclaredOpaqueTopology(topology); err != nil {
		return nil, err
	}
	for i := range topology.Types {
		sortIntentSchemaFields(&topology.Types[i].Atom)
	}
	return &intentSchemaCacheEntry{version: version, converter: converter, topology: topology, roots: roots, hashes: make(map[schema.GroupVersionKind]string)}, nil
}

// The converter's named untyped reference is the provenance marker used by the
// declaration merger. Fail closed if a dependency changes this representation,
// instead of silently treating opaque named lists as whole-value replacements.
func validateDeclaredOpaqueTopology(topology *smdschema.Schema) error {
	definition, ok := topology.FindNamedType("__untyped_deduced_")
	if !ok || definition.Scalar == nil || *definition.Scalar != smdschema.Untyped || definition.Map == nil || definition.List == nil ||
		definition.Map.ElementRelationship != smdschema.Separable || definition.List.ElementRelationship != smdschema.Atomic ||
		definition.Map.ElementType.NamedType == nil || *definition.Map.ElementType.NamedType != "__untyped_deduced_" ||
		definition.List.ElementType.NamedType == nil || *definition.List.ElementType.NamedType != "__untyped_atomic_" {
		return fmt.Errorf("unsupported untyped schema representation for declared resource merging")
	}
	return nil
}

func intentSchemaRoots(models map[string]*spec.Schema) (map[schema.GroupVersionKind][]string, error) {
	roots := make(map[schema.GroupVersionKind][]string)
	for name, model := range models {
		if model == nil {
			return nil, fmt.Errorf("OpenAPI contains a null schema for %s", name)
		}
		extension, exists := model.Extensions["x-kubernetes-group-version-kind"]
		if !exists {
			continue
		}
		raw, err := json.Marshal(extension)
		if err != nil {
			return nil, fmt.Errorf("encode intent schema GVK: %w", err)
		}
		var kinds []schema.GroupVersionKind
		if err := json.Unmarshal(raw, &kinds); err != nil {
			return nil, fmt.Errorf("decode intent schema GVK: %w", err)
		}
		for _, candidate := range kinds {
			roots[candidate] = append(roots[candidate], name)
		}
	}
	return roots, nil
}

func sortIntentSchemaFields(atom *smdschema.Atom) {
	if atom.Map != nil {
		sort.Slice(atom.Map.Fields, func(i, j int) bool { return atom.Map.Fields[i].Name < atom.Map.Fields[j].Name })
		for i := range atom.Map.Fields {
			// OpenAPI v3 retains defaults on object references (metadata: {},
			// for example) that v2 drops. SMD only uses defaults for scalar
			// associative-list keys; collection defaults cannot affect its merge.
			switch atom.Map.Fields[i].Default.(type) {
			case map[string]any, []any:
				atom.Map.Fields[i].Default = nil
			}
			sortIntentSchemaFields(&atom.Map.Fields[i].Type.Inlined)
		}
		sortIntentSchemaFields(&atom.Map.ElementType.Inlined)
	}
	if atom.List != nil {
		sortIntentSchemaFields(&atom.List.ElementType.Inlined)
	}
}

// Hash the same type model used by the merger, limited to the requested GVK's
// reachable types. Documentation and unrelated Kinds cannot invalidate an intent.
// Validation-only changes remain enforced by the API server's dry-run validation.
func intentMergeTopologyHash(topology *smdschema.Schema, root string) (string, error) {
	closure := make(map[string]smdschema.TypeDef)
	var visitNamed func(string) error
	var visitAtom func(smdschema.Atom) error
	visitRef := func(ref smdschema.TypeRef) error {
		if ref.NamedType != nil {
			return visitNamed(*ref.NamedType)
		}
		return visitAtom(ref.Inlined)
	}
	visitAtom = func(atom smdschema.Atom) error {
		if atom.Map != nil {
			for _, field := range atom.Map.Fields {
				if err := visitRef(field.Type); err != nil {
					return err
				}
			}
			if err := visitRef(atom.Map.ElementType); err != nil {
				return err
			}
		}
		if atom.List != nil {
			return visitRef(atom.List.ElementType)
		}
		return nil
	}
	visitNamed = func(name string) error {
		if _, ok := closure[name]; ok {
			return nil
		}
		definition, ok := topology.FindNamedType(name)
		if !ok {
			return fmt.Errorf("intent merge topology references missing type %s", name)
		}
		closure[name] = definition
		return visitAtom(definition.Atom)
	}
	if err := visitNamed(root); err != nil {
		return "", err
	}
	// SMD's model has YAML inline tags and anonymous Map/List members whose
	// promoted JSON field names collide. JSON-marshaling it directly silently
	// loses merge relationships. Round-trip its supported YAML shape first.
	model, err := yaml.Marshal(closure)
	if err != nil {
		return "", fmt.Errorf("encode intent merge topology: %w", err)
	}
	var normalized any
	if err := yaml.Unmarshal(model, &normalized); err != nil {
		return "", fmt.Errorf("normalize intent merge topology: %w", err)
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("hash intent merge topology: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}
