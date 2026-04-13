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
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/internal/k8s"
	"github.com/OpenSaola/opensaola/internal/resource/logger"
	"github.com/OpenSaola/opensaola/internal/service/status"
	"github.com/OpenSaola/opensaola/pkg/tools"
	"github.com/OpenSaola/opensaola/pkg/tools/ctxkeys"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
			logger.Log.Errorf("panic recovered in action execution: %v\n%s", r, string(buf[:n]))
			err = fmt.Errorf("panic: %v", r)
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
		defer func() {
			if err != nil {
				conditionExecuteCmd.Failed(*ctx, err.Error(), m.Generation)
			} else {
				conditionExecuteCmd.SuccessWithMsg(*ctx, msg, m.Generation)
			}
			if m.Name != "" {
				if updateErr := k8s.UpdateMiddlewareActionStatus(*ctx, cli, m); updateErr != nil {
					logger.Log.Errorf("update middleware action status error: %v", updateErr)
					if err == nil {
						err = updateErr
					}
				}
			}
		}()
		var cmd *exec.Cmd

		if len(step.CMD.Command) == 0 {
			return errors.New("command must not be empty")
		} else {
			// SECURITY NOTE: Commands are executed via sh -c with user-provided arguments from MiddlewareAction CRD spec.
			// Access to create/modify MiddlewareAction resources MUST be restricted via Kubernetes RBAC
			// to prevent command injection by unauthorized users.
			cmd = exec.Command("sh", "-c", strings.Join(step.CMD.Command, " "))
		}

		logger.Log.Debug(strings.Join(step.CMD.Command, " "))

		// Use CombinedOutput to capture both stdout and stderr
		var output []byte
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w;%s", err, string(output))
		}
		msg = fmt.Sprintf("%s", string(output))

		if step.Output.Expose {
			stepMap := ctxkeys.StepFrom(*ctx)
			stepMap[step.Name] = make(map[string]interface{})

			var outputMap = make(map[string]interface{})
			switch step.Output.Type {
			case "json":
				err = json.Unmarshal(output, &outputMap)
				if err != nil {
					return err
				}
				stepMap[step.Name].(map[string]interface{})["output"] = outputMap
			case "yaml":
				err = yaml.Unmarshal(output, &outputMap)
				if err != nil {
					return err
				}
				stepMap[step.Name].(map[string]interface{})["output"] = outputMap
			case "string":
				output = bytes.ReplaceAll(output, []byte("\n"), []byte(""))
				stepMap[step.Name].(map[string]interface{})["output"] = string(output)
			}
			*ctx = ctxkeys.WithStep(*ctx, stepMap)
		}
	}
	return nil
}
