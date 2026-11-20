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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const switchWrite = "middleware.cn/package-switch-write"

type definitionKey struct{ Kind, Name string }

func keyFor(o *unstructured.Unstructured) definitionKey {
	return definitionKey{o.GetKind(), o.GetName()}
}
func (k definitionKey) object() *unstructured.Unstructured {
	o := new(unstructured.Unstructured)
	o.SetGroupVersionKind(v1.GroupVersion.WithKind(k.Kind))
	o.SetName(k.Name)
	return o
}

type switchSource struct {
	Name           string
	UID, SecretUID types.UID
	Version        string
	Retire         []definitionKey
}

func (s switchSource) packageObject() *v1.MiddlewarePackage {
	return &v1.MiddlewarePackage{ObjectMeta: metav1.ObjectMeta{Name: s.Name, UID: s.UID}, Spec: v1.MiddlewarePackageSpec{Version: s.Version}}
}

type switchChange struct {
	Key      definitionKey
	Before   *unstructured.Unstructured `json:",omitempty"`
	Expected string                     // hash of the API preflight response, excluding server bookkeeping
}

func readDefinition(ctx context.Context, cli client.Client, key definitionKey) (*unstructured.Unstructured, error) {
	o := key.object()
	err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKey{Name: key.Name}, o)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return o, err
}

// Only these fields are owned by package publication. A status controller can
// update status and resourceVersion at any time, including during recovery.
func definitionContent(o *unstructured.Unstructured) map[string]any {
	return map[string]any{"spec": o.Object["spec"], "labels": o.GetLabels(), "annotations": o.GetAnnotations(), "owners": o.GetOwnerReferences(), "finalizers": o.GetFinalizers()}
}
func definitionHash(o *unstructured.Unstructured) (string, error) {
	data, err := json.Marshal(definitionContent(o))
	if err != nil {
		return "", fmt.Errorf("encode %s %s for package verification: %w", o.GetKind(), o.GetName(), err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
func hasDeclaredMetadata(next, probe *unstructured.Unstructured) bool {
	for k, v := range next.GetAnnotations() {
		if observed, ok := probe.GetAnnotations()[k]; !ok || observed != v {
			return false
		}
	}
	for k, v := range next.GetLabels() {
		if observed, ok := probe.GetLabels()[k]; !ok || observed != v {
			return false
		}
	}
	for _, f := range next.GetFinalizers() {
		if !slices.Contains(probe.GetFinalizers(), f) {
			return false
		}
	}
	for _, owner := range next.GetOwnerReferences() {
		if !slices.ContainsFunc(probe.GetOwnerReferences(), func(actual metav1.OwnerReference) bool { return reflect.DeepEqual(owner, actual) }) {
			return false
		}
	}
	return true
}
func copySpec(to, from *unstructured.Unstructured) {
	if spec, ok := from.Object["spec"]; ok {
		to.Object["spec"] = spec
	} else {
		delete(to.Object, "spec")
	}
}

// Kubernetes prunes optional null fields in these structural CRDs (for
// example ActionBaseline.spec.necessary). All non-null declarations, list
// lengths and ordering must survive admission.
func declaredSpecMatches(wanted, actual any) bool {
	switch value := wanted.(type) {
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range value {
			if !declaredSpecMatches(v, got[k]) {
				return false
			}
		}
		return true
	case []any:
		got, ok := actual.([]any)
		if !ok || len(value) != len(got) {
			return false
		}
		for i, v := range value {
			if !declaredSpecMatches(v, got[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(wanted, actual)
	}
}

func definitionOwnedBy(o *unstructured.Unstructured, name string, uid types.UID, version string) bool {
	r := metav1.GetControllerOfNoCopy(o)
	return r != nil && r.APIVersion == v1.GroupVersion.String() && r.Kind == "MiddlewarePackage" && r.Name == name && r.UID != "" && r.UID == uid && o.GetLabels()[v1.LabelPackageName] == name && o.GetLabels()[v1.LabelPackageVersion] == version
}

func desiredDefinition(def, live, old *unstructured.Unstructured, mp *v1.MiddlewarePackage, token string) *unstructured.Unstructured {
	next := def.DeepCopy()
	if live != nil {
		next = live.DeepCopy()
	} else {
		next.SetLabels(nil)
	}
	copySpec(next, def)
	labels := next.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[v1.LabelPackageName], labels[v1.LabelPackageVersion], labels[v1.LabelComponent] = mp.Name, mp.Spec.Version, mp.Spec.Name
	next.SetLabels(labels)
	annotations := next.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if old != nil {
		for key := range old.GetAnnotations() {
			delete(annotations, key)
		}
	}
	maps.Copy(annotations, def.GetAnnotations())
	annotations[switchWrite] = token
	next.SetAnnotations(annotations)
	finalizers := next.GetFinalizers()
	if old != nil {
		finalizers = slices.DeleteFunc(finalizers, func(f string) bool {
			return slices.Contains(old.GetFinalizers(), f) && !slices.Contains(def.GetFinalizers(), f)
		})
	}
	for _, f := range def.GetFinalizers() {
		if !slices.Contains(finalizers, f) {
			finalizers = append(finalizers, f)
		}
	}
	next.SetFinalizers(finalizers)
	owners := next.GetOwnerReferences()
	kept := owners[:0]
	for _, ref := range owners {
		if ref.Controller != nil && *ref.Controller {
			continue
		}
		if old != nil && slices.ContainsFunc(old.GetOwnerReferences(), func(prior metav1.OwnerReference) bool { return prior.UID == ref.UID }) {
			continue
		}
		kept = append(kept, ref)
	}
	for _, ref := range def.GetOwnerReferences() {
		if ref.Controller != nil && *ref.Controller {
			continue
		}
		if !slices.ContainsFunc(kept, func(r metav1.OwnerReference) bool { return r.UID == ref.UID }) {
			kept = append(kept, ref)
		}
	}
	next.SetOwnerReferences(append(kept, *metav1.NewControllerRef(mp, v1.GroupVersion.WithKind("MiddlewarePackage"))))
	return next
}

func writeDefinition(ctx context.Context, cli client.Client, obj *unstructured.Unstructured, create, dry bool) error {
	if create {
		if dry {
			return cli.Create(ctx, obj, client.DryRunAll)
		}
		return cli.Create(ctx, obj)
	}
	if dry {
		return cli.Update(ctx, obj, client.DryRunAll)
	}
	return cli.Update(ctx, obj)
}

// Build the entire undo log before the first real definition write. The API
// validates the same CREATE/UPDATE that will be issued with optimistic locking.
func prepareDefinitions(ctx context.Context, cli client.Client, mp *v1.MiddlewarePackage, pkg *packages.Package, j *switchJournal, token string) ([]*unstructured.Unstructured, error) {
	defs, err := pkg.Definitions()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidDefinition, err)
	}
	wanted := make(map[definitionKey]bool, len(defs))
	for _, def := range defs {
		wanted[keyFor(def)] = true
	}
	previous := map[string]map[definitionKey]*unstructured.Unstructured{}
	for i := range j.Sources {
		src := &j.Sources[i]
		archive, err := packages.Get(ctx, cli, src.Name)
		if err != nil {
			return nil, err
		}
		prior, err := archive.Definitions()
		if err != nil {
			return nil, err
		}
		previous[src.Name] = map[definitionKey]*unstructured.Unstructured{}
		for _, def := range prior {
			key := keyFor(def)
			previous[src.Name][key] = def
			if !wanted[key] {
				live, err := readDefinition(ctx, cli, key)
				if err != nil {
					return nil, err
				}
				if live != nil {
					if err := retireDefinition(ctx, cli, src.packageObject(), live, true); err != nil {
						return nil, err
					}
				}
				src.Retire = append(src.Retire, key)
			}
		}
	}
	var writes []*unstructured.Unstructured
	for _, def := range defs {
		key := keyFor(def)
		live, err := readDefinition(ctx, cli, key)
		if err != nil {
			return nil, err
		}
		var old *unstructured.Unstructured
		if live != nil {
			if live.GetDeletionTimestamp() != nil {
				return nil, fmt.Errorf("%s %s is terminating", key.Kind, key.Name)
			}
			owned := definitionOwnedBy(live, mp.Name, mp.UID, mp.Spec.Version)
			for _, src := range j.Sources {
				if definitionOwnedBy(live, src.Name, src.UID, src.Version) {
					old = previous[src.Name][key]
					owned = old != nil
					break
				}
			}
			if !owned {
				return nil, fmt.Errorf("cannot switch %s %s: existing definition does not belong to the selected packages", key.Kind, key.Name)
			}

		}
		if ctrl := metav1.GetControllerOfNoCopy(def); ctrl != nil && (ctrl.UID != mp.UID || ctrl.Kind != "MiddlewarePackage" || ctrl.APIVersion != v1.GroupVersion.String() || ctrl.Name != mp.Name) {
			return nil, fmt.Errorf("%w: %s %s declares a different controller owner", errInvalidDefinition, key.Kind, key.Name)
		}
		next := desiredDefinition(def, live, old, mp, token)
		probe := next.DeepCopy()
		if err := writeDefinition(ctx, cli, probe, live == nil, true); err != nil {
			return nil, fmt.Errorf("preflight %s %s: %w", key.Kind, key.Name, err)
		}
		if !declaredSpecMatches(next.Object["spec"], probe.Object["spec"]) || !definitionOwnedBy(probe, mp.Name, mp.UID, mp.Spec.Version) || !hasDeclaredMetadata(next, probe) {
			return nil, fmt.Errorf("%w: preflight changed declared fields of %s %s", errInvalidDefinition, key.Kind, key.Name)
		}
		if live != nil {
			// Status and managed fields are maintained by other controllers.
			live.SetManagedFields(nil)
			delete(live.Object, "status")
		}
		expected, err := definitionHash(probe)
		if err != nil {
			return nil, err
		}
		j.Changes = append(j.Changes, switchChange{Key: key, Before: live, Expected: expected})
		writes = append(writes, next)
	}
	return writes, nil
}

func rollbackDefinitions(ctx context.Context, cli client.Client, j *switchJournal, token string) error {
	var failures []error
	for i := len(j.Changes) - 1; i >= 0; i-- {
		if err := rollbackDefinition(ctx, cli, j.Changes[i], token); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func rollbackDefinition(ctx context.Context, cli client.Client, step switchChange, token string) error {
	live, err := readDefinition(ctx, cli, step.Key)
	if err != nil {
		return err
	}
	before := step.Before
	if live == nil {
		if before == nil {
			return nil
		}
		return fmt.Errorf("rollback conflict: %s %s was deleted", step.Key.Kind, step.Key.Name)
	}
	if before != nil && live.GetUID() == before.GetUID() && reflect.DeepEqual(definitionContent(live), definitionContent(before)) {
		return nil
	}
	digest, err := definitionHash(live)
	if err != nil {
		return err
	}
	if live.GetAnnotations()[switchWrite] != token || digest != step.Expected || (before != nil && live.GetUID() != before.GetUID()) {
		return fmt.Errorf("rollback conflict: %s %s has another identity or changed content", step.Key.Kind, step.Key.Name)
	}
	if before == nil {
		uid, rv := live.GetUID(), live.GetResourceVersion()
		err = client.IgnoreNotFound(cli.Delete(ctx, live, client.Preconditions{UID: &uid, ResourceVersion: &rv}))
		if err == nil {
			current, readErr := readDefinition(ctx, cli, step.Key)
			if readErr != nil {
				return readErr
			}
			if current != nil && current.GetUID() == uid {
				return fmt.Errorf("waiting for rollback deletion of %s %s", step.Key.Kind, step.Key.Name)
			}
		}
	} else {
		copySpec(live, before)
		live.SetLabels(before.GetLabels())
		live.SetAnnotations(before.GetAnnotations())
		live.SetOwnerReferences(before.GetOwnerReferences())
		live.SetFinalizers(before.GetFinalizers())
		err = cli.Update(ctx, live)
		if err == nil && !reflect.DeepEqual(definitionContent(live), definitionContent(before)) {
			return fmt.Errorf("rollback of %s %s was modified by admission", step.Key.Kind, step.Key.Name)
		}
	}
	if err != nil {
		return err
	}
	return nil
}
