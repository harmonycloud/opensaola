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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

// podSources is the identity last read by a SyncV2 session. An empty
// registration keeps routing conservative until its first successful read.
type podSources struct {
	mid, primary         ownerCandidate
	disasterPod, dataPod string
	readStarted          time.Time
}

// Disaster and data sync annotations name a resource as group/version/kind/name.
func referencedPod(resource string) string {
	name, ok := strings.CutPrefix(resource, "/v1/Pod/")
	if !ok || strings.Contains(name, "/") {
		return ""
	}
	return name
}

// NotifyPodReferenceChange refreshes a MID when it starts or stops observing a
// Pod by annotation. A Pod can already exist when its reference is added.
func NotifyPodReferenceChange(oldMID, newMID *v1.Middleware) {
	for _, key := range []string{v1.AnnotationDisasterSyncer, v1.AnnotationDataSyncer} {
		if referencedPod(oldMID.Annotations[key]) != referencedPod(newMID.Annotations[key]) {
			NotifyMiddleware(newMID.Namespace, newMID.Name)
			return
		}
	}
}

func (m *NsInformerManager) recordPodSources(ns, lease string, sources podSources) {
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
	// put back an owner UID or Pod reference that a newer read replaced.
	prev.readStarted = sources.readStarted
	entry.midKeys[lease] = sources
	if prev != sources {
		entry.podView = nil
	}
}

// Each namespace publishes an immutable index. Pod events can look up the MID
// names and owner UIDs without scanning the other sessions or calling the API.
type podRouteView struct {
	namespace      string
	factory        informers.SharedInformerFactory
	mids           sets.Set[string]
	owners         map[types.UID][]string
	identities     map[ownerCandidate]types.UID // keyed by version, kind and name, with UID cleared
	referencedPods map[string][]string
}

func (e *nsEntry) podRouteSnapshot(ns string) (podRouteView, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped || !e.synced.Load() {
		return podRouteView{}, false
	}
	if e.podView == nil {
		// Cache an incomplete view too. A registration or source change
		// invalidates it, so startup traffic need not rebuild it per Pod.
		view, _ := newPodRouteView(ns, e.factory, e.midKeys)
		e.podView = &view
	}
	return *e.podView, len(e.podView.mids) > 0
}

func newPodRouteView(ns string, factory informers.SharedInformerFactory, registrations map[string]podSources) (podRouteView, bool) {
	v := podRouteView{
		namespace:      ns,
		factory:        factory,
		mids:           sets.New[string](),
		owners:         make(map[types.UID][]string),
		identities:     make(map[ownerCandidate]types.UID),
		referencedPods: make(map[string][]string),
	}
	for _, sources := range registrations {
		name := sources.mid.Name
		if name == "" || v.mids.Has(name) {
			return podRouteView{}, false
		}
		v.mids.Insert(name)
		for _, id := range []ownerCandidate{sources.mid, sources.primary} {
			uid := id.UID
			id.UID = ""
			if uid == "" || id.APIVersion == "" || id.Kind == "" || id.Name == "" {
				return podRouteView{}, false
			}
			if other, found := v.identities[id]; found && uid != other {
				return podRouteView{}, false
			}
			v.identities[id] = uid
			v.owners[uid] = append(v.owners[uid], name)
		}
		for _, pod := range []string{sources.disasterPod, sources.dataPod} {
			if pod != "" {
				v.referencedPods[pod] = append(v.referencedPods[pod], name)
			}
		}
	}
	return v, len(registrations) > 0
}

func newPodEventHandler(ns string, snapshot func() (podRouteView, bool),
	notify func(string, sets.Set[string]) bool, broadcast func(string)) cache.ResourceEventHandlerFuncs {
	route := func(objects ...any) {
		view, ready := snapshot()
		if !ready || view.namespace != ns {
			broadcast(ns)
			return
		}
		targets := sets.New[string]()
		for _, obj := range objects {
			pod := eventPod(obj)
			if pod == nil || pod.Namespace != ns || !view.addPodTargets(pod, targets) {
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

func eventPod(obj any) *corev1.Pod {
	switch tomb := obj.(type) {
	case cache.DeletedFinalStateUnknown:
		obj = tomb.Obj
	case *cache.DeletedFinalStateUnknown:
		if tomb == nil {
			return nil
		}
		obj = tomb.Obj
	}
	pod, _ := obj.(*corev1.Pod)
	return pod
}

func (v podRouteView) addPodTargets(pod *corev1.Pod, targets sets.Set[string]) bool {
	// Check each side of an update on its own. A resolved old Pod must not
	// hide a new Pod whose owner can no longer be identified (or vice versa).
	local := sets.New[string]()
	self := metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}
	if _, valid := v.addRoots(self, local); !valid {
		return false
	}
	local.Insert(v.referencedPods[pod.Name]...)
	v.addLabels(pod, local)
	for _, ref := range pod.OwnerReferences {
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

func (v podRouteView) addLabels(obj metav1.Object, names sets.Set[string]) bool {
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

func (v podRouteView) addRoots(ref metav1.OwnerReference, names sets.Set[string]) (matched, valid bool) {
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

func (v podRouteView) addOwner(ref metav1.OwnerReference, names sets.Set[string], path sets.Set[types.UID]) bool {
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
	apps := v.factory.Apps().V1()
	var parent metav1.Object
	var err error
	switch ref.Kind {
	case "StatefulSet":
		parent, err = apps.StatefulSets().Lister().StatefulSets(v.namespace).Get(ref.Name)
	case "DaemonSet":
		parent, err = apps.DaemonSets().Lister().DaemonSets(v.namespace).Get(ref.Name)
	case "ReplicaSet":
		parent, err = apps.ReplicaSets().Lister().ReplicaSets(v.namespace).Get(ref.Name)
	case "Deployment":
		parent, err = apps.Deployments().Lister().Deployments(v.namespace).Get(ref.Name)
	default:
		return root
	}
	if err != nil || parent.GetUID() != ref.UID {
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
