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
	"context"
	"testing"
	"testing/synctest"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestScheduleInitialReadinessRecheck(t *testing.T) {
	tests := []struct {
		name         string
		stopBefore   bool
		cancelBefore bool
		wantTrigger  bool
	}{
		{
			name:        "fires at the grace deadline",
			wantTrigger: true,
		},
		{
			name:       "owner stop cancels the recheck",
			stopBefore: true,
		},
		{
			name:         "context cancellation cancels the recheck",
			cancelBefore: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stop := make(chan struct{})
				triggered := make(chan struct{}, 1)
				scheduleInitialReadinessRecheck(ctx, stop, time.Now().Add(time.Minute), func() {
					triggered <- struct{}{}
				})
				synctest.Wait()

				if tt.stopBefore {
					close(stop)
					synctest.Wait()
				}
				if tt.cancelBefore {
					cancel()
					synctest.Wait()
				}
				time.Sleep(time.Minute)
				synctest.Wait()

				select {
				case <-triggered:
					if !tt.wantTrigger {
						t.Fatal("expected owner stop to suppress the grace recheck")
					}
				default:
					if tt.wantTrigger {
						t.Fatal("expected grace deadline to trigger a recheck")
					}
				}
			})
		})
	}
}

func TestInitialReadinessRecheckDeadline_UsesLiveCustomResourceTimestamp(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}
	createdAt := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(gvk)
	live.SetNamespace("middleware")
	live.SetName("demo-minio")
	live.SetGeneration(1)
	live.SetCreationTimestamp(metav1.NewTime(createdAt))

	cli := fake.NewClientBuilder().WithObjects(live).Build()
	desired := live.DeepCopy()
	desired.SetCreationTimestamp(metav1.Time{})

	got, ok := initialReadinessRecheckDeadline(context.Background(), cli, desired)
	if !ok {
		t.Fatal("expected live first-generation custom resource to provide a recheck deadline")
	}
	want := createdAt.Add(initialReadinessGracePeriod)
	if !got.Equal(want) {
		t.Fatalf("recheck deadline = %s, want %s", got, want)
	}
}

func TestHasOwnerUID(t *testing.T) {
	tests := []struct {
		name     string
		refs     []metav1.OwnerReference
		ownerUID types.UID
		want     bool
	}{
		{
			name: "matches immutable uid even when type metadata is unavailable",
			refs: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "demo-exporter",
				UID:        types.UID("deployment-uid"),
			}},
			ownerUID: types.UID("deployment-uid"),
			want:     true,
		},
		{
			name: "rejects same name with different uid",
			refs: []metav1.OwnerReference{{
				Name: "demo-exporter",
				UID:  types.UID("old-deployment-uid"),
			}},
			ownerUID: types.UID("new-deployment-uid"),
			want:     false,
		},
		{
			name:     "rejects empty target uid",
			refs:     []metav1.OwnerReference{{UID: types.UID("deployment-uid")}},
			ownerUID: "",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasOwnerUID(tt.refs, tt.ownerUID); got != tt.want {
				t.Fatalf("hasOwnerUID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOwnerReferencesMatchFollowsDeploymentReplicaSetPodChainWithoutTypeMeta(t *testing.T) {
	deployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-exporter",
			UID:  types.UID("deployment-uid"),
		},
	}
	replicaSet := appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-exporter-abc",
			UID:  types.UID("replicaset-uid"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       deployment.Name,
				UID:        deployment.UID,
			}},
		},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-exporter-abc-12345",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       replicaSet.Name,
				UID:        replicaSet.UID,
			}},
		},
	}

	if deployment.APIVersion != "" || deployment.Kind != "" || replicaSet.APIVersion != "" || replicaSet.Kind != "" {
		t.Fatal("test fixture must model informer LIST objects with empty TypeMeta")
	}
	deploymentCandidates := []ownerCandidate{{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployment.Name,
		UID:        deployment.UID,
	}}
	replicaSets := collectOwnedReplicaSets([]appsv1.ReplicaSet{replicaSet}, deploymentCandidates)
	gotReplicaSet, ok := replicaSets[replicaSet.Name]
	if !ok {
		t.Fatal("expected ReplicaSet to match Deployment by UID")
	}
	podCandidates := []ownerCandidate{{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Name:       gotReplicaSet.Name,
		UID:        gotReplicaSet.UID,
	}}
	pods := collectOwnedPods([]corev1.Pod{pod}, podCandidates, "demo")
	if _, ok := pods[pod.Name]; !ok {
		t.Fatal("expected Pod to match ReplicaSet by UID")
	}
}

func TestOwnerReferencesMatchRejectsRecreatedOwnerWithSameIdentity(t *testing.T) {
	refs := []metav1.OwnerReference{{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "demo-exporter",
		UID:        types.UID("old-deployment-uid"),
	}}

	matched, conflicted := ownerReferencesMatch(refs, ownerCandidate{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       "demo-exporter",
		UID:        types.UID("new-deployment-uid"),
	})
	if matched || !conflicted {
		t.Fatalf("expected recreated owner to conflict, got matched=%v conflicted=%v", matched, conflicted)
	}
}

func TestOwnerReferencesMatchAllowsFallbackForDifferentOwnerIdentity(t *testing.T) {
	refs := []metav1.OwnerReference{{
		APIVersion: "middleware.cn/v1",
		Kind:       "Middleware",
		Name:       "demo",
		UID:        types.UID("middleware-uid"),
	}}

	matched, conflicted := ownerReferencesMatch(refs, ownerCandidate{
		APIVersion: "psmdb.percona.com/v1",
		Kind:       "PerconaServerMongoDB",
		Name:       "demo",
		UID:        types.UID("psmdb-uid"),
	})
	if matched || conflicted {
		t.Fatalf("expected unrelated owner identity to allow fallback, got matched=%v conflicted=%v", matched, conflicted)
	}
}

func TestOwnerReferencesMatchPrefersMiddlewareUIDAmongBaseCandidates(t *testing.T) {
	refs := []metav1.OwnerReference{{
		APIVersion: "middleware.cn/v1",
		Kind:       "Middleware",
		Name:       "demo",
		UID:        types.UID("middleware-uid"),
	}}
	candidates := []ownerCandidate{
		{
			APIVersion: "psmdb.percona.com/v1",
			Kind:       "PerconaServerMongoDB",
			Name:       "demo",
			UID:        types.UID("psmdb-uid"),
		},
		{
			APIVersion: "middleware.cn/v1",
			Kind:       "Middleware",
			Name:       "demo",
			UID:        types.UID("middleware-uid"),
		},
	}

	matched, conflicted := ownerReferencesMatch(refs, candidates...)
	if !matched || conflicted {
		t.Fatalf("expected Middleware UID to match among base candidates, got matched=%v conflicted=%v", matched, conflicted)
	}
}

func TestCollectOwnedPodsRejectsConflictingUIDDespiteMatchingLabel(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:   "demo-exporter-old",
		Labels: map[string]string{"app": "demo"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1",
			Kind:       "ReplicaSet",
			Name:       "demo-exporter",
			UID:        types.UID("old-replicaset-uid"),
		}},
	}}
	candidates := []ownerCandidate{{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Name:       "demo-exporter",
		UID:        types.UID("new-replicaset-uid"),
	}}

	if pods := collectOwnedPods([]corev1.Pod{pod}, candidates, "demo"); len(pods) != 0 {
		t.Fatalf("expected conflicting owner UID to block label fallback, got %#v", pods)
	}
}

func TestCollectOwnedPodsAllowsLabelFallbackForDifferentOwnerIdentity(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:   "demo-hook",
		Labels: map[string]string{"app.kubernetes.io/instance": "demo"},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "batch/v1",
			Kind:       "Job",
			Name:       "demo-hook",
			UID:        types.UID("job-uid"),
		}},
	}}
	candidates := []ownerCandidate{{
		APIVersion: "psmdb.percona.com/v1",
		Kind:       "PerconaServerMongoDB",
		Name:       "demo",
		UID:        types.UID("psmdb-uid"),
	}}

	pods := collectOwnedPods([]corev1.Pod{pod}, candidates, "demo")
	if _, ok := pods[pod.Name]; !ok {
		t.Fatal("expected unrelated owner identity to preserve label fallback")
	}
}

func TestCustomResourcesFromStatusClearsMissingReason(t *testing.T) {
	status := []byte(`{"phase":"Running"}`)

	got, err := customResourcesFromStatus(status)
	if err != nil {
		t.Fatalf("customResourcesFromStatus returned error: %v", err)
	}
	if got.Phase != v1.PhaseRunning {
		t.Fatalf("expected phase %q, got %q", v1.PhaseRunning, got.Phase)
	}
	if got.Reason != "" {
		t.Fatalf("expected missing reason to remain empty, got %q", got.Reason)
	}
}

func TestPhaseFromGenericStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   string
		fallback v1.Phase
		want     v1.Phase
	}{
		{name: "missing phase and state preserves creating", status: `{}`, fallback: v1.PhaseCreating, want: v1.PhaseCreating},
		{name: "empty phase preserves creating", status: `{"phase":""}`, fallback: v1.PhaseCreating, want: v1.PhaseCreating},
		{name: "phase only", status: `{"phase":"Running"}`, fallback: v1.PhaseCreating, want: v1.PhaseRunning},
		{name: "state only", status: `{"state":"Available"}`, fallback: v1.PhaseCreating, want: v1.Phase("Available")},
		{name: "operator initializing state passes through", status: `{"state":"initializing"}`, fallback: v1.PhaseCreating, want: v1.Phase("initializing")},
		{name: "operator ready state passes through", status: `{"state":"ready"}`, fallback: v1.PhaseCreating, want: v1.Phase("ready")},
		{name: "empty state does not override phase", status: `{"phase":"Running","state":""}`, fallback: v1.PhaseCreating, want: v1.PhaseRunning},
		{name: "state preserves existing precedence over phase", status: `{"phase":"Running","state":"Updating"}`, fallback: v1.PhaseCreating, want: v1.PhaseUpdating},
		{name: "missing phase and state preserves running", status: `{}`, fallback: v1.PhaseRunning, want: v1.PhaseRunning},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := phaseFromGenericStatus([]byte(tt.status), tt.fallback); got != tt.want {
				t.Fatalf("phaseFromGenericStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOperatorOwnsCustomResourceStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mid  *v1.Middleware
		want bool
	}{
		{
			name: "nil middleware is not operator managed",
		},
		{
			name: "no operator baseline is not operator managed",
			mid:  &v1.Middleware{},
		},
		{
			name: "complete operator baseline owns status",
			mid: &v1.Middleware{Spec: v1.MiddlewareSpec{OperatorBaseline: v1.OperatorBaseline{
				Name:    "mongodb-operator-standard",
				GvkName: "v1",
			}}},
			want: true,
		},
		{
			name: "partial legacy operator baseline still owns status",
			mid: &v1.Middleware{Spec: v1.MiddlewareSpec{OperatorBaseline: v1.OperatorBaseline{
				Name: "mongodb-operator-standard",
			}}},
			want: true,
		},
		{
			name: "operator GVK reference alone still owns status",
			mid: &v1.Middleware{Spec: v1.MiddlewareSpec{OperatorBaseline: v1.OperatorBaseline{
				GvkName: "v1",
			}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := operatorOwnsCustomResourceStatus(tt.mid); got != tt.want {
				t.Fatalf("operatorOwnsCustomResourceStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPVCBelongsToStatefulSet_UsesSelectorLabels(t *testing.T) {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rocketmq-xwjnamesrv"},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "rocketmq-xwjnamesrv"},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "namesrv-log-volume"},
			}},
		},
	}
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "namesrv-log-volume-rocketmq-xwjnamesrv-0",
			Labels: map[string]string{"app": "rocketmq-xwjnamesrv"},
		},
	}

	if !pvcBelongsToStatefulSet(pvc, sts) {
		t.Fatal("expected PVC to match StatefulSet selector labels and claim template name")
	}
}

func TestPVCBelongsToStatefulSet_UsesTemplateLabelsFallback(t *testing.T) {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-namesrv"},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "demo-namesrv"},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "namesrv-log-volume"},
			}},
		},
	}
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "namesrv-log-volume-demo-namesrv-0",
			Labels: map[string]string{"app": "demo-namesrv"},
		},
	}

	if !pvcBelongsToStatefulSet(pvc, sts) {
		t.Fatal("expected PVC to match StatefulSet template labels when selector is unavailable")
	}
}

func TestPVCBelongsToStatefulSet_HandlesHyphenatedClaimTemplateNames(t *testing.T) {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-namesrv-proxy"},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "demo-namesrv-proxy"},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "namesrv-proxy-log-volume"},
			}},
		},
	}
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "namesrv-proxy-log-volume-demo-namesrv-proxy-0",
			Labels: map[string]string{"app": "demo-namesrv-proxy"},
		},
	}

	if !pvcBelongsToStatefulSet(pvc, sts) {
		t.Fatal("expected PVC with hyphenated claim template name to match StatefulSet")
	}
}

func TestPVCBelongsToStatefulSet_RequiresAllSelectorLabels(t *testing.T) {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-namesrv"},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":       "demo-namesrv",
					"component": "namesrv",
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "namesrv-log-volume"},
			}},
		},
	}
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "namesrv-log-volume-demo-namesrv-0",
			Labels: map[string]string{"app": "demo-namesrv"},
		},
	}

	if pvcBelongsToStatefulSet(pvc, sts) {
		t.Fatal("expected PVC not to match when it misses part of the StatefulSet selector")
	}
}

func TestPVCBelongsToStatefulSet_RejectsUnrelatedPVCs(t *testing.T) {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rocketmq-xwjnamesrv"},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "rocketmq-xwjnamesrv"},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "namesrv-log-volume"},
			}},
		},
	}

	tests := []struct {
		name string
		pvc  corev1.PersistentVolumeClaim
	}{
		{
			name: "label matches but claim name is unrelated",
			pvc: corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "other-volume-rocketmq-xwjnamesrv-0",
					Labels: map[string]string{"app": "rocketmq-xwjnamesrv"},
				},
			},
		},
		{
			name: "claim name matches but selector label is different",
			pvc: corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "namesrv-log-volume-rocketmq-xwjnamesrv-0",
					Labels: map[string]string{"app": "other"},
				},
			},
		},
		{
			name: "missing labels",
			pvc: corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "namesrv-log-volume-rocketmq-xwjnamesrv-0"},
			},
		},
		{
			name: "claim name has extra suffix after ordinal",
			pvc: corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "namesrv-log-volume-rocketmq-xwjnamesrv-0-backup",
					Labels: map[string]string{"app": "rocketmq-xwjnamesrv"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if pvcBelongsToStatefulSet(tt.pvc, sts) {
				t.Fatal("expected PVC not to match StatefulSet")
			}
		})
	}
}
