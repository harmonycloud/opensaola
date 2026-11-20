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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"github.com/harmonycloud/opensaola/pkg/tools/ctxkeys"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	kjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"
)

// executeCmd executes a CMD step
func executeCmd(ctx *context.Context, cli client.Client, step v1.Step, m *v1.MiddlewareAction) (err error) {
	conditionExecuteCmd := status.GetCondition(*ctx, &m.Status.Conditions, fmt.Sprintf("STEP-%s", step.Name))
	var msg string
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			log.FromContext(*ctx).Error(fmt.Errorf("panic: %v", r), "panic recovered in action execution", "stack", string(buf[:n]))
			err = fmt.Errorf("panic: %v", r)
		}
		if err != nil {
			conditionExecuteCmd.Failed(*ctx, safeActionDiagnosticMessage(err.Error()), m.Generation)
		} else {
			conditionExecuteCmd.SuccessWithMsg(*ctx, safeActionDiagnosticMessage(msg), m.Generation)
		}
	}()

	var templateValues *tools.TemplateValues
	templateValues, err = tools.GetTemplateValues(*ctx, m)
	if err != nil {
		return err
	}

	err = TemplateParseWithBaseline(*ctx, cli, &step, templateValues)
	if err != nil {
		return err
	}

	if conditionExecuteCmd.Status != metav1.ConditionTrue {
		var cmd *exec.Cmd

		if len(step.CMD.Command) == 0 {
			return errors.New("command must not be empty")
		} else {
			// SECURITY NOTE: Commands are executed via sh -c with user-provided arguments from MiddlewareAction CRD spec.
			// Access to create/modify MiddlewareAction resources MUST be restricted via Kubernetes RBAC
			// to prevent command injection by unauthorized users.
			cmd = exec.Command("sh", "-c", strings.Join(step.CMD.Command, " "))
		}

		log.FromContext(*ctx).V(1).Info("executing command step", "step", step.Name, "commandArgs", len(step.CMD.Command))

		// Use CombinedOutput to capture both stdout and stderr
		var output []byte
		output, err = cmd.CombinedOutput()
		if err != nil {
			return commandStepError(err, output)
		}
		msg = commandStepSuccessMessage(output)

		if step.Output.Expose {
			if step.Output.Type == "string" {
				output = bytes.ReplaceAll(output, []byte("\n"), nil)
			}
			return exposeStepOutput(ctx, step, output)
		}
	}
	return nil
}

// exposeStepOutput decodes a command or HTTP response for subsequent step templates.
func exposeStepOutput(ctx *context.Context, step v1.Step, output []byte) error {
	entry := make(map[string]any)
	switch step.Output.Type {
	case "json", "yaml":
		if step.Output.Type == "yaml" {
			var err error
			output, err = yaml.YAMLToJSON(output)
			if err != nil {
				return parseExposedOutputError(step.Output.Type, step.Name)
			}
		}
		var content map[string]any
		if err := kjson.UnmarshalCaseSensitivePreserveInts(output, &content); err != nil {
			return parseExposedOutputError(step.Output.Type, step.Name)
		}
		entry["output"] = exposedObjectValue(step.Output.Type, content)
	case "string":
		entry["output"] = string(output)
	}
	storeStepOutput(ctx, step.Name, entry)
	return nil
}

// Older action templates receive an empty map for a null or empty YAML document,
// and a nil map for JSON null. Keep the distinction when the object is missing.
func exposedObjectValue(format string, content map[string]any) map[string]any {
	if format == "yaml" && content == nil {
		return map[string]any{}
	}
	return content
}

func storeStepOutput(ctx *context.Context, name string, entry map[string]any) {
	steps := ctxkeys.StepFrom(*ctx)
	steps[name] = entry
	*ctx = ctxkeys.WithStep(*ctx, steps)
}
