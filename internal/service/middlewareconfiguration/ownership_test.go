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
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func boolPtr(v bool) *bool {
	return &v
}

func testMiddleware() *v1.Middleware {
	return &v1.Middleware{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis-lxt",
			Namespace: "mv1",
			UID:       types.UID("middleware-uid"),
		},
	}
}

func testConfig(annotations map[string]string) *v1.MiddlewareConfiguration {
	return &v1.MiddlewareConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "redis-sentinel-configmap",
			Annotations: annotations,
		},
	}
}

func testResource(ownerRefs []metav1.OwnerReference, labels map[string]string, annotations map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetName("redis-lxt-sentinel")
	obj.SetNamespace("mv1")
	obj.SetLabels(labels)
	obj.SetAnnotations(annotations)
	obj.SetOwnerReferences(ownerRefs)
	return obj
}

func middlewareOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: v1.GroupVersion.String(),
		Kind:       "Middleware",
		Name:       "redis-lxt",
		UID:        types.UID("middleware-uid"),
		Controller: boolPtr(true),
	}
}

func redisClusterOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "redis.middleware.hc.cn/v1alpha1",
		Kind:       "RedisCluster",
		Name:       "redis-lxt",
		UID:        types.UID("rediscluster-uid"),
		Controller: boolPtr(true),
	}
}

func TestShouldSetControllerReference(t *testing.T) {
	t.Parallel()
	owner := testMiddleware()
	desired := testResource(nil, nil, nil)

	tests := []struct {
		name    string
		old     metav1.Object
		cfg     *v1.MiddlewareConfiguration
		want    bool
		wantErr bool
	}{
		{
			name: "new resource is owned by OpenSaola",
			cfg:  testConfig(nil),
			want: true,
		},
		{
			name: "existing current Middleware-owned resource keeps owner",
			old:  testResource([]metav1.OwnerReference{middlewareOwnerRef()}, nil, nil),
			cfg:  testConfig(nil),
			want: true,
		},
		{
			name: "existing external-controller resource is patched without taking owner by default",
			old:  testResource([]metav1.OwnerReference{redisClusterOwnerRef()}, nil, nil),
			cfg:  testConfig(nil),
			want: false,
		},
		{
			name: "managed policy refuses external-controller resource",
			old:  testResource([]metav1.OwnerReference{redisClusterOwnerRef()}, nil, nil),
			cfg: testConfig(map[string]string{
				v1.AnnotationConfigurationOwnershipPolicy: v1.ConfigurationOwnershipPolicyManaged,
			}),
			want:    false,
			wantErr: true,
		},
		{
			name: "existing resource without controller can be adopted",
			old:  testResource(nil, nil, nil),
			cfg:  testConfig(nil),
			want: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := shouldSetControllerReference(owner, tt.old, tt.cfg, desired)
			if (err != nil) != tt.wantErr {
				t.Fatalf("unexpected error state: got %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("shouldSetControllerReference() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldDeleteRenderedResource(t *testing.T) {
	t.Parallel()
	owner := testMiddleware()
	managedLabels := map[string]string{v1.LabelApp: "redis-lxt"}
	managedAnnotations := map[string]string{v1.LabelConfigurations: "redis-sentinel-configmap"}

	tests := []struct {
		name         string
		obj          metav1.Object
		deletePolicy string
		want         bool
	}{
		{
			name: "current Middleware-owned resource is deleted",
			obj:  testResource([]metav1.OwnerReference{middlewareOwnerRef()}, nil, nil),
			want: true,
		},
		{
			name: "OpenSaola-marked cluster-scoped style resource without owner is deleted",
			obj:  testResource(nil, managedLabels, managedAnnotations),
			want: true,
		},
		{
			name: "external-controller resource is not deleted even with OpenSaola marks",
			obj:  testResource([]metav1.OwnerReference{redisClusterOwnerRef()}, managedLabels, managedAnnotations),
			want: false,
		},
		{
			name:         "explicit delete policy can delete external-controller resource",
			obj:          testResource([]metav1.OwnerReference{redisClusterOwnerRef()}, managedLabels, managedAnnotations),
			deletePolicy: v1.ConfigurationDeletePolicyDelete,
			want:         true,
		},
		{
			name:         "orphan policy skips Middleware-owned resource",
			obj:          testResource([]metav1.OwnerReference{middlewareOwnerRef()}, nil, nil),
			deletePolicy: v1.ConfigurationDeletePolicyOrphan,
			want:         false,
		},
		{
			name: "unmarked resource without owner is not deleted",
			obj:  testResource(nil, nil, nil),
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldDeleteRenderedResource(owner, tt.obj, "redis-sentinel-configmap", tt.deletePolicy)
			if got != tt.want {
				t.Fatalf("shouldDeleteRenderedResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigurationPolicyObjectOverridesConfiguration(t *testing.T) {
	t.Parallel()
	cfg := testConfig(map[string]string{
		v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyOrphan,
	})
	obj := testResource(nil, nil, map[string]string{
		v1.AnnotationConfigurationDeletePolicy: v1.ConfigurationDeletePolicyDelete,
	})

	got := configurationPolicy(cfg, obj, v1.AnnotationConfigurationDeletePolicy)
	if got != v1.ConfigurationDeletePolicyDelete {
		t.Fatalf("configurationPolicy() = %q, want %q", got, v1.ConfigurationDeletePolicyDelete)
	}
}
