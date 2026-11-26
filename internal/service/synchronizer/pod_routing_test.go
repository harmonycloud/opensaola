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
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

const podRoutingNamespace = "pod-routing"

func podRoutingOwner(apiVersion, kind, name, uid string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(uid)}
}

func podRoutingMeta(name, uid, label string, owners ...metav1.OwnerReference) metav1.ObjectMeta {
	m := metav1.ObjectMeta{Namespace: podRoutingNamespace, Name: name, UID: types.UID(uid), OwnerReferences: owners}
	if label != "" {
		m.Labels = map[string]string{"middleware.cn/app": label}
	}
	return m
}

func podRoutingPod(label string, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: podRoutingMeta("server-0", "pod-uid", label, owners...)}
}

func podRoutingFixture(t *testing.T) (informers.SharedInformerFactory, map[string]podSources) {
	t.Helper()
	factory := informers.NewSharedInformerFactory(fake.NewSimpleClientset(), 0)
	sources := map[string]podSources{}
	for _, name := range []string{"mid-a", "mid-b"} {
		sources[name+"#1"] = podSources{
			mid:     ownerCandidate{APIVersion: "middleware.cn/v1", Kind: "Middleware", Name: name, UID: types.UID(name)},
			primary: ownerCandidate{APIVersion: "example.test/v1", Kind: "Cluster", Name: name + "-primary", UID: types.UID("cr-" + name)},
		}
	}
	primary := podRoutingOwner("example.test/v1", "Cluster", "mid-a-primary", "cr-mid-a")
	deployment := podRoutingOwner("apps/v1", "Deployment", "exporter", "deployment-uid")
	apps := factory.Apps().V1()
	for _, item := range []struct {
		store cache.Store
		obj   any
	}{
		{apps.StatefulSets().Informer().GetStore(), &appsv1.StatefulSet{ObjectMeta: podRoutingMeta("server", "sts-uid", "", primary)}},
		{apps.Deployments().Informer().GetStore(), &appsv1.Deployment{ObjectMeta: podRoutingMeta("exporter", "deployment-uid", "", primary)}},
		{apps.ReplicaSets().Informer().GetStore(), &appsv1.ReplicaSet{ObjectMeta: podRoutingMeta("exporter-123", "rs-uid", "", deployment)}},
		{apps.DaemonSets().Informer().GetStore(), &appsv1.DaemonSet{ObjectMeta: podRoutingMeta("agent", "ds-uid", "mid-b")}},
	} {
		if err := item.store.Add(item.obj); err != nil {
			t.Fatal(err)
		}
	}
	return factory, sources
}

// Assert recipient sets at the handler boundary. Informer stores are populated
// directly, so these tests need neither an API server nor timing-based waits.
func assertPodRoutingEvent(t *testing.T, view podRouteView, ready bool, event func(cache.ResourceEventHandlerFuncs), want []string, wantBroadcast bool) {
	t.Helper()
	var got sets.Set[string]
	notifications, broadcasts := 0, 0
	handler := newPodEventHandler(podRoutingNamespace,
		func() (podRouteView, bool) { return view, ready },
		func(ns string, targets sets.Set[string]) bool {
			if ns != podRoutingNamespace {
				t.Errorf("notification namespace = %q", ns)
			}
			notifications++
			got = targets.Clone()
			return true
		},
		func(ns string) {
			if ns != podRoutingNamespace {
				t.Errorf("broadcast namespace = %q", ns)
			}
			broadcasts++
		})
	event(handler)
	if wantBroadcast {
		if broadcasts != 1 || notifications != 0 {
			t.Fatalf("broadcasts=%d, targeted deliveries=%d; want one broadcast only", broadcasts, notifications)
		}
		return
	}
	if broadcasts != 0 || notifications != 1 || !got.Equal(sets.New(want...)) {
		t.Fatalf("broadcasts=%d, targeted deliveries=%d, recipients=%v; want %v", broadcasts, notifications, sets.List(got), want)
	}
}

func TestPodRoutingEvents(t *testing.T) {
	factory, sources := podRoutingFixture(t)
	view, ready := newPodRouteView(podRoutingNamespace, factory, sources)
	if !ready {
		t.Fatal("complete live identities must be routable")
	}
	add := func(obj any) func(cache.ResourceEventHandlerFuncs) {
		return func(h cache.ResourceEventHandlerFuncs) { h.OnAdd(obj, false) }
	}
	update := func(oldObj, newObj any) func(cache.ResourceEventHandlerFuncs) {
		return func(h cache.ResourceEventHandlerFuncs) { h.OnUpdate(oldObj, newObj) }
	}
	deleteEvent := func(obj any) func(cache.ResourceEventHandlerFuncs) {
		return func(h cache.ResourceEventHandlerFuncs) { h.OnDelete(obj) }
	}
	a, b := podRoutingPod("mid-a"), podRoutingPod("mid-b")
	status := a.DeepCopy()
	status.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	shared := a.DeepCopy()
	shared.Labels["app.kubernetes.io/instance"] = "mid-b"
	sts := podRoutingOwner("apps/v1", "StatefulSet", "server", "sts-uid")
	rs := podRoutingOwner("apps/v1", "ReplicaSet", "exporter-123", "rs-uid")
	primary := podRoutingOwner("example.test/v1", "Cluster", "mid-a-primary", "cr-mid-a")
	oldSTS := sts
	oldSTS.UID = "old-sts-uid"
	oldPrimary := primary
	oldPrimary.UID = "old-primary-uid"
	wrongNS := a.DeepCopy()
	wrongNS.Namespace = "another-namespace"
	for _, tc := range []struct {
		name      string
		event     func(cache.ResourceEventHandlerFuncs)
		want      []string
		broadcast bool
		cold      bool
	}{
		{name: "labeled_add", event: add(a), want: []string{"mid-a"}},
		{name: "status_update", event: update(a, status), want: []string{"mid-a"}},
		{name: "label_migration_notifies_old_and_new", event: update(a, b), want: []string{"mid-a", "mid-b"}},
		{name: "owner_migration_notifies_old_and_new", event: update(podRoutingPod("", sts), podRoutingPod("", podRoutingOwner("apps/v1", "DaemonSet", "agent", "ds-uid"))), want: []string{"mid-a", "mid-b"}},
		{name: "delete_uses_old_pod", event: deleteEvent(a), want: []string{"mid-a"}},
		{name: "delete_tombstone", event: deleteEvent(cache.DeletedFinalStateUnknown{Key: podRoutingNamespace + "/server-0", Obj: a}), want: []string{"mid-a"}},
		{name: "delete_pointer_tombstone", event: deleteEvent(&cache.DeletedFinalStateUnknown{Key: podRoutingNamespace + "/server-0", Obj: a}), want: []string{"mid-a"}},
		{name: "direct_mid_owner", event: add(podRoutingPod("", podRoutingOwner("middleware.cn/v1", "Middleware", "mid-a", "mid-a"))), want: []string{"mid-a"}},
		{name: "direct_primary_owner", event: add(podRoutingPod("", primary)), want: []string{"mid-a"}},
		{name: "statefulset_owner_without_labels_or_typemeta", event: add(podRoutingPod("", sts)), want: []string{"mid-a"}},
		{name: "deployment_replicaset_chain_without_labels_or_typemeta", event: add(podRoutingPod("", rs)), want: []string{"mid-a"}},
		{name: "daemonset_label_owner", event: add(podRoutingPod("", podRoutingOwner("apps/v1", "DaemonSet", "agent", "ds-uid"))), want: []string{"mid-b"}},
		{name: "shared_labels", event: add(shared), want: []string{"mid-a", "mid-b"}},
		{name: "owner_and_label_share_pod", event: add(podRoutingPod("mid-b", sts)), want: []string{"mid-a", "mid-b"}},
		{name: "same_name_old_workload_uid", event: add(podRoutingPod("mid-a", oldSTS)), broadcast: true},
		{name: "same_name_old_primary_uid", event: add(podRoutingPod("mid-a", oldPrimary)), broadcast: true},
		{name: "unknown_owner_despite_known_label", event: add(podRoutingPod("mid-a", podRoutingOwner("example.test/v1", "Unknown", "unknown", "unknown-uid"))), broadcast: true},
		{name: "workload_missing_from_cache", event: add(podRoutingPod("", podRoutingOwner("apps/v1", "StatefulSet", "missing", "missing-uid"))), broadcast: true},
		{name: "new_side_loses_identity", event: update(a, podRoutingPod("")), broadcast: true},
		{name: "old_side_unresolved", event: update(podRoutingPod(""), b), broadcast: true},
		{name: "cold_cache", event: add(a), cold: true, broadcast: true},
		{name: "different_namespace", event: add(wrongNS), broadcast: true},
		{name: "unknown_event_object", event: add(&corev1.ConfigMap{}), broadcast: true},
		{name: "delete_without_snapshot", event: deleteEvent(cache.DeletedFinalStateUnknown{Key: podRoutingNamespace + "/server-0"}), broadcast: true},
		{name: "nil_tombstone", event: deleteEvent((*cache.DeletedFinalStateUnknown)(nil)), broadcast: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertPodRoutingEvent(t, view, !tc.cold, tc.event, tc.want, tc.broadcast)
		})
	}
	for _, label := range GeneralLabelKeys {
		t.Run("general_label_"+label, func(t *testing.T) {
			pod := podRoutingPod("")
			pod.Labels = map[string]string{label: "mid-a"}
			assertPodRoutingEvent(t, view, true, add(pod), []string{"mid-a"}, false)
		})
	}
}

func TestPodRoutingNativePrimary(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			factory, sources := podRoutingFixture(t)
			source := sources["mid-a#1"]
			source.primary = ownerCandidate{APIVersion: "apps/v1", Kind: kind, Name: "native", UID: "native-uid"}
			pod := podRoutingPod("", podRoutingOwner("apps/v1", kind, "native", "native-uid"))
			apps := factory.Apps().V1()
			var store cache.Store
			var obj any
			switch kind {
			case "Deployment":
				store = apps.Deployments().Informer().GetStore()
				obj = &appsv1.Deployment{ObjectMeta: podRoutingMeta("native", "native-uid", "")}
				rs := &appsv1.ReplicaSet{ObjectMeta: podRoutingMeta("native-rs", "native-rs-uid", "", pod.OwnerReferences...)}
				if err := apps.ReplicaSets().Informer().GetStore().Add(rs); err != nil {
					t.Fatal(err)
				}
				pod.OwnerReferences = []metav1.OwnerReference{podRoutingOwner("apps/v1", "ReplicaSet", rs.Name, string(rs.UID))}
			case "StatefulSet":
				store = apps.StatefulSets().Informer().GetStore()
				obj = &appsv1.StatefulSet{ObjectMeta: podRoutingMeta("native", "native-uid", "")}
			case "DaemonSet":
				store = apps.DaemonSets().Informer().GetStore()
				obj = &appsv1.DaemonSet{ObjectMeta: podRoutingMeta("native", "native-uid", "")}
			case "Pod":
				source.primary.APIVersion = "v1"
				pod.ObjectMeta = podRoutingMeta("native", "native-uid", "")
			}
			if store != nil {
				if err := store.Add(obj); err != nil {
					t.Fatal(err)
				}
			}
			sources["mid-a#1"] = source
			view, ready := newPodRouteView(podRoutingNamespace, factory, sources)
			assertPodRoutingEvent(t, view, ready, func(h cache.ResourceEventHandlerFuncs) { h.OnAdd(pod, false) }, []string{"mid-a"}, false)
		})
	}
}

func TestPodRoutingRegisteredTargetsAreAtomic(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "all_targets_registered"
		if missing {
			name = "one_target_disappeared"
		}
		t.Run(name, func(t *testing.T) {
			registry := &NsDebounceRegistry{debouncers: map[string]*Debouncer{}}
			t.Cleanup(registry.StopAll)
			registered := map[string]*Debouncer{}
			for _, mid := range []string{"mid-a", "mid-b", "unrelated"} {
				if missing && mid == "mid-b" {
					continue
				}
				d := registry.Register(podRoutingNamespace, mid, func() {})
				d.window = time.Hour
				registered[mid] = d
			}
			if got := registry.notifyRegisteredMIDs(podRoutingNamespace, sets.New("mid-a", "mid-b")); got == missing {
				t.Fatalf("delivery accepted=%v, missing target=%v", got, missing)
			}
			for mid, d := range registered {
				d.mu.Lock()
				pending := d.pending
				d.mu.Unlock()
				if want := !missing && mid != "unrelated"; pending != want {
					t.Errorf("%s scheduled=%v, want %v", mid, pending, want)
				}
			}
		})
	}
}

func TestPodRoutingReferencedPodCanBeShared(t *testing.T) {
	for _, reference := range []string{"disaster", "data"} {
		t.Run(reference, func(t *testing.T) {
			factory, sources := podRoutingFixture(t)
			a := sources["mid-a#1"]
			if reference == "disaster" {
				a.disasterPod = "server-0"
			} else {
				a.dataPod = "server-0"
			}
			sources["mid-a#1"] = a
			view, ready := newPodRouteView(podRoutingNamespace, factory, sources)
			assertPodRoutingEvent(t, view, ready, func(h cache.ResourceEventHandlerFuncs) {
				h.OnAdd(podRoutingPod("mid-b"), false)
			}, []string{"mid-a", "mid-b"}, false)
		})
	}
}

func TestPodRoutingSnapshotFollowsLease(t *testing.T) {
	factory, sources := podRoutingFixture(t)
	entry := &nsEntry{factory: factory, midKeys: map[string]podSources{}}
	manager := &NsInformerManager{}
	manager.entries.Store(podRoutingNamespace, entry)
	entry.addRegistration("mid-a#1")
	manager.recordPodSources(podRoutingNamespace, "mid-a#1", sources["mid-a#1"])
	if _, ready := entry.podRouteSnapshot(podRoutingNamespace); ready {
		t.Fatal("cache synchronization has not finished")
	}
	entry.synced.Store(true)
	if _, ready := entry.podRouteSnapshot(podRoutingNamespace); !ready {
		t.Fatal("synchronized cache and first identity should be ready")
	}

	entry.addRegistration("mid-a#2")
	if _, ready := entry.podRouteSnapshot(podRoutingNamespace); ready {
		t.Fatal("new lease must stay conservative before its first live read")
	}
	replacement := sources["mid-a#1"]
	replacement.mid.UID = "replacement-mid-uid"
	replacement.primary.UID = "replacement-primary-uid"
	replacement.readStarted = time.Unix(100, 0)
	manager.recordPodSources(podRoutingNamespace, "mid-a#2", replacement)
	if _, ready := entry.podRouteSnapshot(podRoutingNamespace); ready {
		t.Fatal("overlapping same-name sessions must stay conservative")
	}
	if removed, stopped, _ := entry.removeRegistration("mid-a#1"); !removed || stopped {
		t.Fatalf("removing old lease: removed=%v stopped=%v", removed, stopped)
	}
	// An old in-flight recompute cannot restore a registration already removed.
	manager.recordPodSources(podRoutingNamespace, "mid-a#1", sources["mid-a#1"])
	// Nor may an older observation in the current lease replace newer sources.
	older := sources["mid-a#1"]
	older.readStarted = replacement.readStarted.Add(-time.Second)
	manager.recordPodSources(podRoutingNamespace, "mid-a#2", older)
	view, ready := entry.podRouteSnapshot(podRoutingNamespace)
	assertPodRoutingEvent(t, view, ready, func(h cache.ResourceEventHandlerFuncs) {
		h.OnAdd(podRoutingPod("", podRoutingOwner("example.test/v1", "Cluster", "mid-a-primary", "replacement-primary-uid")), false)
	}, []string{"mid-a"}, false)
	assertPodRoutingEvent(t, view, ready, func(h cache.ResourceEventHandlerFuncs) {
		h.OnAdd(podRoutingPod("", podRoutingOwner("example.test/v1", "Cluster", "mid-a-primary", "cr-mid-a")), false)
	}, nil, true)
	if removed, _, _ := entry.removeRegistration("mid-a#1"); removed {
		t.Fatal("repeated stale cleanup must not remove the replacement")
	}
	if _, ready := entry.podRouteSnapshot(podRoutingNamespace); !ready {
		t.Fatal("replacement route lost after stale cleanup")
	}
	entry.removeRegistration("mid-a#2")
	if _, ready := entry.podRouteSnapshot(podRoutingNamespace); ready {
		t.Fatal("stopped entry must not provide a route")
	}
}

func TestNotifyPodReferenceChange(t *testing.T) {
	for _, tc := range []struct {
		name, key, oldValue, newValue string
		want                          bool
	}{
		{name: "add_disaster_pod", key: v1.AnnotationDisasterSyncer, newValue: "/v1/Pod/new", want: true},
		{name: "add_data_pod", key: v1.AnnotationDataSyncer, newValue: "/v1/Pod/new", want: true},
		{name: "switch_pod", key: v1.AnnotationDataSyncer, oldValue: "/v1/Pod/old", newValue: "/v1/Pod/new", want: true},
		{name: "remove_pod", key: v1.AnnotationDisasterSyncer, oldValue: "/v1/Pod/old", want: true},
		{name: "switch_to_nonpod", key: v1.AnnotationDisasterSyncer, oldValue: "/v1/Pod/old", newValue: "apps/v1/Deployment/new", want: true},
		{name: "unchanged_reference", key: v1.AnnotationDataSyncer, oldValue: "/v1/Pod/old", newValue: "/v1/Pod/old"},
		{name: "ordinary_status_update"},
		{name: "nonpod_reference_change", key: v1.AnnotationDisasterSyncer, oldValue: "apps/v1/Deployment/old", newValue: "apps/v1/Deployment/new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the global registry intact; own only this unique test key.
			oldMID := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{
				Namespace: "pod-reference-change-test", Name: tc.name,
				Annotations: map[string]string{tc.key: tc.oldValue},
			}}
			newMID := oldMID.DeepCopy()
			newMID.Annotations[tc.key] = tc.newValue
			newMID.ResourceVersion = "2"
			newMID.Status.CustomResources.Phase = "Running"
			d := RegisterDebouncer(newMID.Namespace, newMID.Name, func() {})
			d.window = time.Hour
			t.Cleanup(func() { UnregisterDebouncerIfCurrent(newMID.Namespace, newMID.Name, d) })
			NotifyPodReferenceChange(oldMID, newMID)
			d.mu.Lock()
			pending := d.pending
			d.mu.Unlock()
			if pending != tc.want {
				t.Fatalf("scheduled=%v, want %v", pending, tc.want)
			}
		})
	}
}
