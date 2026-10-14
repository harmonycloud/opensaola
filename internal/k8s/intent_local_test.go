//go:build integration

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

package k8s_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/internal/service/middlewareaction"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	"github.com/harmonycloud/opensaola/pkg/tools"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
	crzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"
)

// TestLocalIntentReconcile is intentionally opt-in and refuses non-local or
// non-dedicated contexts. It tests the production writer against a real API
// server and, for Redis, a running one-replica workload with persistent data.
func TestLocalIntentReconcile(t *testing.T) {
	kubeconfig := os.Getenv("OPENSAOLA_LOCAL_INTENT_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set OPENSAOLA_LOCAL_INTENT_KUBECONFIG to a dedicated local kind-opensaola-intent-* cluster")
	}
	// The root logger is process-global and controller-runtime only fulfills it
	// once. A stdout logger remains valid across go test -count=N executions.
	crlog.SetLogger(crzap.New(crzap.WriteTo(os.Stdout), crzap.UseDevMode(true)))
	raw, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw.CurrentContext, "kind-opensaola-intent-") {
		t.Fatal("refusing to test outside a dedicated kind-opensaola-intent-* context")
	}
	cfg, err := clientcmd.NewDefaultClientConfig(*raw, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(cfg.Host)
	if err != nil || (endpoint.Hostname() != "127.0.0.1" && endpoint.Hostname() != "localhost") {
		t.Fatal("refusing a non-loopback API endpoint")
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, v1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := k8s.NewIntentSchemaResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := &k8s.ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
	for _, legacy := range []bool{false, true} {
		name := "new"
		if legacy {
			name = "legacy_create"
		}
		t.Run(name+"_map_keys", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			desired := localIntentObject(owner, "v1", "ConfigMap", "config")
			desired.Object["data"] = map[string]interface{}{"KEEP": "before", "AUTH": "enabled"}
			if legacy {
				old := desired.DeepCopy()
				localIntentMap(t, old.Object["data"])["UNKNOWN"] = "external-or-old"
				if err := cli.Create(ctx, old, client.FieldOwner("manager")); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			localIntentPatch(t, ctx, cli, desired, "manager", `[{"op":"add","path":"/data/EXTERNAL","value":"preserve"}]`)
			desired.Object["data"] = map[string]interface{}{"KEEP": "after"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live := localIntentGet(t, ctx, cli, desired)
			data, _, err := unstructured.NestedStringMap(live.Object, "data")
			if err != nil {
				t.Fatal(err)
			}
			if data["KEEP"] != "after" || data["EXTERNAL"] != "preserve" {
				t.Fatalf("desired/external data not preserved: %v", data)
			}
			if _, found := data["AUTH"]; found {
				t.Fatalf("known previous AUTH key was not deleted: %v", data)
			}
			if legacy && data["UNKNOWN"] != "external-or-old" {
				t.Fatalf("unknown first-update field was deleted: %v", data)
			}
			t.Logf("map transition verified: %v", data)
		})
		t.Run(name+"_redis_runtime", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: owner.Namespace, Name: "redis-data"}}
			if err := json.Unmarshal([]byte(`{"accessModes":["ReadWriteOnce"],"resources":{"requests":{"storage":"64Mi"}}}`), &pvc.Spec); err != nil {
				t.Fatal(err)
			}
			if err := cli.Create(ctx, pvc); err != nil {
				t.Fatal(err)
			}
			desired := localIntentRedis(owner)
			if legacy {
				old := desired.DeepCopy()
				if err := cli.Create(ctx, old, client.FieldOwner("manager")); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "rollout", "status", "deployment/redis", "--timeout=150s")
			key := "opensaola-intent-check-" + string(owner.UID)
			if got := localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "exec", "deployment/redis", "--", "redis-cli", "SET", key, "retained-after-update"); strings.TrimSpace(got) != "OK" {
				t.Fatalf("redis SET: %s", got)
			}
			localIntentPatch(t, ctx, cli, desired, "manager", `[{"op":"add","path":"/spec/template/spec/containers/0/env/-","value":{"name":"EXTERNAL","value":"preserve"}}]`)
			localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "rollout", "status", "deployment/redis", "--timeout=90s")
			containers, _, _ := unstructured.NestedSlice(desired.Object, "spec", "template", "spec", "containers")
			localIntentMap(t, containers[0])["env"] = []interface{}{map[string]interface{}{"name": "KEEP", "value": "after"}}
			if err := unstructured.SetNestedSlice(desired.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				t.Fatal(err)
			}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "rollout", "status", "deployment/redis", "--timeout=90s")
			live := localIntentGet(t, ctx, cli, desired)
			env := localIntentEnvironment(t, live)
			if env["KEEP"] != "after" || env["EXTERNAL"] != "preserve" {
				t.Fatalf("desired/external env not preserved: %v", env)
			}
			if _, found := env["AUTH"]; found {
				t.Fatalf("known AUTH list item was not deleted: %v", env)
			}
			if got := strings.TrimSpace(localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "exec", "deployment/redis", "--", "redis-cli", "GET", key)); got != "retained-after-update" {
				t.Fatalf("Redis persisted data changed after rollout: %q", got)
			}
			// A stale preparation can be collected without changing the running
			// workload or restarting its Pod; the persisted key remains readable.
			var podsBefore corev1.PodList
			if err := cli.List(ctx, &podsBefore, client.InNamespace(owner.Namespace)); err != nil {
				t.Fatal(err)
			}
			prepared := localGCPrepared(t, ctx, cli, owner, live)
			if _, err := localGC(cli, owner.Namespace).Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(prepared), &corev1.Secret{}); !apierrors.IsNotFound(err) {
				t.Fatalf("stale preparation not collected: %v", err)
			}
			localGCCheckBusiness(t, live, localIntentGet(t, ctx, cli, desired))
			for _, pod := range podsBefore.Items {
				current := &corev1.Pod{}
				if err := cli.Get(ctx, client.ObjectKeyFromObject(&pod), current); err != nil || current.UID != pod.UID {
					t.Fatalf("collector replaced Redis Pod: %v", err)
				}
			}
			if got := strings.TrimSpace(localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "exec", "deployment/redis", "--", "redis-cli", "GET", key)); got != "retained-after-update" {
				t.Fatalf("Redis data lost during collection: %q", got)
			}
			localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "exec", "deployment/redis", "--", "redis-cli", "DEL", key)
			t.Logf("one-replica Redis Ready after updates; persisted key survived, cleanup DEL completed; env=%v", env)
		})
		t.Run(name+"_statefulset_secret_env_volume_mounts", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			desired := localIntentRedis(owner)
			desired.SetKind("StatefulSet")
			desired.SetName("nacos-shape")
			unstructured.RemoveNestedField(desired.Object, "spec", "strategy")
			if err := unstructured.SetNestedField(desired.Object, int64(0), "spec", "replicas"); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedField(desired.Object, "nacos-shape", "spec", "serviceName"); err != nil {
				t.Fatal(err)
			}
			containers, _, _ := unstructured.NestedSlice(desired.Object, "spec", "template", "spec", "containers")
			container := localIntentMap(t, containers[0])
			container["env"] = []interface{}{
				map[string]interface{}{"name": "KEEP", "value": "before"},
				map[string]interface{}{"name": "NACOS_AUTH_TOKEN", "valueFrom": map[string]interface{}{"secretKeyRef": map[string]interface{}{"name": "auth-secret", "key": "token"}}},
			}
			container["volumeMounts"] = []interface{}{map[string]interface{}{"name": "auth", "mountPath": "/opt/auth"}}
			if err := unstructured.SetNestedSlice(desired.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedSlice(desired.Object, []interface{}{map[string]interface{}{"name": "auth", "secret": map[string]interface{}{"secretName": "auth-secret"}}}, "spec", "template", "spec", "volumes"); err != nil {
				t.Fatal(err)
			}
			if legacy {
				if err := cli.Create(ctx, desired.DeepCopy(), client.FieldOwner("manager")); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			enabledDesired := desired.DeepCopy()
			containers, _, _ = unstructured.NestedSlice(desired.Object, "spec", "template", "spec", "containers")
			container = localIntentMap(t, containers[0])
			container["env"] = []interface{}{map[string]interface{}{"name": "KEEP", "value": "after"}}
			delete(container, "volumeMounts")
			if err := unstructured.SetNestedSlice(desired.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				t.Fatal(err)
			}
			unstructured.RemoveNestedField(desired.Object, "spec", "template", "spec", "volumes")
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live := localIntentGet(t, ctx, cli, desired)
			env := localIntentEnvironment(t, live)
			if _, found := env["NACOS_AUTH_TOKEN"]; found {
				t.Fatalf("known secret env reference was not deleted: %v", env)
			}
			containers, _, _ = unstructured.NestedSlice(live.Object, "spec", "template", "spec", "containers")
			mounts, _, _ := unstructured.NestedSlice(localIntentMap(t, containers[0]), "volumeMounts")
			if len(mounts) != 0 {
				t.Fatalf("known volumeMount was not deleted: %v", mounts)
			}
			volumes, _, _ := unstructured.NestedSlice(live.Object, "spec", "template", "spec", "volumes")
			if len(volumes) != 0 {
				if !legacy {
					t.Fatalf("new SSA resource defaulted Secret volume must be deleted: %v", volumes)
				}
				t.Logf("LIMITATION: defaulted secret volume retained conservatively; env and volumeMount cleanup succeeded: %v", volumes)
			} else {
				t.Log("secret env, volumeMount and volume removed")
			}
			if !legacy {
				if err := w.Reconcile(ctx, owner, enabledDesired, ""); err != nil {
					t.Fatal(err)
				}
				if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
					t.Fatal(err)
				}
				live = localIntentGet(t, ctx, cli, desired)
				volumes, _, _ = unstructured.NestedSlice(live.Object, "spec", "template", "spec", "volumes")
				if len(volumes) != 0 {
					t.Fatalf("second enable→disable cycle left defaulted volume after ordinary Patch ownership: %v", volumes)
				}
				if err := w.Reconcile(ctx, owner, enabledDesired, ""); err != nil {
					t.Fatal(err)
				}
				localIntentPatch(t, ctx, cli, enabledDesired, "manager", `[{"op":"replace","path":"/spec/template/spec/volumes/0/secret/defaultMode","value":256},{"op":"add","path":"/spec/template/spec/volumes/0/secret/optional","value":true}]`)
				if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
					t.Fatal(err)
				}
				live = localIntentGet(t, ctx, cli, desired)
				volumes, _, _ = unstructured.NestedSlice(live.Object, "spec", "template", "spec", "volumes")
				if len(volumes) != 1 {
					t.Fatalf("externally edited Secret volume was deleted: %v", volumes)
				}
				volume, ok := volumes[0].(map[string]interface{})
				if !ok {
					t.Fatal("volume must be an object")
				}
				mode, _, _ := unstructured.NestedInt64(volume, "secret", "defaultMode")
				optional, _, _ := unstructured.NestedBool(volume, "secret", "optional")
				if mode != 256 || !optional {
					t.Fatalf("external defaultMode/optional drift lost: mode=%d optional=%t", mode, optional)
				}
				t.Log("repeated unchanged defaulted volumes deleted; externally modified defaultMode and added optional preserved")
			}
		})
	}
	t.Run("legacy_first_omission_is_preserved", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "config")
		desired.Object["data"] = map[string]interface{}{"KEEP": "before", "AUTH": "cannot-prove-origin"}
		if err := cli.Create(ctx, desired.DeepCopy(), client.FieldOwner("manager")); err != nil {
			t.Fatal(err)
		}
		desired.Object["data"] = map[string]interface{}{"KEEP": "after"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		value, _, _ := unstructured.NestedString(live.Object, "data", "AUTH")
		if value != "cannot-prove-origin" {
			t.Fatalf("ambiguous legacy omission was not preserved: %q", value)
		}
		t.Log("legacy AUTH intentionally retained: this is protected ambiguity, not successful cleanup")
	})
	t.Run("externally_changed_deleted_key_is_preserved", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "config")
		desired.Object["data"] = map[string]interface{}{"KEEP": "before", "AUTH": "initial"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		localIntentPatch(t, ctx, cli, desired, "external-controller", `[{"op":"replace","path":"/data/AUTH","value":"external-value"}]`)
		desired.Object["data"] = map[string]interface{}{"KEEP": "after"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		value, _, _ := unstructured.NestedString(live.Object, "data", "AUTH")
		if value != "external-value" {
			t.Fatalf("external change was deleted or overwritten: %q", value)
		}
	})
	t.Run("external_apply_shared_key_is_preserved", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "config")
		desired.Object["data"] = map[string]interface{}{"KEEP": "before", "AUTH": "shared"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		external := localIntentObject(owner, "v1", "ConfigMap", "config")
		external.SetOwnerReferences(nil)
		external.Object["data"] = map[string]interface{}{"AUTH": "shared"}
		if err := cli.Patch(ctx, external, client.Apply, client.FieldOwner("disaster-recovery")); err != nil {
			t.Fatal(err)
		}
		desired.Object["data"] = map[string]interface{}{"KEEP": "after"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		value, _, _ := unstructured.NestedString(live.Object, "data", "AUTH")
		if value != "shared" {
			t.Fatalf("shared Apply ownership was ignored: %q", value)
		}
	})
	t.Run("paused_owner_rejects_writes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "config")
		desired.Object["data"] = map[string]interface{}{"KEEP": "before"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		if err := cli.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
			t.Fatal(err)
		}
		owner.SetAnnotations(map[string]string{v1.AnnotationSuspendReconcile: "true"})
		if err := cli.Update(ctx, owner); err != nil {
			t.Fatal(err)
		}
		desired.Object["data"] = map[string]interface{}{"KEEP": "after"}
		if err := w.Reconcile(ctx, owner, desired, ""); err == nil {
			t.Fatal("paused owner accepted a business write")
		}
		live := localIntentGet(t, ctx, cli, desired)
		value, _, _ := unstructured.NestedString(live.Object, "data", "KEEP")
		if value != "before" {
			t.Fatalf("paused resource was changed: %q", value)
		}
	})
	t.Run("configuration_handle_cross_namespace_recovers_saved_intent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		targetOwner := localIntentOwner(t, ctx, cli)
		k8s.ConfigureManagedResources(cli)
		k8s.ConfigureResourceIntents(resolver)
		t.Cleanup(func() {
			k8s.ConfigureManagedResources(nil)
			k8s.ConfigureResourceIntents(nil)
		})
		configuration := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{GenerateName: "intent-cross-"}}
		configuration.Spec.Template = fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cross-config\n  namespace: %s\ndata:\n  KEEP: before\n  AUTH: enabled\n", targetOwner.Namespace)
		if err := cli.Create(ctx, configuration); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := cli.Delete(cleanupCtx, configuration); client.IgnoreNotFound(err) != nil {
				t.Error(err)
			}
		})
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionPublish, configuration); err != nil {
			t.Fatal(err)
		}
		obj := localIntentObject(targetOwner, "v1", "ConfigMap", "cross-config")
		live := localIntentGet(t, ctx, cli, obj)
		if ref := metav1.GetControllerOf(live); ref != nil {
			t.Fatalf("invalid cross-namespace controller ownerReference: %v", ref)
		}
		if live.GetAnnotations()[v1.AnnotationConfigurationOwnerUID] != string(owner.UID) {
			t.Fatal("cross-namespace lifecycle owner identity missing")
		}
		recordName := live.GetAnnotations()[k8s.AnnotationResourceIntent]
		if recordName == "" {
			t.Fatal("resource intent pointer missing")
		}
		record := &corev1.Secret{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: recordName}, record); err != nil {
			t.Fatalf("intent record not in owning MID namespace: %v", err)
		}
		localIntentPatch(t, ctx, cli, obj, "external-controller", `[{"op":"add","path":"/data/EXTERNAL","value":"preserve"}]`)
		// Recreate all in-memory writer dependencies, as after controller restart.
		freshResolver, err := k8s.NewIntentSchemaResolver(cfg)
		if err != nil {
			t.Fatal(err)
		}
		k8s.ConfigureResourceIntents(freshResolver)
		configuration.Spec.Template = fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cross-config\n  namespace: %s\ndata:\n  KEEP: after\n", targetOwner.Namespace)
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionUpdate, configuration); err != nil {
			t.Fatal(err)
		}
		live = localIntentGet(t, ctx, cli, obj)
		data, _, err := unstructured.NestedStringMap(live.Object, "data")
		if err != nil || data["KEEP"] != "after" || data["EXTERNAL"] != "preserve" {
			t.Fatalf("cross-namespace desired/external fields: %v err=%v", data, err)
		}
		if _, exists := data["AUTH"]; exists {
			t.Fatalf("restart lost previous render deletion evidence: %v", data)
		}
		t.Logf("real Configuration.Handle preserved explicit namespace and external fields; recreated resolver/writer deleted known AUTH: %v", data)
	})
	t.Run("resource_version_race_retries_without_losing_external_key", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "config")
		desired.Object["data"] = map[string]interface{}{"KEEP": "before", "AUTH": "remove"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		raceClient := &localIntentRaceClient{Client: cli, namespace: owner.Namespace, name: desired.GetName()}
		racingWriter := &k8s.ManagedResourceWriter{Client: raceClient, Reader: cli, IntentSchemas: resolver}
		desired.Object["data"] = map[string]interface{}{"KEEP": "after"}
		if err := racingWriter.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		if !raceClient.injected {
			t.Fatal("race injection did not execute")
		}
		live := localIntentGet(t, ctx, cli, desired)
		data, _, err := unstructured.NestedStringMap(live.Object, "data")
		if err != nil || data["EXTERNAL_RACE"] != "preserve" || data["KEEP"] != "after" {
			t.Fatalf("race lost fields: %v err=%v", data, err)
		}
		if _, exists := data["AUTH"]; exists {
			t.Fatalf("retry lost deletion intent: %v", data)
		}
	})
	for _, committed := range []bool{false, true} {
		name := "response_lost_before_commit"
		if committed {
			name = "response_lost_after_commit"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			desired := localIntentObject(owner, "v1", "ConfigMap", "uncertain-commit")
			desired.Object["data"] = map[string]any{"KEEP": "before", "AUTH": "remove"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			wrapped := &localIntentResponseLostClient{Client: cli, key: client.ObjectKeyFromObject(desired), commit: committed}
			writer := &k8s.ManagedResourceWriter{Client: wrapped, Reader: cli, IntentSchemas: resolver}
			desired.Object["data"] = map[string]any{"KEEP": "after"}
			if err := writer.Reconcile(ctx, owner, desired, ""); err == nil || !wrapped.injected {
				t.Fatalf("expected uncertain response: injected=%v err=%v", wrapped.injected, err)
			}
			live := localIntentGet(t, ctx, cli, desired)
			data, _, err := unstructured.NestedStringMap(live.Object, "data")
			if err != nil {
				t.Fatal(err)
			}
			if (data["KEEP"] == "after") != committed {
				t.Fatalf("unexpected server commit outcome: %v", data)
			}
			var records corev1.SecretList
			if err := cli.List(ctx, &records, client.InNamespace(owner.Namespace)); err != nil || len(records.Items) != 2 {
				t.Fatalf("uncertain publication lost prepared record: count=%d err=%v", len(records.Items), err)
			}
			pointer := live.GetAnnotations()[k8s.AnnotationResourceIntent]
			if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: pointer}, &corev1.Secret{}); err != nil {
				t.Fatalf("live pointer references missing record: %v", err)
			}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live = localIntentGet(t, ctx, cli, desired)
			data, _, err = unstructured.NestedStringMap(live.Object, "data")
			if err != nil || data["KEEP"] != "after" || len(data) != 1 {
				t.Fatalf("retry failed to converge: %v err=%v", data, err)
			}
			// Scope accelerated collection to this fixture, leaving the separate
			// default-interval GC acceptance test and other namespaces untouched.
			result, err := localGC(cli, owner.Namespace).Sweep(ctx)
			if err != nil || result.Deleted != 1 || result.Kept != 1 {
				t.Fatalf("orphan preparation collection: %+v err=%v", result, err)
			}
		})
	}
	for _, failure := range []string{"missing_record", "recreated_resource"} {
		t.Run(failure+"_fails_closed", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			desired := localIntentObject(owner, "v1", "ConfigMap", "config")
			desired.Object["data"] = map[string]interface{}{"KEEP": "before", "AUTH": "remove"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live := localIntentGet(t, ctx, cli, desired)
			if failure == "missing_record" {
				record := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: owner.Namespace, Name: live.GetAnnotations()[k8s.AnnotationResourceIntent]}}
				if err := cli.Delete(ctx, record); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := cli.Delete(ctx, live); err != nil {
					t.Fatal(err)
				}
				replacement := desired.DeepCopy()
				replacement.SetAnnotations(live.GetAnnotations())
				if err := cli.Create(ctx, replacement, client.FieldOwner("manager")); err != nil {
					t.Fatal(err)
				}
			}
			desired.Object["data"] = map[string]interface{}{"KEEP": "after"}
			if err := w.Reconcile(ctx, owner, desired, ""); err == nil {
				t.Fatal("untrusted/missing persisted record did not fail closed")
			} else {
				t.Logf("expected failure: %v", err)
			}
			live = localIntentGet(t, ctx, cli, desired)
			data, _, _ := unstructured.NestedStringMap(live.Object, "data")
			if data["KEEP"] != "before" || data["AUTH"] != "remove" {
				t.Fatalf("failure partially updated business fields: %v", data)
			}
		})
	}
	for _, snapshot := range []bool{false, true} {
		name := "legacy_valuefrom_without_record_rejects_invalid_transition"
		if snapshot {
			name = "legacy_valuefrom_with_record_switches_atomically"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			desired := localIntentRedis(owner)
			if err := unstructured.SetNestedField(desired.Object, int64(0), "spec", "replicas"); err != nil {
				t.Fatal(err)
			}
			containers, _, _ := unstructured.NestedSlice(desired.Object, "spec", "template", "spec", "containers")
			localIntentMap(t, containers[0])["env"] = []interface{}{map[string]interface{}{"name": "AUTH", "valueFrom": map[string]interface{}{"secretKeyRef": map[string]interface{}{"name": "auth-secret", "key": "token"}}}}
			if err := unstructured.SetNestedSlice(desired.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				t.Fatal(err)
			}
			if err := cli.Create(ctx, desired.DeepCopy(), client.FieldOwner("manager")); err != nil {
				t.Fatal(err)
			}
			if snapshot {
				if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
					t.Fatal(err)
				}
			}
			localIntentMap(t, containers[0])["env"] = []interface{}{map[string]interface{}{"name": "AUTH", "value": "inline-value"}}
			if err := unstructured.SetNestedSlice(desired.Object, containers, "spec", "template", "spec", "containers"); err != nil {
				t.Fatal(err)
			}
			err := w.Reconcile(ctx, owner, desired, "")
			if snapshot && err != nil {
				t.Fatal(err)
			}
			if !snapshot && err == nil {
				t.Fatal("ambiguous valueFrom removal unexpectedly succeeded")
			}
			live := localIntentGet(t, ctx, cli, desired)
			containers, _, _ = unstructured.NestedSlice(live.Object, "spec", "template", "spec", "containers")
			env, _, _ := unstructured.NestedSlice(localIntentMap(t, containers[0]), "env")
			entry := localIntentMap(t, env[0])
			if snapshot {
				if _, found := entry["valueFrom"]; found || entry["value"] != "inline-value" {
					t.Fatalf("non-atomic valueFrom/value transition: %v", entry)
				}
			} else if _, found := entry["valueFrom"]; !found {
				t.Fatalf("failed first-update transition modified old secret reference: %v", entry)
			}
		})
	}
	t.Run("explicit_resume_apply_replays_unchanged_desired", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "ConfigMap", "config")
		desired.Object["data"] = map[string]interface{}{"KEEP": "original-intent"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		localIntentPatch(t, ctx, cli, desired, "disaster-recovery", `[{"op":"replace","path":"/data/KEEP","value":"changed-during-pause"}]`)
		if err := cli.Get(ctx, client.ObjectKeyFromObject(owner), owner); err != nil {
			t.Fatal(err)
		}
		owner.SetAnnotations(map[string]string{v1.AnnotationResumePolicy: string(v1.ReconcileResumePolicyApply)})
		if err := cli.Update(ctx, owner); err != nil {
			t.Fatal(err)
		}
		owner.Status.Conditions = []metav1.Condition{{Type: v1.CondTypeReconcilePaused, Status: metav1.ConditionTrue, Reason: "TestingResumeApply", Message: "Test explicit apply resume", LastTransitionTime: metav1.Now()}}
		if err := cli.Status().Update(ctx, owner); err != nil {
			t.Fatal(err)
		}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		value, _, _ := unstructured.NestedString(live.Object, "data", "KEEP")
		if value != "original-intent" {
			t.Fatalf("explicit resume apply did not replay unchanged desired: %q", value)
		}
	})
	for _, legacy := range []bool{false, true} {
		name := "new_structural_crd_lists_and_status_subresource"
		if legacy {
			name = "legacy_structural_crd_lists_and_status_subresource"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			group := "intent-" + string(owner.UID)[:8] + ".test.middleware.cn"
			crd := &unstructured.Unstructured{}
			crdJSON := fmt.Sprintf(`{
		  "apiVersion":"apiextensions.k8s.io/v1","kind":"CustomResourceDefinition",
		  "metadata":{"name":"checks.%s"},
		  "spec":{"group":"%s","scope":"Namespaced","names":{"plural":"checks","singular":"check","kind":"IntentCheck"},
		    "versions":[{"name":"v1","served":true,"storage":true,"subresources":{"status":{}},"schema":{"openAPIV3Schema":{
		      "type":"object","properties":{
		        "spec":{"type":"object","properties":{
		          "settings":{"type":"object","additionalProperties":{"type":"string"}},
		          "entries":{"type":"array","x-kubernetes-list-type":"map","x-kubernetes-list-map-keys":["name"],"items":{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"value":{"type":"string"},"note":{"type":"string"}}}}
		        }},
		        "status":{"type":"object","properties":{"phase":{"type":"string"}}}
		      }
		    }}}]
		  }
		}`, group, group)
			if err := json.Unmarshal([]byte(crdJSON), &crd.Object); err != nil {
				t.Fatal(err)
			}
			if err := cli.Create(ctx, crd); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := cli.Delete(cleanupCtx, crd); client.IgnoreNotFound(err) != nil {
					t.Error(err)
				}
			})
			localIntentKubectl(t, ctx, kubeconfig, owner.Namespace, "wait", "--for=condition=Established", "crd/"+crd.GetName(), "--timeout=60s")
			desired := localIntentObject(owner, group+"/v1", "IntentCheck", "check")
			// Established precedes OpenAPI publication. Wait for that separate API
			// prerequisite rather than treating initial discovery lag as merge failure.
			if err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
				_, _, schemaErr := resolver.Resolve(ctx, desired.GroupVersionKind())
				return schemaErr == nil, nil
			}); err != nil {
				t.Fatalf("CRD OpenAPI schema did not become available: %v", err)
			}
			desired.Object["spec"] = map[string]interface{}{
				"settings": map[string]interface{}{"KEEP": "before", "AUTH": "remove"},
				"entries":  []interface{}{map[string]interface{}{"name": "auth", "value": "remove"}, map[string]interface{}{"name": "keep", "value": "before"}, map[string]interface{}{"name": "protected", "value": "retain-if-external-child"}},
			}
			if legacy {
				if err := cli.Create(ctx, desired.DeepCopy(), client.FieldOwner("manager")); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live := localIntentGet(t, ctx, cli, desired)
			if err := cli.Status().Patch(ctx, live, client.RawPatch(types.MergePatchType, []byte(`{"status":{"phase":"ExternalReady"}}`)), client.FieldOwner("cr-controller")); err != nil {
				t.Fatal(err)
			}
			var statusBefore string
			for _, entry := range live.GetManagedFields() {
				if entry.Subresource == "status" {
					raw, marshalErr := json.Marshal(entry)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					statusBefore = string(raw)
				}
			}
			if statusBefore == "" {
				t.Fatal("fixture status manager was not recorded")
			}
			localIntentPatch(t, ctx, cli, desired, "manager", `[{"op":"add","path":"/spec/entries/2/note","value":"external-same-manager"},{"op":"add","path":"/spec/entries/-","value":{"name":"external","value":"preserve"}},{"op":"add","path":"/spec/settings/EXTERNAL","value":"preserve"}]`)
			desired.Object["spec"] = map[string]interface{}{
				"settings": map[string]interface{}{"KEEP": "after"},
				"entries":  []interface{}{map[string]interface{}{"name": "keep", "value": "after"}},
			}
			desired.Object["status"] = map[string]interface{}{"phase": "MustNotWrite"}
			if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
				t.Fatal(err)
			}
			live = localIntentGet(t, ctx, cli, desired)
			phase, _, _ := unstructured.NestedString(live.Object, "status", "phase")
			if phase != "ExternalReady" {
				t.Fatalf("writer changed status: %q", phase)
			}
			var statusAfter string
			for _, entry := range live.GetManagedFields() {
				if entry.Subresource == "status" {
					raw, marshalErr := json.Marshal(entry)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					statusAfter = string(raw)
				}
			}
			if statusAfter != statusBefore {
				t.Fatalf("status managedFields changed: before=%s after=%s", statusBefore, statusAfter)
			}
			settings, _, _ := unstructured.NestedStringMap(live.Object, "spec", "settings")
			if _, exists := settings["AUTH"]; exists || settings["KEEP"] != "after" || settings["EXTERNAL"] != "preserve" {
				t.Fatalf("CRD map merge failed: %v", settings)
			}
			entries, _, _ := unstructured.NestedSlice(live.Object, "spec", "entries")
			found := make(map[string]string)
			for _, item := range entries {
				entry := localIntentMap(t, item)
				found[fmt.Sprint(entry["name"])] = fmt.Sprint(entry["value"])
			}
			if _, exists := found["auth"]; exists || found["keep"] != "after" || found["external"] != "preserve" || found["protected"] != "retain-if-external-child" {
				t.Fatalf("CRD associative list merge failed: %v", found)
			}
			t.Logf("CRD schema map/list deletion and external additions preserved; status=%s", phase)
		})
	}
	for _, legacy := range []bool{false, true} {
		name := "new_preaction_template_configuration_chain"
		if legacy {
			name = "legacy_preaction_template_configuration_chain"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			owner := localIntentOwner(t, ctx, cli)
			k8s.ConfigureManagedResources(cli)
			k8s.ConfigureResourceIntents(resolver)
			t.Cleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
			template := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: cue-chain
spec:
  replicas: 0
  selector:
    matchLabels: {app: cue-chain}
  template:
    metadata:
      labels: {app: cue-chain}
    spec:
      containers:
      - name: redis
        image: redis:7.2.7-alpine
        env:
        - name: KEEP
          value: stable
        {{ if .Values.auth }}
        - name: AUTH
          value: enabled
        {{ end }}
`
			configuration := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "intent-preaction-fixture"}}
			baseline := &v1.MiddlewareActionBaseline{ObjectMeta: metav1.ObjectMeta{Name: "set-auth"}}
			baseline.Spec.Steps = []v1.Step{{Name: "set-auth", CUE: `parameters: resource: {
  apiversion: "middleware.cn/v1"
  kind: "Middleware"
  name: "{{ .Globe.Name }}"
  namespace: "{{ .Globe.Namespace }}"
}
output: spec: parameters: auth: {{ .Necessary.enable }}`}}
			for _, enabled := range []bool{true, false} {
				input := localIntentObject(owner, v1.GroupVersion.String(), "Middleware", owner.Name)
				input.Object["spec"] = map[string]interface{}{"parameters": map[string]interface{}{}}
				action := &v1.MiddlewareAction{}
				action.Spec.Necessary.Raw = []byte(fmt.Sprintf(`{"enable":%t}`, enabled))
				if err := middlewareaction.ExecutePreAction(ctx, cli, input, baseline, action); err != nil {
					t.Fatal(err)
				}
				values, _, err := unstructured.NestedMap(input.Object, "spec", "parameters")
				if err != nil || values["auth"] != enabled {
					t.Fatalf("PreAction intermediate value mismatch: %v err=%v", values, err)
				}
				rendered, err := tools.TemplateParse(ctx, template, &tools.TemplateValues{Values: values, Globe: map[string]interface{}{"Name": owner.Name, "Namespace": owner.Namespace}})
				if err != nil {
					t.Fatal(err)
				}
				configuration.Spec.Template = rendered
				if enabled && legacy {
					old := &unstructured.Unstructured{}
					if err := yaml.Unmarshal([]byte(rendered), old); err != nil {
						t.Fatal(err)
					}
					old.SetNamespace(owner.Namespace)
					old.SetOwnerReferences(localIntentObject(owner, "apps/v1", "Deployment", "cue-chain").GetOwnerReferences())
					old.SetLabels(map[string]string{v1.LabelApp: owner.Name})
					old.SetAnnotations(map[string]string{v1.LabelConfigurations: configuration.Name})
					if err := cli.Create(ctx, old, client.FieldOwner("manager")); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionUpdate, configuration); err != nil {
					t.Fatal(err)
				}
				live := localIntentGet(t, ctx, cli, localIntentObject(owner, "apps/v1", "Deployment", "cue-chain"))
				env := localIntentEnvironment(t, live)
				_, exists := env["AUTH"]
				if exists != enabled || env["KEEP"] != "stable" {
					t.Fatalf("CUE→template→Configuration chain auth=%t env=%v", enabled, env)
				}
			}
			t.Log("real ExecutePreAction → TemplateParse → Configuration.Handle true→false removed AUTH after baseline establishment")
		})
	}
	t.Run("job_stays_create_only", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "batch/v1", "Job", "once")
		desired.Object["spec"] = map[string]interface{}{"suspend": true, "template": localIntentBatchTemplate(true)}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		if live.GetAnnotations()[k8s.AnnotationResourceIntent] != "" {
			t.Fatal("one-shot Job received a mutable intent record")
		}
		for _, field := range live.GetManagedFields() {
			if field.Operation == metav1.ManagedFieldsOperationApply {
				t.Fatal("one-shot Job was created with Apply")
			}
		}
		desired.Object["spec"] = map[string]interface{}{"suspend": true, "template": localIntentBatchTemplate(false)}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live = localIntentGet(t, ctx, cli, desired)
		if _, exists := localIntentEnvironment(t, live)["AUTH"]; !exists {
			t.Fatal("one-shot Job template was unexpectedly rewritten")
		}
	})
	t.Run("cronjob_v1_is_mutable", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "batch/v1", "CronJob", "scheduled")
		desired.Object["spec"] = map[string]interface{}{"schedule": "0 0 1 1 *", "suspend": true, "jobTemplate": map[string]interface{}{"spec": map[string]interface{}{"template": localIntentBatchTemplate(true)}}}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		desired.Object["spec"] = map[string]interface{}{"schedule": "0 0 1 1 *", "suspend": true, "jobTemplate": map[string]interface{}{"spec": map[string]interface{}{"template": localIntentBatchTemplate(false)}}}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		if live.GetAnnotations()[k8s.AnnotationResourceIntent] == "" {
			t.Fatal("batch/v1 CronJob did not receive mutable resource intent")
		}
		jobSpec, _, _ := unstructured.NestedMap(live.Object, "spec", "jobTemplate", "spec")
		job := &unstructured.Unstructured{Object: map[string]interface{}{"spec": jobSpec}}
		if _, exists := localIntentEnvironment(t, job)["AUTH"]; exists {
			t.Fatal("batch/v1 CronJob AUTH env was not removed")
		}
	})
	t.Run("middlewareoperator_owner_manages_configuration", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		mid := localIntentOwner(t, ctx, cli)
		owner := &v1.MiddlewareOperator{ObjectMeta: metav1.ObjectMeta{Namespace: mid.Namespace, Name: "operator-owner"}}
		if err := cli.Create(ctx, owner); err != nil {
			t.Fatal(err)
		}
		k8s.ConfigureManagedResources(cli)
		k8s.ConfigureResourceIntents(resolver)
		t.Cleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
		configuration := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "intent-mo-fixture"}}
		configuration.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: operator-config\ndata:\n  KEEP: stable\n  AUTH: enabled\n"
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionPublish, configuration); err != nil {
			t.Fatal(err)
		}
		configuration.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: operator-config\ndata:\n  KEEP: stable\n"
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionUpdate, configuration); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, localIntentObject(mid, "v1", "ConfigMap", "operator-config"))
		data, _, _ := unstructured.NestedStringMap(live.Object, "data")
		if _, exists := data["AUTH"]; exists || data["KEEP"] != "stable" {
			t.Fatalf("MO configuration update failed: %v", data)
		}
		ref := metav1.GetControllerOf(live)
		if ref == nil || ref.UID != owner.UID || ref.Kind != "MiddlewareOperator" {
			t.Fatalf("MO controller identity lost: %v", ref)
		}
	})
	t.Run("secret_stringdata_canonicalization_and_private_record", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		desired := localIntentObject(owner, "v1", "Secret", "credentials")
		desired.Object["stringData"] = map[string]interface{}{"KEEP": "keep-fixture-secret", "AUTH": "delete-fixture-secret"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		desired.Object["stringData"] = map[string]interface{}{"KEEP": "keep-fixture-secret"}
		if err := w.Reconcile(ctx, owner, desired, ""); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, desired)
		data, _, _ := unstructured.NestedStringMap(live.Object, "data")
		if _, exists := data["AUTH"]; exists || data["KEEP"] == "" {
			t.Fatal("stringData removal did not remove corresponding data key")
		}
		annotations, err := json.Marshal(live.GetAnnotations())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(annotations), "fixture-secret") || strings.Contains(string(annotations), data["KEEP"]) {
			t.Fatal("Secret rendered values leaked into public annotations")
		}
		if live.GetAnnotations()[k8s.AnnotationResourceIntent] == "" {
			t.Fatal("private record pointer is missing")
		}
	})
	t.Run("cluster_scoped_configuration_record_in_owner_namespace", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		k8s.ConfigureManagedResources(cli)
		k8s.ConfigureResourceIntents(resolver)
		t.Cleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
		roleName := "intent-role-" + string(owner.UID)[:8]
		configuration := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "intent-cluster-fixture", UID: types.UID("configuration-v1")}}
		configuration.Spec.Template = fmt.Sprintf("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: %s\n  labels: {keep: stable, auth: enabled}\nrules:\n- apiGroups: ['']\n  resources: [configmaps]\n  verbs: [get]\n", roleName)
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionPublish, configuration); err != nil {
			t.Fatal(err)
		}
		obj := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]interface{}{"name": roleName}}}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := cli.Delete(cleanupCtx, obj); client.IgnoreNotFound(err) != nil {
				t.Error(err)
			}
		})
		configuration.Spec.Template = fmt.Sprintf("apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: %s\n  labels: {keep: stable}\nrules:\n- apiGroups: ['']\n  resources: [configmaps]\n  verbs: [get]\n", roleName)
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionUpdate, configuration); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, obj)
		if _, exists := live.GetLabels()["auth"]; exists || live.GetLabels()["keep"] != "stable" {
			t.Fatalf("cluster resource desired label deletion failed: %v", live.GetLabels())
		}
		if live.GetNamespace() != "" || metav1.GetControllerOf(live) != nil {
			t.Fatal("cluster-scoped resource got namespaced ownership")
		}
		if live.GetAnnotations()[v1.AnnotationConfigurationOwnerUID] != string(owner.UID) {
			t.Fatal("cluster resource owner UID marker missing")
		}
		record := &corev1.Secret{}
		if err := cli.Get(ctx, client.ObjectKey{Namespace: owner.Namespace, Name: live.GetAnnotations()[k8s.AnnotationResourceIntent]}, record); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("same_named_configuration_recreated_uid_is_same_source", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		owner := localIntentOwner(t, ctx, cli)
		k8s.ConfigureManagedResources(cli)
		k8s.ConfigureResourceIntents(resolver)
		t.Cleanup(func() { k8s.ConfigureManagedResources(nil); k8s.ConfigureResourceIntents(nil) })
		configuration := &v1.MiddlewareConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "same-name-logical-source", UID: types.UID("configuration-before")}}
		configuration.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: recreated-source\ndata:\n  KEEP: stable\n  AUTH: enabled\n"
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionPublish, configuration); err != nil {
			t.Fatal(err)
		}
		configuration.UID = types.UID("configuration-after")
		configuration.Spec.Template = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: recreated-source\ndata:\n  KEEP: stable\n"
		if _, err := middlewareconfiguration.Handle(ctx, cli, owner, consts.HandleActionUpdate, configuration); err != nil {
			t.Fatal(err)
		}
		live := localIntentGet(t, ctx, cli, localIntentObject(owner, "v1", "ConfigMap", "recreated-source"))
		data, _, _ := unstructured.NestedStringMap(live.Object, "data")
		if _, exists := data["AUTH"]; exists || data["KEEP"] != "stable" {
			t.Fatalf("recreated MCF source broke normal desired cleanup: %v", data)
		}
		if live.GetAnnotations()[v1.AnnotationConfigurationUID] != string(configuration.UID) {
			t.Fatal("new MCF source UID was not reflected in lifecycle metadata")
		}
	})
	t.Run("intent_gc", func(t *testing.T) { runLocalIntentGCTests(t, cli, w) })
}

func localIntentBatchTemplate(auth bool) map[string]interface{} {
	env := []interface{}{map[string]interface{}{"name": "KEEP", "value": "stable"}}
	if auth {
		env = append(env, map[string]interface{}{"name": "AUTH", "value": "enabled"})
	}
	return map[string]interface{}{"spec": map[string]interface{}{"restartPolicy": "Never", "containers": []interface{}{map[string]interface{}{"name": "job", "image": "redis:7.2.7-alpine", "env": env}}}}
}

// localIntentResponseLostClient injects a transport-style EOF at the boundary
// of an actual API request. The committed branch executes the real server patch
// before dropping its response; neither branch treats dry-run as publication.
type localIntentResponseLostClient struct {
	client.Client
	key      client.ObjectKey
	commit   bool
	injected bool
}

func (c *localIntentResponseLostClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, opt := range opts {
		opt.ApplyToPatch(options)
	}
	if !c.injected && client.ObjectKeyFromObject(obj) == c.key && patch.Type() == types.JSONPatchType && len(options.DryRun) == 0 {
		c.injected = true
		if c.commit {
			if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
				return err
			}
		}
		return io.ErrUnexpectedEOF
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

type localIntentRaceClient struct {
	client.Client
	namespace string
	name      string
	injected  bool
}

func (c *localIntentRaceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	options := &client.PatchOptions{}
	for _, option := range opts {
		option.ApplyToPatch(options)
	}
	// Inject after any discovery/defaulting dry-run, immediately before the
	// actual resource commit, to exercise the final UID/RV preconditions.
	if !c.injected && len(options.DryRun) == 0 && obj.GetNamespace() == c.namespace && obj.GetName() == c.name && patch.Type() == types.JSONPatchType {
		c.injected = true
		live := &corev1.ConfigMap{}
		if err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
			return err
		}
		if err := c.Client.Patch(ctx, live, client.RawPatch(types.JSONPatchType, []byte(`[{"op":"add","path":"/data/EXTERNAL_RACE","value":"preserve"}]`)), client.FieldOwner("race-controller")); err != nil {
			return err
		}
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func localIntentOwner(t *testing.T, ctx context.Context, cli client.Client) *v1.Middleware {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "intent-local-"}}
	if err := cli.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cli.Delete(cleanupCtx, ns); client.IgnoreNotFound(err) != nil {
			t.Errorf("delete test namespace %s: %v", ns.Name, err)
		}
	})
	owner := &v1.Middleware{ObjectMeta: metav1.ObjectMeta{Name: "test-owner", Namespace: ns.Name}}
	if err := cli.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	return owner
}

func localIntentObject(owner *v1.Middleware, apiVersion, kind, name string) *unstructured.Unstructured {
	controller := true
	obj := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": apiVersion, "kind": kind}}
	obj.SetName(name)
	obj.SetNamespace(owner.Namespace)
	obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: v1.GroupVersion.String(), Kind: "Middleware", Name: owner.Name, UID: owner.UID, Controller: &controller}})
	return obj
}

func localIntentRedis(owner *v1.Middleware) *unstructured.Unstructured {
	obj := localIntentObject(owner, "apps/v1", "Deployment", "redis")
	obj.Object["spec"] = map[string]interface{}{
		"replicas": int64(1),
		"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "intent-redis"}},
		"strategy": map[string]interface{}{"type": "Recreate"},
		"template": map[string]interface{}{
			"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "intent-redis"}},
			"spec": map[string]interface{}{
				"containers": []interface{}{map[string]interface{}{
					"name": "redis", "image": "redis:7.2.7-alpine", "imagePullPolicy": "Never",
					"args":           []interface{}{"redis-server", "--appendonly", "yes", "--save", ""},
					"env":            []interface{}{map[string]interface{}{"name": "KEEP", "value": "before"}, map[string]interface{}{"name": "AUTH", "value": "enabled"}},
					"resources":      map[string]interface{}{"requests": map[string]interface{}{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]interface{}{"memory": "96Mi"}},
					"volumeMounts":   []interface{}{map[string]interface{}{"name": "data", "mountPath": "/data"}},
					"readinessProbe": map[string]interface{}{"exec": map[string]interface{}{"command": []interface{}{"redis-cli", "ping"}}, "periodSeconds": int64(1)},
				}},
				"volumes": []interface{}{map[string]interface{}{"name": "data", "persistentVolumeClaim": map[string]interface{}{"claimName": "redis-data"}}},
			},
		},
	}
	return obj
}

func localIntentGet(t *testing.T, ctx context.Context, cli client.Client, obj *unstructured.Unstructured) *unstructured.Unstructured {
	t.Helper()
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(obj.GroupVersionKind())
	if err := cli.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
		t.Fatal(err)
	}
	return live
}

func localIntentMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		t.Fatalf("expected object, got %T", value)
	}
	return object
}

func localIntentPatch(t *testing.T, ctx context.Context, cli client.Client, obj *unstructured.Unstructured, manager, patch string) {
	t.Helper()
	live := localIntentGet(t, ctx, cli, obj)
	if err := cli.Patch(ctx, live, client.RawPatch(types.JSONPatchType, []byte(patch)), client.FieldOwner(manager)); err != nil {
		t.Fatal(err)
	}
}

func localIntentEnvironment(t *testing.T, obj *unstructured.Unstructured) map[string]string {
	t.Helper()
	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		t.Fatalf("containers missing: found=%t err=%v", found, err)
	}
	env, _, err := unstructured.NestedSlice(localIntentMap(t, containers[0]), "env")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, item := range env {
		entry := localIntentMap(t, item)
		values[fmt.Sprint(entry["name"])] = fmt.Sprint(entry["value"])
	}
	return values
}

func localIntentKubectl(t *testing.T, ctx context.Context, kubeconfig, namespace string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", kubeconfig, "--namespace", namespace}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %v: %v\n%s", args, err, output)
	}
	return string(output)
}
