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
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

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

// executeHTTP executes an HTTP step
func executeHTTP(ctx *context.Context, cli client.Client, step v1.Step, m *v1.MiddlewareAction) (err error) {
	conditionExecuteHttp := status.GetCondition(*ctx, &m.Status.Conditions, fmt.Sprintf("STEP-%s", step.Name))
	var msg string
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			logger.Log.Errorf("panic recovered in action execution: %v\n%s", r, string(buf[:n]))
			err = fmt.Errorf("panic: %v", r)
		}

		if err != nil {
			conditionExecuteHttp.Failed(*ctx, err.Error(), m.Generation)
		} else {
			conditionExecuteHttp.SuccessWithMsg(*ctx, msg, m.Generation)
		}
		if updateErr := k8s.UpdateMiddlewareActionStatus(*ctx, cli, m); updateErr != nil {
			logger.Log.Errorf("update middleware action status error: %v", updateErr)
			if err == nil {
				err = updateErr
			}
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

	if conditionExecuteHttp.Status != metav1.ConditionTrue {
		var request *http.Request
		request, err = http.NewRequest(step.HTTP.Method, step.HTTP.URL, strings.NewReader(step.HTTP.Body))
		if err != nil {
			return err
		}
		for k, v := range step.HTTP.Header {
			request.Header.Set(k, v)
		}

		var resp *http.Response
		httpClient := new(http.Client)
		httpClient.Timeout = 60 * time.Second
		resp, err = httpClient.Do(request)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		var output []byte
		output, err = io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("execute http error: %w output: %s method: %s url: %s header: %v body: %s", err, string(output), step.HTTP.Method, step.HTTP.URL, step.HTTP.Header, step.HTTP.Body)

		}

		msg = fmt.Sprintf("output: %s method: %s url: %s header: %v body: %s", string(output), step.HTTP.Method, step.HTTP.URL, step.HTTP.Header, step.HTTP.Body)

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
				stepMap[step.Name].(map[string]interface{})["output"] = string(output)
			}
			*ctx = ctxkeys.WithStep(*ctx, stepMap)
		}
	}
	return nil
}
