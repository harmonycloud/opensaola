//go:build performance

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

package k8s

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/harmonycloud/opensaola/api/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type perfTransport func(*http.Request) (*http.Response, error)

func (f perfTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type writeTraffic struct {
	committed atomic.Int64
	dryRun    atomic.Int64
}

func (tr *writeTraffic) observe(r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		if r.URL.Query().Get("dryRun") != "" {
			tr.dryRun.Add(1)
		} else {
			tr.committed.Add(1)
		}
	}
}

func perfRun(t *testing.T, stage string, concurrency, n int, tr *writeTraffic, work func(int) error) {
	t.Helper()
	beforeWrites, beforeDry := tr.committed.Load(), tr.dryRun.Load()
	elapsed := make([]float64, n)
	failed := make([]error, n)
	var wg sync.WaitGroup
	var seq atomic.Int64
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)
	started := time.Now()
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(seq.Add(1)) - 1
				if i >= n {
					return
				}
				s := time.Now()
				failed[i] = work(i)
				elapsed[i] = float64(time.Since(s)) / float64(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	duration := time.Since(started)
	runtime.ReadMemStats(&m2)
	sort.Float64s(elapsed)
	quant := func(p float64) float64 { return elapsed[int(math.Ceil(p*float64(n)))-1] }
	errs := 0
	for _, e := range failed {
		if e != nil {
			if errs == 0 {
				t.Errorf("first failure in %s: %v", stage, e)
			}
			errs++
		}
	}
	writes, dry := tr.committed.Load()-beforeWrites, tr.dryRun.Load()-beforeDry
	t.Logf("stage=%s workers=%d ops=%d ops_per_sec=%.2f p95_ms=%.3f allocated_B_per_op=%d writes=%d dry_writes=%d",
		stage, concurrency, n, float64(n)/duration.Seconds(), quant(.95), (m2.TotalAlloc-m1.TotalAlloc)/uint64(n), writes, dry)
	if stage == "noop" && (writes != 0 || dry != 0) {
		t.Errorf("unchanged objects triggered %d writes and %d dry runs", writes, dry)
	}
	if errs != 0 {
		t.FailNow()
	}
}
func TestManagedResourcePerformance(t *testing.T) {
	env := &envtest.Environment{UseExistingCluster: new(bool), CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := env.Stop(); e != nil {
			t.Error(e)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cfg = rest.CopyConfig(cfg)
	cfg.QPS = -1
	cfg.RateLimiter = nil
	tr := new(writeTraffic)
	cfg.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return perfTransport(func(r *http.Request) (*http.Response, error) { tr.observe(r); return next.RoundTrip(r) })
	})
	resolver, err := NewIntentSchemaResolver(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sch := k8sruntime.NewScheme()
	for _, add := range []func(*k8sruntime.Scheme) error{api.AddToScheme, v1.AddToScheme} {
		if err := add(sch); err != nil {
			t.Fatal(err)
		}
	}
	cli, err := client.New(cfg, client.Options{Scheme: sch})
	if err != nil {
		t.Fatal(err)
	}
	writer := ManagedResourceWriter{Client: cli, Reader: cli, IntentSchemas: resolver}
	const n = 48
	for _, workload := range []struct{ sizeKiB, workers int }{{1, 8}, {64, 32}} {
		t.Run(fmt.Sprintf("%dKiB", workload.sizeKiB), func(t *testing.T) {
			workers := workload.workers
			payload := strings.Repeat("abcdef01", 128*workload.sizeKiB)
			objs := make([]*unstructured.Unstructured, n)
			owners := make([]*api.Middleware, n)
			for i := range n {
				name := fmt.Sprintf("measure-%d-%03d", workers, i)
				owner := &api.Middleware{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec:       api.MiddlewareSpec{Baseline: "local-performance"},
				}
				if err := cli.Create(ctx, owner); err != nil {
					t.Fatal(err)
				}
				owners[i] = owner
				obj := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "v1", "kind": "ConfigMap",
					"metadata": map[string]any{"name": name, "namespace": "default"},
					"data":     map[string]any{"mode": "v1", "to-withdraw": "yes", "payload": payload},
				}}
				ref, err := intentOwnerReference(owner)
				if err != nil {
					t.Fatal(err)
				}
				obj.SetOwnerReferences([]metav1.OwnerReference{ref})
				objs[i] = obj
			}
			reconcile := func(i int) error { return writer.Reconcile(ctx, owners[i], objs[i].DeepCopy(), "") }
			read := func(i int) *v1.ConfigMap {
				t.Helper()
				var live v1.ConfigMap
				if err := cli.Get(ctx, client.ObjectKeyFromObject(objs[i]), &live); err != nil {
					t.Fatal(err)
				}
				return &live
			}
			perfRun(t, "create", workers, n, tr, reconcile)
			for i := range objs {
				obj := read(i)
				if obj.Data["mode"] != "v1" || obj.Data["to-withdraw"] != "yes" || obj.Data["payload"] != payload {
					t.Fatal("invalid first publication")
				}
				obj.Data["external"] = "keep"
				if err := cli.Update(ctx, obj); err != nil {
					t.Fatal(err)
				}
				data := objs[i].Object["data"].(map[string]any)
				data["mode"] = "v2"
				delete(data, "to-withdraw")
			}
			perfRun(t, "update", workers, n, tr, reconcile)
			saved := make([]string, n)
			for i := range objs {
				obj := read(i)
				_, stillThere := obj.Data["to-withdraw"]
				if obj.Data["mode"] != "v2" || obj.Data["external"] != "keep" || obj.Data["payload"] != payload || stillThere {
					t.Fatalf("incorrect updated object %d", i)
				}
				saved[i] = obj.ResourceVersion
			}
			perfRun(t, "noop", workers, n, tr, reconcile)
			for i := range objs {
				if obj := read(i); obj.ResourceVersion != saved[i] {
					t.Fatal("no-op changed target resourceVersion")
				}
			}
		})
	}
}
