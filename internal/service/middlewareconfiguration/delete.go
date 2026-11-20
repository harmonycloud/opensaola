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

package middlewareconfiguration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"github.com/mohae/deepcopy"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	kjson "sigs.k8s.io/json"
)

func applyConfigurationValues(ctx context.Context, cfg v1.Configuration, templateValues *tools.TemplateValues) error {
	if len(cfg.Values.Raw) == 0 {
		return nil
	}
	data, err := cfg.Values.MarshalJSON()
	if err != nil {
		return err
	}
	parse, err := tools.TemplateParse(ctx, string(data), templateValues)
	if err != nil {
		return err
	}
	return kjson.UnmarshalCaseSensitivePreserveInts([]byte(parse), &templateValues.Values)
}

type deleteByNameResult string

const (
	deleteByNameDeleted  deleteByNameResult = "deleted"
	deleteByNameNotFound deleteByNameResult = "not_found"
	deleteByNameSkipped  deleteByNameResult = "skipped"
)

func deleteByGVKAndName(ctx context.Context, cli client.Client, owner metav1.Object, gvk schema.GroupVersionKind, namespace string, name string, configuration *v1.MiddlewareConfiguration) (deleteByNameResult, error) {
	log.FromContext(ctx).Info("deleting configuration rendered resource by name", "gvk", gvk.String(), "namespace", namespace, "name", name)
	obj, err := k8s.GetCustomResource(ctx, cli, name, namespace, gvk)
	if apierrors.IsNotFound(err) {
		log.FromContext(ctx).Info("configuration rendered resource not found by name", "warning", true, "gvk", gvk.String(), "namespace", namespace, "name", name)
		return deleteByNameNotFound, nil
	}
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to get configuration rendered resource by name", "gvk", gvk.String(), "namespace", namespace, "name", name)
		return "", err
	}
	return retireConfigurationResource(ctx, cli, owner, obj, configuration)
}

// retireConfigurationResource uses the same observation for the policy decision
// and mutation. A modified or recreated object must be evaluated again.
func retireConfigurationResource(ctx context.Context, cli client.Client, owner metav1.Object, live *unstructured.Unstructured, configuration *v1.MiddlewareConfiguration) (deleteByNameResult, error) {
	if recordedConfigurationConflict(owner, live, configuration.Name) {
		return deleteByNameSkipped, nil
	}
	policy := resolveConfigurationDeletePolicy(configuration, live)
	if !shouldDeleteRenderedResource(owner, live, configuration.Name, policy) {
		if policy == v1.ConfigurationDeletePolicyOrphan && canDetachConfigurationOwner(owner, live, configuration.Name) {
			if err := stripLiveConfigurationOwnerReference(ctx, cli, live, owner); err != nil && !apierrors.IsNotFound(err) {
				return "", err
			}
		}
		return deleteByNameSkipped, nil
	}
	uid, rv := live.GetUID(), live.GetResourceVersion()
	if err := cli.Delete(ctx, live, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil {
		if apierrors.IsNotFound(err) {
			// The observed target is already gone. Do not broaden this into a list cleanup.
			return deleteByNameDeleted, nil
		}
		return "", err
	}
	return deleteByNameDeleted, nil
}

// An old target can still carry a garbage-collection reference at deletion
// time. Prefer the exact recorded identity; releases without inventory can
// still prove a link by the app/configuration markers and the owner's UID.
func canDetachConfigurationOwner(owner metav1.Object, live *unstructured.Unstructured, configurationName string) bool {
	if !isOpenSaolaManagedConfiguration(owner, live, configurationName) ||
		owner.GetUID() == "" || owner.GetNamespace() != live.GetNamespace() {
		return false
	}
	if controller := metav1.GetControllerOf(live); controller != nil && controller.UID != owner.GetUID() {
		return false
	}
	if !slices.ContainsFunc(live.GetOwnerReferences(), func(ref metav1.OwnerReference) bool { return ref.UID == owner.GetUID() }) {
		return false
	}
	if marked := live.GetAnnotations()[v1.AnnotationConfigurationOwnerUID]; marked != "" && marked != string(owner.GetUID()) {
		return false
	}
	var inventory []v1.RenderedConfigurationResource
	switch m := owner.(type) {
	case *v1.Middleware:
		inventory = m.Status.RenderedConfigurationResources
	case *v1.MiddlewareOperator:
		inventory = m.Status.RenderedConfigurationResources
	default:
		return false
	}
	found := false
	for _, prior := range inventory {
		if atRecordedAddress(prior, live) {
			found = true
			if matchesRecordedConfiguration(prior, owner, live, configurationName) {
				return true
			}
		}
	}
	return !found
}

// Resolve only the identity when cleaning up an object. Re-rendering can
// confirm that a configuration has no output, but cannot identify an old target.
func DeleteTemplateRenderedResources(ctx context.Context, cli client.Client, owner metav1.Object, quoter tools.Quoter, prepareParameters func(context.Context, *tools.TemplateValues) error) error {
	configurations := quoter.GetConfigurations()
	if resourceOwner, ok := owner.(client.Object); ok {
		if err := recoverConfigurationWrite(ctx, cli, resourceOwner); err != nil {
			return err
		}
		// A reference can be withdrawn immediately before owner deletion, before
		// the new generation has ever been reconciled. Complete that withdrawal
		// using the published policy and identity, including on an empty spec.
		if entries, _ := configurationInventory(resourceOwner); entries != nil {
			referenced := make(map[string]bool, len(configurations))
			for _, cfg := range configurations {
				referenced[cfg.Name] = true
			}
			var withdrawn []v1.RenderedConfigurationResource
			for _, entry := range *entries {
				if !referenced[entry.ConfigurationName] {
					withdrawn = append(withdrawn, entry)
				}
			}
			if err := DeleteDisabledRenderedConfigurationResources(ctx, cli, owner, withdrawn, nil); err != nil {
				return err
			}
		}
	}
	if len(configurations) == 0 {
		return nil
	}
	pkgName := quoter.GetLabels()[v1.LabelPackageName]
	if pkgName == "" {
		return fmt.Errorf("package name is empty")
	}
	baseValues, err := tools.GetTemplateValues(ctx, quoter)
	if err != nil {
		return fmt.Errorf("failed to get template values: %w", err)
	}
	var problems []error
	for _, cfg := range configurations {
		mc, err := Get(ctx, cli, cfg.Name, pkgName)
		if err == nil {
			err = deleteConfigurationTargets(ctx, cli, owner, &mc, cfg, baseValues)
			// Generated objects and fields that depend on earlier mutations
			// can only be identified by their original publication.
			if errors.Is(err, errOpaqueDeleteIdentity) || errors.Is(err, errMutableDeleteIdentity) {
				if handled, recordErr := retireRecordedConfiguration(ctx, cli, owner, &mc); handled || recordErr != nil {
					err = recordErr
				} else {
					err = verifyEmptyConfiguration(ctx, cfg, baseValues, &mc, err, prepareParameters)
				}
			}
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("configuration %s: %w", cfg.Name, err))
		}
	}
	return errors.Join(problems...)
}

func copyConfigurationRenderInputs(base *tools.TemplateValues) (*tools.TemplateValues, error) {
	// Sprig's set and merge can mutate nested inputs used by later cleanups.
	values, ok := deepcopy.Copy(*base).(tools.TemplateValues)
	if !ok {
		return nil, errors.New("cannot copy configuration render inputs")
	}
	// API discovery is read-only. Its private maps are not reflect-copyable.
	values.Capabilities = base.Capabilities
	return &values, nil
}

func verifyEmptyConfiguration(ctx context.Context, cfg v1.Configuration, base *tools.TemplateValues, mc *v1.MiddlewareConfiguration, identityErr error, prepareParameters func(context.Context, *tools.TemplateValues) error) error {
	if bytes.Contains(cfg.Values.Raw, []byte("{{")) {
		refValues, err := copyConfigurationRenderInputs(base)
		if err != nil {
			return err
		}
		if prepareParameters != nil {
			if err := prepareParameters(ctx, refValues); err != nil {
				return fmt.Errorf("prepare configuration reference parameters: %w", err)
			}
		}
		refs, err := RenderConfigurationReferences(ctx, []v1.Configuration{cfg}, refValues)
		if err != nil {
			return err
		}
		if len(refs) != 1 || refs[0].Name != cfg.Name {
			return errors.New("rendered configuration reference changed its identity")
		}
		cfg = refs[0]
	}
	values, err := copyConfigurationRenderInputs(base)
	if err != nil {
		return err
	}
	rendered, err := renderConfiguration(ctx, cfg, *values, mc)
	if err != nil {
		return fmt.Errorf("check empty configuration output: %w", err)
	}
	doc, err := identityDocument(rendered)
	if errors.Is(err, io.EOF) || err == nil && emptyYAMLDocument(doc) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check rendered configuration documents: %w", err)
	}
	return identityErr
}

func deleteConfigurationTargets(ctx context.Context, cli client.Client, owner metav1.Object, mc *v1.MiddlewareConfiguration, cfg v1.Configuration, base *tools.TemplateValues) error {
	id, err := parseDeleteIdentity(mc.Spec.Template)
	if err != nil {
		return err
	}
	// Literal fields do not depend on configuration values. Prepare those
	// values once, only if an identity expression actually needs rendering.
	values := sync.OnceValues(func() (*tools.TemplateValues, error) {
		v := *base
		return &v, applyConfigurationValues(ctx, cfg, &v)
	})
	render := func(key string, field deleteIdentityField) (string, error) {
		v := base
		if field.Dynamic() {
			var err error
			v, err = values()
			if err != nil {
				return "", fmt.Errorf("prepare values for %s: %w", key, err)
			}
		}
		if !field.Present() {
			return "", fmt.Errorf("missing %s", key)
		}
		value, err := field.Render(ctx, v)
		if err != nil {
			return "", fmt.Errorf("render %s: %w", key, err)
		}
		return value, nil
	}
	version, err := render("apiVersion", id.APIVersion)
	if err != nil {
		return err
	}
	kind, err := render("kind", id.Kind)
	if err != nil {
		return err
	}
	gvk, err := deleteResourceGVK(version, kind)
	if err != nil {
		return err
	}
	mapping, err := cli.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	missingKind := meta.IsNoMatchError(err) || k8s.IsCRDNotInstalled(err)
	if err != nil && !missingKind {
		return fmt.Errorf("resolve scope for %s: %w", gvk, err)
	}
	ns := ""
	if (!missingKind && mapping.Scope.Name() == meta.RESTScopeNameNamespace) || (missingKind && id.Namespace.Present()) {
		ns = owner.GetNamespace()
		if id.Namespace.Present() {
			ns, err = render("metadata.namespace", id.Namespace)
			if err != nil {
				return err
			}
		}
		if len(validation.IsDNS1123Label(ns)) != 0 {
			return fmt.Errorf("invalid metadata.namespace for %s", gvk.Kind)
		}
	}
	name, nameErr := render("metadata.name", id.Name)
	if errors.Is(nameErr, errMutableDeleteIdentity) {
		return nameErr
	}
	if handled, err := retireRecordedConfiguration(ctx, cli, owner, mc); handled || err != nil {
		return err
	}
	if missingKind || isCustomResourceDefinition(gvk) {
		return nil
	}
	if nameErr == nil {
		outcome, err := deleteByGVKAndName(ctx, cli, owner, gvk, ns, name, mc)
		if outcome != deleteByNameNotFound || err != nil {
			return err
		}
	} else {
		log.FromContext(ctx).Info("cannot render resource name, checking configuration markers in the resolved scope", "configuration", mc.Name, "err", nameErr)
	}
	items, err := k8s.ListCustomResources(ctx, cli, ns, gvk, client.MatchingLabels{v1.LabelApp: owner.GetName()})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("list fallback for %s: %w", gvk, err)
	}
	var problems []error
	for i := range items {
		item := &items[i]
		annotations := item.GetAnnotations()
		if annotations[v1.LabelConfigurations] != mc.Name {
			continue
		}
		// Instance names can coincide across namespaces. A label scan must
		// respect any stronger ownership evidence, even with an explicit delete policy.
		if marked := annotations[v1.AnnotationConfigurationOwnerUID]; marked != "" && marked != string(owner.GetUID()) {
			continue
		}
		if controller := metav1.GetControllerOf(item); controller != nil && !sameControllerOwner(owner, controller) {
			continue
		}
		item.SetGroupVersionKind(gvk)
		if _, err := retireConfigurationResource(ctx, cli, owner, item, mc); err != nil {
			problems = append(problems, fmt.Errorf("clean up %s/%s: %w", ns, item.GetName(), err))
		}
	}
	return errors.Join(problems...)
}

// Invalid rendered strings must not turn into a RESTMapper NoMatch, which
// is also the normal result when an optional CRD has been uninstalled.
func deleteResourceGVK(version, kind string) (schema.GroupVersionKind, error) {
	gv, err := schema.ParseGroupVersion(version)
	if err != nil || gv.String() != version || len(validation.IsDNS1035Label(gv.Version)) != 0 ||
		(gv.Group != "" && len(validation.IsDNS1123Subdomain(gv.Group)) != 0) {
		return schema.GroupVersionKind{}, fmt.Errorf("invalid rendered apiVersion")
	}
	if len(validation.IsDNS1035Label(strings.ToLower(kind))) != 0 {
		return schema.GroupVersionKind{}, fmt.Errorf("invalid rendered kind")
	}
	return gv.WithKind(kind), nil
}
