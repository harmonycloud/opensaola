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

// Package statusrule evaluates declarative status projections carried by an
// operator baseline's managed GVK declaration.
package statusrule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"

	v1 "github.com/harmonycloud/opensaola/api/v1"
)

const (
	maxRulesPerGVK        = 32
	maxRuleSourceLength   = 8 * 1024
	maxCachedRulePrograms = 256
	statusRuleCostLimit   = 10_000
)

// Result is the optional field-level override returned by a matching CEL rule.
// A nil field preserves the generic SyncV2 projection for that field.
type Result struct {
	Phase    *string
	Reason   *string
	Replicas *int
}

// Apply overlays the fields explicitly returned by a CEL rule.
func (r Result) Apply(target *v1.CustomResources) {
	if target == nil {
		return
	}
	if r.Phase != nil {
		target.Phase = v1.Phase(*r.Phase)
	}
	if r.Reason != nil {
		target.Reason = *r.Reason
	}
	if r.Replicas != nil {
		target.Replicas = *r.Replicas
	}
}

type compiledRule struct {
	program cel.Program
	err     error
}

var (
	statusRuleEnvOnce sync.Once
	statusRuleEnv     *cel.Env
	statusRuleEnvErr  error

	statusRuleProgramCache = struct {
		sync.Mutex
		programs map[string]compiledRule
	}{
		programs: make(map[string]compiledRule),
	}
)

// Validate verifies that every configured rule is non-empty, bounded, and
// compiles in the same CEL environment used by SyncV2.
func Validate(rules []string) error {
	if len(rules) > maxRulesPerGVK {
		return fmt.Errorf("too many rules: got %d, maximum is %d", len(rules), maxRulesPerGVK)
	}
	for index, rule := range rules {
		if _, err := compile(rule); err != nil {
			return fmt.Errorf("statusRules[%d]: %w", index, err)
		}
	}
	return nil
}

// Evaluate executes rules in declaration order. A null result means the rule
// did not apply; the first object result is returned. An evaluation error is
// returned to the caller so SyncV2 can retain its generic status projection.
func Evaluate(ctx context.Context, rules []string, object map[string]interface{}, previous v1.CustomResources) (Result, bool, error) {
	if len(rules) == 0 {
		return Result{}, false, nil
	}
	if err := Validate(rules); err != nil {
		return Result{}, false, err
	}

	previousMap, err := asMap(previous)
	if err != nil {
		return Result{}, false, fmt.Errorf("convert previous customResources: %w", err)
	}
	activation := map[string]interface{}{
		"object":   object,
		"previous": previousMap,
	}

	for index, rule := range rules {
		program, err := compile(rule)
		if err != nil {
			return Result{}, false, fmt.Errorf("statusRules[%d]: %w", index, err)
		}
		value, _, err := program.ContextEval(ctx, activation)
		if err != nil {
			return Result{}, false, fmt.Errorf("statusRules[%d] evaluation: %w", index, err)
		}
		result, matched, err := decodeResult(value)
		if err != nil {
			return Result{}, false, fmt.Errorf("statusRules[%d] result: %w", index, err)
		}
		if matched {
			return result, true, nil
		}
	}
	return Result{}, false, nil
}

func environment() (*cel.Env, error) {
	statusRuleEnvOnce.Do(func() {
		statusRuleEnv, statusRuleEnvErr = cel.NewEnv(
			cel.Variable("object", cel.DynType),
			cel.Variable("previous", cel.DynType),
			cel.Function(
				"latestTrueCondition",
				cel.Overload(
					"opensaola_latest_true_condition_dyn",
					[]*cel.Type{cel.DynType},
					cel.DynType,
					cel.UnaryBinding(latestTrueCondition),
				),
			),
		)
	})
	return statusRuleEnv, statusRuleEnvErr
}

func compile(source string) (cel.Program, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("must not be empty")
	}
	if len(source) > maxRuleSourceLength {
		return nil, fmt.Errorf("is too long: got %d bytes, maximum is %d", len(source), maxRuleSourceLength)
	}

	statusRuleProgramCache.Lock()
	cached, ok := statusRuleProgramCache.programs[source]
	statusRuleProgramCache.Unlock()
	if ok {
		return cached.program, cached.err
	}

	env, err := environment()
	if err == nil {
		ast, issues := env.Compile(source)
		if issues != nil && issues.Err() != nil {
			err = issues.Err()
		} else {
			program, programErr := env.Program(
				ast,
				cel.CostLimit(statusRuleCostLimit),
				cel.InterruptCheckFrequency(100),
				cel.EvalOptions(cel.OptOptimize),
			)
			if programErr != nil {
				err = programErr
			} else {
				cached.program = program
			}
		}
	}
	cached.err = err

	statusRuleProgramCache.Lock()
	if len(statusRuleProgramCache.programs) >= maxCachedRulePrograms {
		statusRuleProgramCache.programs = make(map[string]compiledRule)
	}
	statusRuleProgramCache.programs[source] = cached
	statusRuleProgramCache.Unlock()
	return cached.program, cached.err
}

func asMap(value interface{}) (map[string]interface{}, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	result := make(map[string]interface{})
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeResult(value ref.Val) (Result, bool, error) {
	if value == nil || value.Type() == types.NullType {
		return Result{}, false, nil
	}
	if types.IsError(value) {
		return Result{}, false, fmt.Errorf("%v", value)
	}

	mapper, ok := value.(traits.Mapper)
	if !ok {
		return Result{}, false, fmt.Errorf("must return null or an object, got %s", value.Type())
	}

	result := Result{}
	iterator := mapper.Iterator()
	for iterator.HasNext() == types.True {
		keyValue := iterator.Next()
		key, ok := keyValue.Value().(string)
		if !ok {
			return Result{}, false, fmt.Errorf("result object key must be a string")
		}
		fieldValue, found := mapper.Find(keyValue)
		if !found {
			return Result{}, false, fmt.Errorf("result object lost key %q during evaluation", key)
		}

		switch key {
		case "phase":
			field, err := stringValue(fieldValue, key)
			if err != nil {
				return Result{}, false, err
			}
			result.Phase = &field
		case "reason":
			field, err := stringValue(fieldValue, key)
			if err != nil {
				return Result{}, false, err
			}
			result.Reason = &field
		case "replicas":
			field, err := replicasValue(fieldValue)
			if err != nil {
				return Result{}, false, err
			}
			result.Replicas = &field
		default:
			return Result{}, false, fmt.Errorf("unsupported result key %q", key)
		}
	}
	return result, true, nil
}

func stringValue(value ref.Val, name string) (string, error) {
	if types.IsError(value) {
		return "", fmt.Errorf("%s: %v", name, value)
	}
	result, ok := value.Value().(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", name, value.Value())
	}
	return result, nil
}

func replicasValue(value ref.Val) (int, error) {
	if types.IsError(value) {
		return 0, fmt.Errorf("replicas: %v", value)
	}
	maxInt := int64(^uint(0) >> 1)
	switch number := value.Value().(type) {
	case int64:
		if number < 0 || number > maxInt {
			return 0, fmt.Errorf("replicas must be a non-negative int, got %d", number)
		}
		return int(number), nil
	case uint64:
		if number > uint64(maxInt) {
			return 0, fmt.Errorf("replicas exceeds int range: %d", number)
		}
		return int(number), nil
	default:
		return 0, fmt.Errorf("replicas must be an int, got %T", value.Value())
	}
}

// latestTrueCondition returns the True condition with the newest valid
// lastTransitionTime. A valid timestamp wins over malformed historical data;
// when every timestamp is malformed, the first True condition is retained.
func latestTrueCondition(argument ref.Val) ref.Val {
	conditions, ok := argument.(traits.Lister)
	if !ok {
		return types.NullValue
	}

	var (
		latest        ref.Val
		latestTime    time.Time
		found         bool
		latestHasTime bool
	)
	iterator := conditions.Iterator()
	for iterator.HasNext() == types.True {
		condition := iterator.Next()
		if conditionField(condition, "status") != "True" {
			continue
		}

		transitionTime, err := time.Parse(time.RFC3339Nano, conditionField(condition, "lastTransitionTime"))
		hasTime := err == nil
		if !found ||
			(!latestHasTime && hasTime) ||
			(hasTime && latestHasTime && transitionTime.After(latestTime)) {
			latest = condition
			latestTime = transitionTime
			latestHasTime = hasTime
			found = true
		}
	}
	if !found {
		return types.NullValue
	}
	return latest
}

func conditionField(condition ref.Val, field string) string {
	mapper, ok := condition.(traits.Mapper)
	if !ok {
		return ""
	}
	value, found := mapper.Find(types.String(field))
	if !found || types.IsError(value) {
		return ""
	}
	text, ok := value.Value().(string)
	if !ok {
		return ""
	}
	return text
}
