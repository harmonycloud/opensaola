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

	v1 "github.com/harmonycloud/opensaola/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
