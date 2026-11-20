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
	"errors"
	"fmt"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middlewareactionbaseline"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errOutcomeUnknown = errors.New("execution outcome unknown; verify the target before retrying")

// Execute runs one standalone Action. The status write before the first step
// prevents retries from replaying external effects after an interrupted run.
func Execute(ctx context.Context, cli client.Client, m *v1.MiddlewareAction) error {
	switch m.Status.State {
	case v1.StateUpdating:
		return failAction(ctx, cli, m, m.Status.DeepCopy(), v1.CondTypeExecuteAction, errOutcomeUnknown)
	case "":
		// Older controllers wrote step receipts without marking the Action in progress.
		for _, c := range m.Status.Conditions {
			if strings.HasPrefix(c.Type, "STEP-") {
				return failAction(ctx, cli, m, m.Status.DeepCopy(), v1.CondTypeExecuteAction, errOutcomeUnknown)
			}
		}
	default:
		return nil
	}

	baseline, err := middlewareactionbaseline.Get(ctx, cli, m.Spec.Baseline, m.Labels[v1.LabelPackageName])
	if err != nil {
		return fmt.Errorf("read action baseline %q: %w", m.Spec.Baseline, err)
	}
	steps := baseline.Spec.Steps
	if baseline.Spec.BaselineType == v1.WorkflowPreAction {
		err = errors.New("PreAction baselines run during Middleware or MiddlewareOperator reconciliation")
	} else {
		err = middlewareactionbaseline.ValidateStepNames(steps)
	}
	if err != nil {
		return failAction(ctx, cli, m, nil, v1.CondTypeChecked, err)
	}
	status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeChecked).Success(ctx, m.Generation)
	m.Status.Reason = ""
	if len(steps) == 0 {
		m.Status.State = v1.StateAvailable
		return k8s.UpdateMiddlewareActionStatus(ctx, cli, m, nil)
	}
	m.Status.State = v1.StateUpdating
	if err := k8s.UpdateMiddlewareActionStatus(ctx, cli, m, nil); err != nil {
		return err
	}

	return executeSteps(ctx, cli, m, steps)
}

func executeSteps(ctx context.Context, cli client.Client, m *v1.MiddlewareAction, steps []v1.Step) error {
	for i := range steps {
		step := *steps[i].DeepCopy()
		previous := m.Status.DeepCopy()
		var err error
		switch {
		case step.CUE != "":
			err = executeCue(&ctx, cli, step, m)
		case len(step.CMD.Command) != 0:
			err = executeCmd(&ctx, cli, step, m)
		case step.HTTP.URL != "":
			err = executeHTTP(&ctx, cli, step, m)
		default:
			err = errors.New("step requires a CUE expression, command or HTTP URL")
		}
		if err != nil {
			return failAction(ctx, cli, m, previous, "STEP-"+step.Name, err)
		}
		if i == len(steps)-1 {
			m.Status.State = v1.StateAvailable
		}
		// Record the last step and the final result in the same API write.
		if err := k8s.UpdateMiddlewareActionStatus(ctx, cli, m, previous); err != nil {
			return err
		}
	}
	return nil
}

func failAction(ctx context.Context, cli client.Client, m *v1.MiddlewareAction, previous *v1.MiddlewareActionStatus, condition string, cause error) error {
	message := safeActionDiagnosticMessage(cause.Error())
	if errors.Is(cause, errOutcomeUnknown) {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type: condition, Status: metav1.ConditionUnknown, Reason: "ExecutionOutcomeUnknown",
			Message: message, ObservedGeneration: m.Generation,
		})
	} else {
		status.GetCondition(ctx, &m.Status.Conditions, condition).Failed(ctx, message, m.Generation)
	}
	m.Status.State = v1.StateUnavailable
	m.Status.Reason = metav1.StatusReason(condition + ": " + message)
	return errors.Join(errors.New(string(m.Status.Reason)), k8s.UpdateMiddlewareActionStatus(ctx, cli, m, previous))
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
