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
	"sort"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/schemaconv"
	"k8s.io/kube-openapi/pkg/validation/spec"
	smdschema "sigs.k8s.io/structured-merge-diff/v6/schema"
)

const intentSchemaTimeout = 15 * time.Second

// IntentSchemaResolver returns the API server's current merge topology. The hash
// binds a saved intent to that topology; a mismatch must not be guessed through.
type IntentSchemaResolver interface {
	Resolve(context.Context, schema.GroupVersionKind) (managedfields.TypeConverter, string, error)
}

type intentSchemaCacheEntry struct {
	hash      string
	converter managedfields.TypeConverter
	topology  *smdschema.Schema
}

type liveIntentSchemaResolver struct {
	client rest.Interface
	mu     sync.Mutex
	cache  map[schema.GroupVersion]intentSchemaCacheEntry
}

// NewIntentSchemaResolver creates a resolver for the connected API server. Only
// converted schemas are cached: each lookup fetches the current group/version
// document so that changing a CRD cannot leave a stale deletion topology in use.
func NewIntentSchemaResolver(config *rest.Config) (IntentSchemaResolver, error) {
	if config == nil {
		return nil, fmt.Errorf("intent schema resolver requires a REST config")
	}
	config = rest.CopyConfig(config)
	if config.Timeout <= 0 || config.Timeout > intentSchemaTimeout {
		config.Timeout = intentSchemaTimeout
	}
	client, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create intent schema discovery client: %w", err)
	}
	return &liveIntentSchemaResolver{
		client: client.RESTClient(),
		cache:  make(map[schema.GroupVersion]intentSchemaCacheEntry),
	}, nil
}

func (r *liveIntentSchemaResolver) Resolve(ctx context.Context, gvk schema.GroupVersionKind) (managedfields.TypeConverter, string, error) {
	if gvk.Version == "" || gvk.Kind == "" {
		return nil, "", fmt.Errorf("intent schema requires a complete GVK: %s", gvk)
	}
	ctx, cancel := context.WithTimeout(ctx, intentSchemaTimeout)
	defer cancel()
	path := "/openapi/v3/api/" + gvk.Version
	if gvk.Group != "" {
		path = "/openapi/v3/apis/" + gvk.Group + "/" + gvk.Version
	}
	// client-go's OpenAPIV3().Paths()/Schema() use context.TODO internally.
	// The same unversioned discovery REST client keeps caller cancellation and
	// timeout semantics while requesting the current (unhashed) GV endpoint.
	raw, err := r.client.Get().AbsPath(path).SetHeader("Accept", "application/json").DoRaw(ctx)
	useV2 := false
	if apierrors.IsNotFound(err) {
		// A newly installed CRD may be served before its v3 GV document has
		// been published. That is a retryable discovery gap, not an old server:
		// recording a v2 baseline now and switching to v3 later can change its
		// topology hash despite no CRD edit. Only a missing v3 root proves the
		// server lacks v3 discovery.
		_, rootErr := r.client.Get().AbsPath("/openapi/v3").SetHeader("Accept", "application/json").DoRaw(ctx)
		if rootErr == nil {
			return nil, "", apierrors.NewServiceUnavailable(fmt.Sprintf("OpenAPI v3 schema for %s is not published yet; retry after discovery catches up", gvk))
		}
		if !apierrors.IsNotFound(rootErr) {
			return nil, "", fmt.Errorf("check OpenAPI v3 availability for %s: %w", gvk, rootErr)
		}
		useV2 = true
		// Kubernetes 1.21/1.22 do not publish OpenAPI v3. Their actual v2
		// definitions are still usable merge schemas, not a deduced fallback.
		// Authorization, timeout, and malformed-v3 errors must remain failures.
		raw, err = r.client.Get().AbsPath("/openapi/v2").SetHeader("Accept", "application/json").DoRaw(ctx)
	}
	if err != nil {
		return nil, "", fmt.Errorf("read intent schema for %s: %w", gvk, err)
	}
	var document struct {
		Components struct {
			Schemas map[string]*spec.Schema `json:"schemas"`
		} `json:"components"`
		Definitions map[string]*spec.Schema `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, "", fmt.Errorf("decode intent schema for %s: %w", gvk, err)
	}
	models := document.Components.Schemas
	if useV2 {
		models = document.Definitions
	}
	if len(models) == 0 {
		return nil, "", fmt.Errorf("OpenAPI schemas missing for %s", gvk)
	}
	root, err := requireIntentSchemaGVK(models, gvk)
	if err != nil {
		return nil, "", err
	}
	canonical, err := json.Marshal(models)
	if err != nil {
		return nil, "", fmt.Errorf("hash intent schema for %s: %w", gvk, err)
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
	gv := gvk.GroupVersion()
	if useV2 {
		// v2 is a single server-wide document. Share its converter instead of
		// retaining a full copy for every requested group/version.
		gv = schema.GroupVersion{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, ok := r.cache[gv]; ok && cached.hash == hash {
		semanticHash, err := intentMergeTopologyHash(cached.topology, root)
		return cached.converter, semanticHash, err
	}
	converter, err := managedfields.NewTypeConverter(models, false)
	if err != nil {
		return nil, "", fmt.Errorf("convert intent schema for %s: %w", gvk, err)
	}
	topology, err := schemaconv.ToSchemaFromOpenAPI(models, false)
	if err != nil {
		return nil, "", fmt.Errorf("convert intent topology for %s: %w", gvk, err)
	}
	// OpenAPI properties are maps. The converter preserves their iteration
	// order in Fields, which must not make a stable topology hash nondeterministic.
	for i := range topology.Types {
		sortIntentSchemaFields(&topology.Types[i].Atom)
	}
	semanticHash, err := intentMergeTopologyHash(topology, root)
	if err != nil {
		return nil, "", err
	}
	r.cache[gv] = intentSchemaCacheEntry{hash: hash, converter: converter, topology: topology}
	return converter, semanticHash, nil
}

func requireIntentSchemaGVK(models map[string]*spec.Schema, gvk schema.GroupVersionKind) (string, error) {
	matches := 0
	root := ""
	for name, model := range models {
		if model == nil {
			return "", fmt.Errorf("OpenAPI contains a null schema for %s", gvk)
		}
		extension, exists := model.Extensions["x-kubernetes-group-version-kind"]
		if !exists {
			continue
		}
		raw, err := json.Marshal(extension)
		if err != nil {
			return "", fmt.Errorf("encode intent schema GVK: %w", err)
		}
		var kinds []schema.GroupVersionKind
		if err := json.Unmarshal(raw, &kinds); err != nil {
			return "", fmt.Errorf("decode intent schema GVK: %w", err)
		}
		for _, candidate := range kinds {
			if candidate == gvk {
				matches++
				root = name
			}
		}
	}
	if matches != 1 {
		return "", fmt.Errorf("expected exactly one OpenAPI model for %s, found %d", gvk, matches)
	}
	return root, nil
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
