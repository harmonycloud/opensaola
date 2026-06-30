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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCurrentFalseCondition_SkipsStaleObservedGeneration(t *testing.T) {
	t.Parallel()

	conditions := []metav1.Condition{
		{
			Type:               "ApplyCluster",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: 3,
			Message:            "old apply failure",
		},
		{
			Type:               "TemplateParseWithBaseline",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 4,
			Message:            "current success",
		},
	}

	if condition, ok := currentFalseCondition(conditions, 4); ok {
		t.Fatalf("expected stale false condition to be ignored, got %#v", condition)
	}
}

func TestCurrentFalseCondition_ReturnsCurrentGenerationFailure(t *testing.T) {
	t.Parallel()

	conditions := []metav1.Condition{
		{
			Type:               "ApplyCluster",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: 4,
			Message:            "current apply failure",
		},
	}

	condition, ok := currentFalseCondition(conditions, 4)
	if !ok {
		t.Fatal("expected current false condition")
	}
	if condition.Message != "current apply failure" {
		t.Fatalf("expected current failure message, got %q", condition.Message)
	}
}
