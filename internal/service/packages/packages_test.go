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

package packages

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestList_NilClient(t *testing.T) {
	SetDataNamespace("test-ns")

	_, err := List(context.Background(), nil, Option{})
	if err == nil {
		t.Fatal("expected error when calling List with nil client, got nil")
	}
}

func TestList_EmptyResult(t *testing.T) {
	SetDataNamespace("test-ns")

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	pkgs, err := List(context.Background(), cli, Option{})
	if err != nil {
		t.Fatalf("List() returned unexpected error: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages, got %d", len(pkgs))
	}
}

func TestSelectPreferredMiddlewarePackages(t *testing.T) {
	legacy := v1.MiddlewarePackage{
		ObjectMeta: metav1.ObjectMeta{
			Name: "legacy",
			Labels: map[string]string{
				v1.LabelProject: consts.ProjectZeusOperator,
			},
		},
	}
	current := v1.MiddlewarePackage{
		ObjectMeta: metav1.ObjectMeta{
			Name: "current",
			Labels: map[string]string{
				v1.LabelProject: consts.ProjectOpenSaola,
			},
		},
	}

	got := SelectPreferredMiddlewarePackages([]v1.MiddlewarePackage{legacy})
	if len(got) != 1 || got[0].Name != "legacy" {
		t.Fatalf("expected legacy package when it is the only compatible package, got %#v", got)
	}

	got = SelectPreferredMiddlewarePackages([]v1.MiddlewarePackage{legacy, current})
	if len(got) != 1 || got[0].Name != "current" {
		t.Fatalf("expected current project package to win over legacy, got %#v", got)
	}

	got = SelectPreferredMiddlewarePackages([]v1.MiddlewarePackage{{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other",
			Labels: map[string]string{
				v1.LabelProject: "other",
			},
		},
	}})
	if len(got) != 0 {
		t.Fatalf("expected incompatible packages to be filtered out, got %#v", got)
	}
}

func TestListCompatibleMiddlewarePackagesFiltersLabelsAndProject(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add middleware scheme: %v", err)
	}

	middlewarePackage := func(name, project, component, version string) *v1.MiddlewarePackage {
		return &v1.MiddlewarePackage{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					v1.LabelProject:        project,
					v1.LabelComponent:      component,
					v1.LabelPackageVersion: version,
				},
			},
		}
	}

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			middlewarePackage("legacy-match", consts.ProjectZeusOperator, "zookeeper", "1.0.0"),
			middlewarePackage("current-match", consts.ProjectOpenSaola, "zookeeper", "1.0.0"),
			middlewarePackage("wrong-component", consts.ProjectOpenSaola, "kafka", "1.0.0"),
			middlewarePackage("wrong-version", consts.ProjectZeusOperator, "zookeeper", "2.0.0"),
			middlewarePackage("wrong-project", "other", "zookeeper", "1.0.0"),
		).
		Build()

	got, err := ListCompatibleMiddlewarePackages(context.Background(), cli, client.MatchingLabels{
		v1.LabelComponent:      "zookeeper",
		v1.LabelPackageVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("ListCompatibleMiddlewarePackages() returned unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "current-match" {
		t.Fatalf("expected only current matching package, got %#v", got)
	}

	got, err = ListCompatibleMiddlewarePackages(context.Background(), cli, client.MatchingLabels{
		v1.LabelComponent:      "kafka",
		v1.LabelPackageVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("ListCompatibleMiddlewarePackages() returned unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "wrong-component" {
		t.Fatalf("expected exact label match for kafka package, got %#v", got)
	}
}
