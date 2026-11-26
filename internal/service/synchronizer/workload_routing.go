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

package synchronizer

import (
	"strings"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

// routeSources holds the identities last read by a SyncV2 session. An empty
// registration keeps routing conservative until its first successful read.
type routeSources struct {
	mid, primary               ownerCandidate
	disasterSyncer, dataSyncer string
	readStarted                time.Time
}

// Keep the full group/version/kind/name so same-named resources cannot collide.
func routedReference(resource string) string {
	last := strings.LastIndexByte(resource, '/')
	if last < 0 || last == len(resource)-1 {
		return ""
	}
	switch resource[:last] {
	case "/v1/Pod", "apps/v1/Deployment", "apps/v1/ReplicaSet":
		return resource
	}
	return ""
}

// NotifyWorkloadReferenceChange refreshes a MID when it starts or stops observing
// a routed resource by annotation, even if that resource has no new event.
func NotifyWorkloadReferenceChange(oldMID, newMID *v1.Middleware) {
	for _, key := range []string{v1.AnnotationDisasterSyncer, v1.AnnotationDataSyncer} {
		oldRef, newRef := oldMID.Annotations[key], newMID.Annotations[key]
		if oldRef != newRef && (routedReference(oldRef) != "" || routedReference(newRef) != "") {
			NotifyMiddleware(newMID.Namespace, newMID.Name)
			return
		}
	}
}

func (m *NsInformerManager) recordRouteSources(ns, lease string, sources routeSources) {
	raw, ok := m.entries.Load(ns)
	if !ok {
		return
	}
	entry, ok := raw.(*nsEntry)
	if !ok {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	prev, active := entry.midKeys[lease]
	if !active || entry.stopped || sources.readStarted.Before(prev.readStarted) {
		return
	}
	// Initial and debounced reads can overlap. An older callback must not
	// put back an owner UID or explicit reference that a newer read replaced.
	prev.readStarted = sources.readStarted
	entry.midKeys[lease] = sources
	if prev != sources {
		entry.routing = nil
	}
}

// Each namespace publishes an immutable index for routing workload events
// without scanning the other sessions or calling the API.
type routeView struct {
	namespace  string
	factory    informers.SharedInformerFactory
	mids       sets.Set[string]
	owners     map[types.UID][]string
	identities map[ownerCandidate]types.UID // keyed by version, kind and name, with UID cleared
	syncerRefs map[string][]string
}

func (e *nsEntry) routeSnapshot(ns string) (routeView, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || !e.synced.Load() {
		return routeView{}, false
	}
	if e.routing == nil {
		// Cache an incomplete view too. A registration or source change
		// invalidates it, so startup traffic need not rebuild it per event.
		view, _ := newRouteView(ns, e.factory, e.midKeys)
		e.routing = &view
	}
	return *e.routing, len(e.routing.mids) > 0
}

func newRouteView(ns string, factory informers.SharedInformerFactory, registrations map[string]routeSources) (routeView, bool) {
	v := routeView{
		namespace:  ns,
		factory:    factory,
		mids:       sets.New[string](),
		owners:     make(map[types.UID][]string),
		identities: make(map[ownerCandidate]types.UID),
		syncerRefs: make(map[string][]string),
	}
	for _, sources := range registrations {
		name := sources.mid.Name
		if name == "" || v.mids.Has(name) {
			return routeView{}, false
		}
		v.mids.Insert(name)
		for _, id := range []ownerCandidate{sources.mid, sources.primary} {
			uid := id.UID
			id.UID = ""
			if uid == "" || id.APIVersion == "" || id.Kind == "" || id.Name == "" {
				return routeView{}, false
			}
			if other, found := v.identities[id]; found && uid != other {
				return routeView{}, false
			}
			v.identities[id] = uid
			v.owners[uid] = append(v.owners[uid], name)
		}
		for _, ref := range []string{sources.disasterSyncer, sources.dataSyncer} {
			if ref != "" {
				v.syncerRefs[ref] = append(v.syncerRefs[ref], name)
			}
		}
	}
	return v, len(registrations) > 0
}

func newWorkloadEventHandler(ns string, snapshot func() (routeView, bool),
	notify func(string, sets.Set[string]) bool, broadcast func(string)) cache.ResourceEventHandlerFuncs {
	route := func(objects ...any) {
		view, ready := snapshot()
		if !ready || view.namespace != ns {
			broadcast(ns)
			return
		}
		targets := sets.New[string]()
		for _, obj := range objects {
			if !view.addEventTargets(obj, targets) {
				broadcast(ns)
				return
			}
		}
		if !notify(ns, targets) {
			broadcast(ns)
		}
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { route(obj) },
		UpdateFunc: func(oldObj, newObj any) { route(oldObj, newObj) },
		DeleteFunc: func(obj any) { route(obj) },
	}
}

func (v routeView) addEventTargets(obj any, targets sets.Set[string]) bool {
	switch tomb := obj.(type) {
	case cache.DeletedFinalStateUnknown:
		obj = tomb.Obj
	case *cache.DeletedFinalStateUnknown:
		if tomb == nil {
			return false
		}
		obj = tomb.Obj
	}
	// Typed informer objects may have no TypeMeta. Use their concrete type.
	switch obj := obj.(type) {
	case *corev1.Pod:
		return obj != nil && v.addWorkloadTargets(&obj.ObjectMeta, corev1.SchemeGroupVersion.WithKind("Pod"), targets)
	case *appsv1.Deployment:
		return obj != nil && v.addWorkloadTargets(&obj.ObjectMeta, appsv1.SchemeGroupVersion.WithKind("Deployment"), targets)
	case *appsv1.ReplicaSet:
		return obj != nil && v.addWorkloadTargets(&obj.ObjectMeta, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"), targets)
	default:
		return false
	}
}

func (v routeView) addWorkloadTargets(obj *metav1.ObjectMeta, gvk schema.GroupVersionKind, targets sets.Set[string]) bool {
	if obj.Namespace != v.namespace {
		return false
	}
	if gvk.Group == appsv1.GroupName {
		if v.factory == nil {
			return false
		}
		// A delete can arrive after eviction. Use the event's metadata unless
		// the cache already contains a replacement with the same name.
		current, err := v.cachedWorkload(gvk.Kind, obj.Name)
		if err != nil && !apierrors.IsNotFound(err) {
			return false
		}
		if err == nil && (current == nil || current.GetUID() != obj.UID) {
			return false
		}
	}
	// Resolve each side of an update independently before merging recipients.
	local := sets.New[string]()
	self := metav1.OwnerReference{APIVersion: gvk.GroupVersion().String(), Kind: gvk.Kind, Name: obj.Name, UID: obj.UID}
	if _, valid := v.addRoots(self, local); !valid {
		return false
	}
	local.Insert(v.syncerRefs[gvk.Group+"/"+gvk.Version+"/"+gvk.Kind+"/"+obj.Name]...)
	// SyncV2 collects ReplicaSets by their owning Deployment, not by RS labels.
	if gvk.Kind != "ReplicaSet" {
		v.addLabels(obj, local)
	}
	for _, ref := range obj.OwnerReferences {
		if !v.addOwner(ref, local, sets.New[types.UID]()) {
			return false
		}
	}
	if local.Len() == 0 {
		return false
	}
	for name := range local {
		targets.Insert(name)
	}
	return true
}

func (v routeView) addLabels(obj metav1.Object, names sets.Set[string]) bool {
	matched := false
	for _, key := range GeneralLabelKeys {
		name := obj.GetLabels()[key]
		if v.mids.Has(name) {
			names.Insert(name)
			matched = true
		}
	}
	return matched
}

func (v routeView) addRoots(ref metav1.OwnerReference, names sets.Set[string]) (matched, valid bool) {
	if ref.UID == "" || ref.Name == "" || ref.Kind == "" || ref.APIVersion == "" {
		return false, false
	}
	id := ownerCandidate{APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name}
	if uid, exists := v.identities[id]; exists && uid != ref.UID {
		return false, false
	}
	owners := v.owners[ref.UID]
	names.Insert(owners...)
	return len(owners) > 0, true
}

func (v routeView) addOwner(ref metav1.OwnerReference, names sets.Set[string], path sets.Set[types.UID]) bool {
	root, valid := v.addRoots(ref, names)
	if !valid || path.Has(ref.UID) {
		return false
	}
	if ref.APIVersion != appsv1.SchemeGroupVersion.String() {
		return root
	}
	path.Insert(ref.UID)
	defer path.Delete(ref.UID)
	if v.factory == nil {
		return false
	}
	parent, err := v.cachedWorkload(ref.Kind, ref.Name)
	if err != nil {
		return false
	}
	if parent == nil {
		return root
	}
	if parent.GetUID() != ref.UID {
		return false
	}
	if ref.Kind != "ReplicaSet" && v.addLabels(parent, names) {
		root = true
	}
	parents := parent.GetOwnerReferences()
	for _, ancestor := range parents {
		if !v.addOwner(ancestor, names, path) {
			return false
		}
	}
	return root || len(parents) > 0
}

// cachedWorkload returns nil for kinds outside the namespace's apps informers.
func (v routeView) cachedWorkload(kind, name string) (metav1.Object, error) {
	apps := v.factory.Apps().V1()
	switch kind {
	case "StatefulSet":
		return apps.StatefulSets().Lister().StatefulSets(v.namespace).Get(name)
	case "DaemonSet":
		return apps.DaemonSets().Lister().DaemonSets(v.namespace).Get(name)
	case "ReplicaSet":
		return apps.ReplicaSets().Lister().ReplicaSets(v.namespace).Get(name)
	case "Deployment":
		return apps.Deployments().Lister().Deployments(v.namespace).Get(name)
	default:
		return nil, nil
	}
}

// Check the entire set under one registry lock before sending the first notification.
// A disappearing target therefore results in one broadcast, without partial delivery.
func (r *NsDebounceRegistry) notifyRegisteredMIDs(ns string, targets sets.Set[string]) bool {
	if targets.Len() == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name := range targets {
		if r.debouncers[registryKey(ns, name)] == nil {
			return false
		}
	}
	for name := range targets {
		r.debouncers[registryKey(ns, name)].Notify()
	}
	return true
}
