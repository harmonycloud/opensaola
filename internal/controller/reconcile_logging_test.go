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

package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestReconcileCorrelationID_IncludesControllerNamespaceAndName(t *testing.T) {
	t.Parallel()

	got := reconcileCorrelationID("middleware", types.NamespacedName{
		Namespace: "middleware-system",
		Name:      "demo",
	})
	if got != "middleware/middleware-system/demo" {
		t.Fatalf("reconcileCorrelationID() = %q, want %q", got, "middleware/middleware-system/demo")
	}
}

func TestReconcileCorrelationID_ClusterScopedResource(t *testing.T) {
	t.Parallel()

	got := reconcileCorrelationID("middlewarebaseline", types.NamespacedName{Name: "redis"})
	if got != "middlewarebaseline/redis" {
		t.Fatalf("reconcileCorrelationID() = %q, want %q", got, "middlewarebaseline/redis")
	}
}

func TestNewReconcileID_IsUniqueAndKeepsCorrelationPrefix(t *testing.T) {
	t.Parallel()

	correlationID := "middleware/default/demo"
	first := newReconcileID(correlationID)
	second := newReconcileID(correlationID)

	if first == second {
		t.Fatalf("expected unique reconcile IDs, got %q twice", first)
	}
	for _, got := range []string{first, second} {
		if !strings.HasPrefix(got, correlationID+"/") {
			t.Fatalf("expected reconcile ID %q to keep correlation prefix %q", got, correlationID)
		}
	}
}

func TestReconcileFunctionsUseSharedLogger(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read controller directory: %v", err)
	}

	reconcileMethods := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_controller.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Reconcile" {
				continue
			}
			reconcileMethods++
			if !hasCallTo(fn.Body, "withReconcileLogger") {
				t.Fatalf("%s Reconcile must call withReconcileLogger", entry.Name())
			}
		}
	}
	if reconcileMethods == 0 {
		t.Fatal("expected at least one Reconcile method")
	}
}

func hasCallTo(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}
