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
	"fmt"
	"slices"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func pendingConfigurationWrite(owner client.Object) **v1.PendingConfigurationWrite {
	switch o := owner.(type) {
	case *v1.Middleware:
		return &o.Status.PendingConfigurationWrite
	case *v1.MiddlewareOperator:
		return &o.Status.PendingConfigurationWrite
	default:
		return nil
	}
}

func sameConfigurationCheckpoint(a, b client.Object) bool {
	ae, ag := configurationInventory(a)
	be, bg := configurationInventory(b)
	return *ag == *bg && equality.Semantic.DeepEqual(*ae, *be) && equality.Semantic.DeepEqual(*pendingConfigurationWrite(a), *pendingConfigurationWrite(b))
}

func readConfigurationCheckpoint(ctx context.Context, cli client.Client, owner client.Object) (client.Object, error) {
	var live client.Object
	switch owner.(type) {
	case *v1.Middleware:
		live = &v1.Middleware{}
	case *v1.MiddlewareOperator:
		live = &v1.MiddlewareOperator{}
	default:
		return nil, fmt.Errorf("unsupported Configuration owner %T", owner)
	}
	if err := k8s.NewManagedResourceWriter(cli).Reader.Get(ctx, client.ObjectKeyFromObject(owner), live); err != nil {
		return nil, err
	}
	if live.GetUID() == "" || owner.GetUID() != live.GetUID() || owner.GetGeneration() != live.GetGeneration() {
		return nil, fmt.Errorf("Configuration owner changed; retry %s", client.ObjectKeyFromObject(owner))
	}
	return live, nil
}

// Only the inventory, its completion generation and pending entry are changed.
// Conditions and other unpersisted state in the caller's object remain its own.
func patchConfigurationCheckpoint(ctx context.Context, cli client.Client, owner, next client.Object) error {
	live, err := readConfigurationCheckpoint(ctx, cli, owner)
	if err != nil {
		return err
	}
	// Pause and deletion can change without advancing the owner generation.
	// Only opening a new attempt requires an active owner; an existing
	// receipt still has to be settled during teardown.
	if *pendingConfigurationWrite(live) == nil && *pendingConfigurationWrite(next) != nil {
		if live.GetDeletionTimestamp() != nil {
			return fmt.Errorf("Configuration owner is deleting")
		}
		switch o := live.(type) {
		case *v1.Middleware:
			if v1.IsMiddlewareReconcileWriteSuspended(o.Annotations, o.Status.Conditions) {
				return fmt.Errorf("Configuration owner is paused")
			}
		case *v1.MiddlewareOperator:
			if v1.IsReconcileSuspended(o.Annotations) {
				return fmt.Errorf("Configuration owner is paused")
			}
		}
	}
	if !sameConfigurationCheckpoint(owner, live) {
		return fmt.Errorf("Configuration owner or inventory changed; retry %s", client.ObjectKeyFromObject(owner))
	}
	if !sameConfigurationCheckpoint(next, live) {
		base, ok := live.DeepCopyObject().(client.Object)
		if !ok {
			return fmt.Errorf("invalid Configuration owner copy %T", live)
		}
		patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
		copyConfigurationCheckpoint(live, next)
		if err := cli.Status().Patch(ctx, live, patch); err != nil {
			return fmt.Errorf("persist Configuration inventory: %w", err)
		}
		if !sameConfigurationCheckpoint(next, live) {
			return fmt.Errorf("Configuration inventory was not persisted; check the installed MID and MO CRDs")
		}
	}
	copyConfigurationCheckpoint(owner, live)
	owner.SetResourceVersion(live.GetResourceVersion())
	return nil
}

func copyConfigurationCheckpoint(dst, src client.Object) {
	de, dg := configurationInventory(dst)
	se, sg := configurationInventory(src)
	*de = slices.Clone(*se)
	*dg = *sg
	*pendingConfigurationWrite(dst) = (*pendingConfigurationWrite(src)).DeepCopy()
}

func beginConfigurationWrite(ctx context.Context, cli client.Client, owner client.Object, entry v1.RenderedConfigurationResource, observedUID types.UID) (string, error) {
	pending := pendingConfigurationWrite(owner)
	if pending == nil || *pending != nil || owner.GetUID() == "" || owner.GetUID() != entry.OwnerUID || entry.ConfigurationName == "" || renderedConfigurationResourceKey(entry) == "" || entry.Namespaced && entry.Namespace == "" {
		return "", fmt.Errorf("invalid or unresolved Configuration write for %s", entry.Name)
	}
	entry.ResourceUID = ""
	id := string(uuid.NewUUID())
	next, ok := owner.DeepCopyObject().(client.Object)
	if !ok {
		return "", fmt.Errorf("invalid Configuration owner copy %T", owner)
	}
	*pendingConfigurationWrite(next) = &v1.PendingConfigurationWrite{ID: id, Resource: entry, ObservedUID: observedUID}
	if err := patchConfigurationCheckpoint(ctx, cli, owner, next); err != nil {
		return "", err
	}
	return id, nil
}

func finishConfigurationWrite(ctx context.Context, cli client.Client, owner client.Object, confirmed *v1.RenderedConfigurationResource) error {
	pending := pendingConfigurationWrite(owner)
	if pending == nil || *pending == nil {
		return fmt.Errorf("no pending Configuration write to finish")
	}
	next, ok := owner.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("invalid Configuration owner copy %T", owner)
	}
	if confirmed != nil {
		expected := (*pending).Resource
		expected.ResourceUID = confirmed.ResourceUID
		if expected.OwnerUID != owner.GetUID() || !hasPublishedIdentity(*confirmed) || !equality.Semantic.DeepEqual(expected, *confirmed) || (*pending).ObservedUID != "" && (*pending).ObservedUID != confirmed.ResourceUID {
			return fmt.Errorf("pending Configuration write does not match the published target %s", confirmed.Name)
		}
		entries, _ := configurationInventory(next)
		*entries = slices.DeleteFunc(*entries, func(e v1.RenderedConfigurationResource) bool {
			return e.Group == confirmed.Group && e.Kind == confirmed.Kind && e.Namespace == confirmed.Namespace && e.Name == confirmed.Name
		})
		*entries = append(*entries, *confirmed)
	}
	*pendingConfigurationWrite(next) = nil
	return patchConfigurationCheckpoint(ctx, cli, owner, next)
}

func recoverConfigurationWrite(ctx context.Context, cli client.Client, owner client.Object) error {
	liveOwner, err := readConfigurationCheckpoint(ctx, cli, owner)
	if err != nil {
		return err
	}
	copyConfigurationCheckpoint(owner, liveOwner)
	owner.SetResourceVersion(liveOwner.GetResourceVersion())
	p := *pendingConfigurationWrite(owner)
	if p == nil {
		return nil
	}
	entry := p.Resource
	if p.ID == "" || entry.OwnerUID != owner.GetUID() || owner.GetUID() == "" || entry.ConfigurationName == "" || renderedConfigurationResourceKey(entry) == "" {
		return fmt.Errorf("invalid pending Configuration record for %s", client.ObjectKeyFromObject(owner))
	}
	reader := k8s.NewManagedResourceWriter(cli).Reader
	gvk := schema.GroupVersionKind{Group: entry.Group, Version: entry.Version, Kind: entry.Kind}
	live, err := getRecordedConfigurationResourceWithReader(ctx, cli, reader, entry, gvk)
	if err != nil {
		return fmt.Errorf("read pending Configuration target: %w", err)
	}
	if live != nil && p.ObservedUID == "" && k8s.HasConfigurationCreateReceipt(live, p.ID) && live.GetAnnotations()[v1.AnnotationConfigurationWriteID] != p.ID {
		return fmt.Errorf("pending Configuration target %s has a creation receipt but its write marker changed", client.ObjectKeyFromObject(live))
	}
	if live == nil || p.ObservedUID != "" && p.ObservedUID != live.GetUID() || live.GetAnnotations()[v1.AnnotationConfigurationWriteID] != p.ID {
		// No receipt for this incarnation. Preserve all earlier confirmed entries.
		return finishConfigurationWrite(ctx, cli, owner, nil)
	}
	if live.GetUID() == "" || !hasConfigurationLifecycleMarkers(owner, live, entry.ConfigurationName, entry.ConfigurationUID) {
		return fmt.Errorf("pending Configuration write %s has inconsistent ownership markers on %s", p.ID, client.ObjectKeyFromObject(live))
	}
	if p.ObservedUID == "" && !k8s.HasConfigurationCreateReceipt(live, p.ID) {
		return fmt.Errorf("cannot verify creation of pending Configuration target %s; write %s lacks its creation receipt", client.ObjectKeyFromObject(live), p.ID)
	}
	if ref := metav1.GetControllerOf(live); ref != nil && !sameControllerOwner(owner, ref) {
		return fmt.Errorf("pending Configuration target %s has a different controller", client.ObjectKeyFromObject(live))
	}
	if err := syncConfigurationOwnerReference(ctx, cli, owner, live, entry); err != nil {
		return err
	}
	entry.ResourceUID = live.GetUID()
	return finishConfigurationWrite(ctx, cli, owner, &entry)
}

func commitConfigurationInventory(ctx context.Context, cli client.Client, owner client.Object, current []v1.RenderedConfigurationResource) error {
	p := pendingConfigurationWrite(owner)
	if p == nil || *p != nil {
		return fmt.Errorf("cannot complete inventory with a pending Configuration write")
	}
	next, ok := owner.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("invalid Configuration owner copy %T", owner)
	}
	entries, generation := configurationInventory(next)
	*entries = current
	*generation = owner.GetGeneration()
	return patchConfigurationCheckpoint(ctx, cli, owner, next)
}
