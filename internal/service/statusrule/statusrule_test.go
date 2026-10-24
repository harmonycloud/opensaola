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

package statusrule

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
)

func TestEvaluate_ProjectsPhaseReasonAndReplicasTogether(t *testing.T) {
	rules := []string{`
		object.status.phase == "Ready"
		  ? dyn({
		      "phase": "Running",
		      "reason": object.status.message,
		      "replicas": int(object.status.replicas)
		    })
		  : null
	`}
	object := map[string]interface{}{
		"status": map[string]interface{}{
			"phase":    "Ready",
			"message":  "all members ready",
			"replicas": int64(3),
		},
	}

	got, matched, err := Evaluate(context.Background(), rules, object, v1.CustomResources{Phase: v1.PhaseCreating})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !matched {
		t.Fatal("Evaluate() did not match the rule")
	}
	if got.Phase == nil || *got.Phase != "Running" {
		t.Fatalf("phase = %#v, want Running", got.Phase)
	}
	if got.Reason == nil || *got.Reason != "all members ready" {
		t.Fatalf("reason = %#v, want all members ready", got.Reason)
	}
	if got.Replicas == nil || *got.Replicas != 3 {
		t.Fatalf("replicas = %#v, want 3", got.Replicas)
	}
}

func TestEvaluate_UsesFirstNonNullRule(t *testing.T) {
	rules := []string{
		`null`,
		`{"phase": "Running"}`,
		`{"phase": "Failed"}`,
	}

	got, matched, err := Evaluate(context.Background(), rules, map[string]interface{}{}, v1.CustomResources{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !matched {
		t.Fatal("Evaluate() did not match the second rule")
	}
	if got.Phase == nil || *got.Phase != "Running" {
		t.Fatalf("phase = %#v, want first non-null result", got.Phase)
	}
}

func TestEvaluate_PreservesPreviousForLifecycleBranching(t *testing.T) {
	rules := []string{`
		previous.phase == "Running"
		  ? dyn({"phase": "Updating", "reason": "operator is reconciling"})
		  : null
	`}

	got, matched, err := Evaluate(context.Background(), rules, map[string]interface{}{}, v1.CustomResources{Phase: v1.PhaseRunning})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !matched || got.Phase == nil || *got.Phase != "Updating" {
		t.Fatalf("Evaluate() = (%#v, %v), want Updating match", got, matched)
	}
}

func TestEvaluate_LatestTrueConditionHelper(t *testing.T) {
	rules := []string{`
		latestTrueCondition(object.status.conditions).type == "Ready"
		  ? dyn({
		      "phase": "Running",
		      "reason": latestTrueCondition(object.status.conditions).message,
		      "replicas": int(object.status.coreNodesStatus.replicas) +
		        int(object.status.replicantNodesStatus.replicas)
		    })
		  : null
	`}
	object := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":               "CoreNodesProgressing",
					"status":             "True",
					"message":            "creating core nodes",
					"lastTransitionTime": "2026-08-10T08:00:00Z",
				},
				map[string]interface{}{
					"type":               "Ready",
					"status":             "True",
					"message":            "cluster ready",
					"lastTransitionTime": "2026-08-10T08:05:00Z",
				},
			},
			"coreNodesStatus":      map[string]interface{}{"replicas": int64(3)},
			"replicantNodesStatus": map[string]interface{}{"replicas": int64(2)},
		},
	}

	got, matched, err := Evaluate(context.Background(), rules, object, v1.CustomResources{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !matched {
		t.Fatal("Evaluate() did not match latest True Ready condition")
	}
	if got.Phase == nil || *got.Phase != "Running" {
		t.Fatalf("phase = %#v, want Running", got.Phase)
	}
	if got.Reason == nil || *got.Reason != "cluster ready" {
		t.Fatalf("reason = %#v, want cluster ready", got.Reason)
	}
	if got.Replicas == nil || *got.Replicas != 5 {
		t.Fatalf("replicas = %#v, want 5", got.Replicas)
	}
}

func TestEvaluate_RejectsInvalidResultObject(t *testing.T) {
	_, _, err := Evaluate(context.Background(), []string{`{"state": "Running"}`}, map[string]interface{}{}, v1.CustomResources{})
	if err == nil || !strings.Contains(err.Error(), "unsupported result key") {
		t.Fatalf("Evaluate() error = %v, want unsupported result key", err)
	}
}

func TestValidate_RejectsInvalidAndOversizedRules(t *testing.T) {
	if err := Validate([]string{`object.status.`}); err == nil {
		t.Fatal("Validate() accepted invalid CEL")
	}
	if err := Validate(make([]string, maxRulesPerGVK+1)); err == nil {
		t.Fatal("Validate() accepted too many rules")
	}
}
