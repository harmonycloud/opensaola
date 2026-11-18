//go:build envtest

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

package middlewareaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/harmonycloud/opensaola/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestOneOffActionPreservesFieldOwnersAndRejectsStaleEdit(t *testing.T) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	cli, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := k8s.NewIntentSchemaResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	k8s.ConfigureResourceIntents(resolver)
	t.Cleanup(func() { k8s.ConfigureResourceIntents(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "action-edit-"}}
	if err := cli.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	target := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "fixture", "namespace": ns.Name}}}
	first := target.DeepCopy()
	first.Object["data"] = map[string]any{"external": "keep", "setting": "before"}
	first.SetAnnotations(map[string]string{k8s.AnnotationResourceIntent: "current"})
	if err := cli.Patch(ctx, first, client.Apply, client.FieldOwner("external")); err != nil {
		t.Fatal(err)
	}
	stale := first.DeepCopy()
	if err := editActionResource(ctx, cli, target, nil, nil, map[string]any{"data": map[string]any{"setting": "updated"}}); err != nil {
		t.Fatal(err)
	}
	staleNew := stale.DeepCopy()
	staleNew.Object["data"] = map[string]any{"external": "wrong", "setting": "before"}
	if err := k8s.PatchActionResource(ctx, cli, stale, staleNew); err == nil {
		t.Fatal("stale action accepted")
	}
	current := target.DeepCopy()
	if err := cli.Get(ctx, client.ObjectKeyFromObject(target), current); err != nil {
		t.Fatal(err)
	}
	data, _, err := unstructured.NestedStringMap(current.Object, "data")
	if err != nil || data["external"] != "keep" || data["setting"] != "updated" || current.GetAnnotations()[k8s.AnnotationResourceIntent] != "current" {
		t.Fatalf("unexpected result: %v, %v", data, err)
	}
	actionFound, foreignFound := false, false
	for _, entry := range current.GetManagedFields() {
		if entry.FieldsV1 == nil {
			continue
		}
		var fields map[string]map[string]any
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			t.Fatal(err)
		}
		owned := fields["f:data"]
		switch entry.Manager {
		case "opensaola-action":
			actionFound = true
			if entry.Operation != metav1.ManagedFieldsOperationUpdate || owned["f:setting"] == nil || owned["f:external"] != nil {
				t.Fatalf("unexpected action ownership %s", entry.FieldsV1.Raw)
			}
		case "external":
			foreignFound = owned["f:external"] != nil
		}
	}
	if !actionFound || !foreignFound {
		t.Fatalf("lost ownership: action=%v external=%v", actionFound, foreignFound)
	}
	t.Run("one volume mounted at two paths", func(t *testing.T) {
		deploy := &unstructured.Unstructured{}
		if err := json.Unmarshal([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"broker"},"spec":{"selector":{"matchLabels":{"app":"broker"}},"template":{"metadata":{"labels":{"app":"broker"}},"spec":{"volumes":[{"name":"logs","emptyDir":{}}],"containers":[{"name":"broker","image":"busybox","args":["existing"],"volumeMounts":[{"name":"logs","mountPath":"/logs"},{"name":"logs","mountPath":"/audit"}]}]}}}}`), &deploy.Object); err != nil {
			t.Fatal(err)
		}
		deploy.SetNamespace(ns.Name)
		if err := cli.Create(ctx, deploy); err != nil {
			t.Fatal(err)
		}
		editMount := func(mount map[string]any) map[string]any {
			container := map[string]any{"name": "broker", "args": []any{}, "volumeMounts": []any{mount}}
			return map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{container}}}}}
		}
		ambiguous := editMount(map[string]any{"name": "logs", "readOnly": true})
		if err := editActionResource(ctx, cli, deploy, nil, nil, ambiguous); err == nil || !strings.Contains(err.Error(), "supply mountPath") {
			t.Fatalf("wanted an explicit mountPath error, got %v", err)
		}
		unchanged := &appsv1.Deployment{}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(deploy), unchanged); err != nil || unchanged.ResourceVersion != deploy.GetResourceVersion() {
			t.Fatalf("ambiguous edit wrote to server: %v", err)
		}
		change := editMount(map[string]any{"name": "logs", "mountPath": "/audit", "readOnly": true})
		if err := editActionResource(ctx, cli, deploy, nil, nil, change); err != nil {
			t.Fatal(err)
		}
		actual := &appsv1.Deployment{}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(deploy), actual); err != nil {
			t.Fatal(err)
		}
		if args := actual.Spec.Template.Spec.Containers[0].Args; len(args) != 1 || args[0] != "existing" {
			t.Fatalf("empty action overlay changed args: %v", args)
		}
		mounts := actual.Spec.Template.Spec.Containers[0].VolumeMounts
		if len(mounts) != 2 || mounts[0].MountPath != "/logs" || mounts[0].ReadOnly || mounts[1].MountPath != "/audit" || !mounts[1].ReadOnly {
			t.Fatalf("unexpected mounts: %v", mounts)
		}
		// Give /audit its own backing volume, then edit it by that unique name.
		actual.Spec.Template.Spec.Volumes = append(actual.Spec.Template.Spec.Volumes, corev1.Volume{Name: "audit", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		if err := cli.Update(ctx, actual); err != nil {
			t.Fatal(err)
		}
		if err := editActionResource(ctx, cli, deploy, nil, nil, editMount(map[string]any{"name": "audit", "mountPath": "/audit"})); err != nil {
			t.Fatal(err)
		}
		if err := editActionResource(ctx, cli, deploy, nil, nil, editMount(map[string]any{"name": "audit", "readOnly": false})); err != nil {
			t.Fatal(err)
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(deploy), actual); err != nil {
			t.Fatal(err)
		}
		mounts = actual.Spec.Template.Spec.Containers[0].VolumeMounts
		if len(mounts) != 2 || mounts[0].Name != "logs" || mounts[0].MountPath != "/logs" || mounts[0].ReadOnly || mounts[1].Name != "audit" || mounts[1].MountPath != "/audit" || mounts[1].ReadOnly {
			t.Fatalf("unique name edit affected unexpected mounts: %v", mounts)
		}

	})
}
