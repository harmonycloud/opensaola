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
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middlewareactionbaseline"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/pkg/tools"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Check validates a MiddlewareAction
func Check(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	conditionChecked := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked)
	if conditionChecked.Status != metav1.ConditionTrue {
		conditionChecked.Success(ctx, m.Generation)
		if err := k8s.UpdateMiddlewareActionStatus(ctx, cli, m); err != nil {
			return fmt.Errorf("update middleware operator status error: %w", err)
		}
	}
	return nil
}

// Execute executes the action
func Execute(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) (err error) {
	var actionBaseline v1.MiddlewareActionBaseline
	actionBaseline, err = middlewareactionbaseline.Get(ctx, cli, m.Spec.Baseline, m.Labels[v1.LabelPackageName])
	if err != nil {
		return fmt.Errorf("get baseline error: %w", err)
	}

	// Execute steps
	for _, step := range actionBaseline.Spec.Steps {
		// Execute CUE
		if step.CUE != "" {
			if err = executeCue(&ctx, cli, step, m); err != nil {
				return fmt.Errorf("execute cue error: %w", err)
			}
		} else if len(step.CMD.Command) != 0 {
			err = executeCmd(&ctx, cli, step, m)
			if err != nil {
				return fmt.Errorf("execute cmd error: %w", err)
			}
		} else if step.HTTP.URL != "" {
			err = executeHTTP(&ctx, cli, step, m)
			if err != nil {
				return fmt.Errorf("execute http error: %w", err)
			}
		}

	}

	return nil
}

// TemplateParseWithBaseline parses and merges the template with baseline
func TemplateParseWithBaseline(ctx context.Context, cli client.Client, step *v1.Step, templateValues *tools.TemplateValues) error {
	if step.CUE != "" {
		cueParse, err := tools.TemplateParse(ctx, step.CUE, templateValues)
		if err != nil {
			return fmt.Errorf("template parse error: %w", err)
		}

		step.CUE = cueParse
	}

	for idx, v := range step.CMD.Command {
		parse, err := tools.TemplateParse(ctx, v, templateValues)
		if err != nil {
			return fmt.Errorf("template parse error: %w", err)
		}
		step.CMD.Command[idx] = parse
	}

	if step.HTTP.URL != "" {
		parse, err := tools.TemplateParse(ctx, step.HTTP.URL, templateValues)
		if err != nil {
			return fmt.Errorf("template parse error: %w", err)
		}
		step.HTTP.URL = parse

		parse, err = tools.TemplateParse(ctx, step.HTTP.Method, templateValues)
		if err != nil {
			return fmt.Errorf("template parse error: %w", err)
		}
		step.HTTP.Method = parse

		parse, err = tools.TemplateParse(ctx, step.HTTP.Body, templateValues)
		if err != nil {
			return fmt.Errorf("template parse error: %w", err)
		}
		step.HTTP.Body = parse

		var headersJson []byte
		headersJson, err = json.Marshal(step.HTTP.Header)
		if err != nil {
			return fmt.Errorf("marshal headers error: %w", err)
		}
		var headersString string
		headersString, err = tools.TemplateParse(ctx, string(headersJson), templateValues)
		if err != nil {
			return fmt.Errorf("template parse error: %w", err)
		}

		err = json.Unmarshal([]byte(headersString), &step.HTTP.Header)
		if err != nil {
			return fmt.Errorf("unmarshal headers error: %w", err)
		}
	}

	return nil
}
