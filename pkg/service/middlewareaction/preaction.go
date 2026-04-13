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

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/middlewareactionbaseline"
	"github.com/OpenSaola/opensaola/pkg/service/status"
	"github.com/OpenSaola/opensaola/pkg/tools"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func HandlePreActions(ctx context.Context, cli client.Client, m tools.Quoter) (err error) {
	for _, preAction := range m.GetPreActions() {
		var mad v1.MiddlewareActionBaseline
		mad, err = middlewareactionbaseline.Get(ctx, cli, preAction.Name, m.GetLabels()[v1.LabelPackageName])
		if err != nil {
			logger.Log.Errorf("get middleware action baseline error: %v", err)
			return err
		}
		if mad.Spec.BaselineType != v1.WorkflowPreAction {
			return fmt.Errorf("pre action %s is not pre action", mad.Name)
		}
		// err = TemplateParsePreAction(ctx, cli, &mad, m)
		// if err != nil {
		// 	logger.Log.Errorf("handle pre action error: %v", err)
		// 	return err
		// }

		var mBytes []byte
		mBytes, err = json.Marshal(m)
		if err != nil {
			logger.Log.Errorf("marshal middleware error: %v", err)
			return err
		}
		temp := new(unstructured.Unstructured)
		err = json.Unmarshal(mBytes, temp)
		if err != nil {
			logger.Log.Errorf("unmarshal middleware error: %v", err)
			return err
		}

		ma := new(v1.MiddlewareAction)
		ma.Spec.Baseline = preAction.Name
		ma.Labels = m.GetLabels()
		ma.Spec.Necessary = preAction.Parameters

		err = ExecutePreAction(ctx, cli, temp, &mad, ma)
		if err != nil {
			logger.Log.Errorf("execute pre action error: %s %v", mad.Name, err)
			return err
		}

		var tempBytes []byte
		tempBytes, err = json.Marshal(temp)
		if err != nil {
			logger.Log.Errorf("marshal temp error: %v", err)
			return err
		}

		err = json.Unmarshal(tempBytes, m)
		if err != nil {
			logger.Log.Errorf("unmarshal temp error: %v", err)
			return err
		}
	}
	return nil
}

func TemplateParsePreAction(ctx context.Context, cli client.Client, mad *v1.MiddlewareActionBaseline, m *v1.Middleware) (err error) {
	templateValues, err := tools.GetTemplateValues(context.WithValue(ctx, tools.TemplateValuesPreActionNameKey, mad.Name), m)
	if err != nil {
		return fmt.Errorf("get template values error: %w", err)
	}

	mSpecBytes, err := json.Marshal(mad.Spec)
	if err != nil {
		return fmt.Errorf("marshal mad spec error: %w", err)
	}
	mSpecParse, err := tools.TemplateParse(ctx, string(mSpecBytes), templateValues)
	if err != nil {
		return err
	}

	err = json.Unmarshal([]byte(mSpecParse), &mad.Spec)
	if err != nil {
		return fmt.Errorf("unmarshal mSpecMap error: %w", err)
	}

	return nil
}

// ExecutePreAction executes pre-actions
func ExecutePreAction(ctx context.Context, cli client.Client, obj *unstructured.Unstructured, mad *v1.MiddlewareActionBaseline, m *v1.MiddlewareAction) (err error) {
	var (
		quoter   tools.Quoter
		objBytes []byte
	)

	objBytes, err = obj.MarshalJSON()
	if err != nil {
		return err
	}

	var parameters map[string]any

	switch obj.GetKind() {
	case "Middleware":
		var middleware = new(v1.Middleware)
		err = json.Unmarshal(objBytes, middleware)
		if err != nil {
			return err
		}
		quoter = middleware
		parameters = make(map[string]any)
		err = json.Unmarshal(middleware.Spec.Parameters.Raw, &parameters)
		if err != nil {
			return err
		}
	case "MiddlewareOperator":
		var middlewareOperator = new(v1.MiddlewareOperator)
		err = json.Unmarshal(objBytes, middlewareOperator)
		if err != nil {
			return err
		}
		quoter = middlewareOperator
	}

	var templateValues *tools.TemplateValues
	templateValues, err = tools.GetTemplateValues(ctx, quoter)
	if err != nil {
		return err
	}

	if m.Spec.Necessary.Raw != nil {
		err = json.Unmarshal(m.Spec.Necessary.Raw, &templateValues.Necessary)
		if err != nil {
			return err
		}
	}

	if parameters != nil {
		templateValues.Parameters = parameters
	}

	// Execute steps
	for _, step := range mad.Spec.Steps {
		err = TemplateParseWithBaseline(ctx, cli, &step, templateValues)
		if err != nil {
			return err
		}
		// Execute CUE
		if step.CUE != "" {
			if err = mapCue(ctx, &step); err != nil {
				return fmt.Errorf("handle cue error: %s %w", mad.Name, err)
			}
			if err = executePreActionCue(ctx, step.CUE, obj, m); err != nil {
				return fmt.Errorf("execute cue error: %w", err)
			}
		} else if len(step.CMD.Command) != 0 {
			err = executeCmd(&ctx, cli, step, m)
			if err != nil {
				return fmt.Errorf("execute cmd error: %w", err)
			}
		}
	}

	return nil
}

// executePreActionCue executes CUE for pre-actions
func executePreActionCue(ctx context.Context, cueString string, obj *unstructured.Unstructured, m *v1.MiddlewareAction) (err error) {
	conditionExecuteCue := status.GetCondition(ctx, &m.Status.Conditions, v1.CondTypeExecuteCue)
	defer func() {
		if err != nil {
			conditionExecuteCue.Failed(ctx, err.Error(), m.Generation)
		} else {
			conditionExecuteCue.Success(ctx, m.Generation)
		}
	}()

	// Build CUE value
	cueCtx := cuecontext.New()
	inst := cueCtx.CompileString(cueString)

	// Extract fields
	resource := inst.LookupPath(cue.ParsePath("parameters.resource"))
	apiversion, err := resource.LookupPath(cue.ParsePath("apiversion")).String()
	if err != nil {
		return fmt.Errorf("resource apiversion error: %w", err)
	}
	kind, err := resource.LookupPath(cue.ParsePath("kind")).String()
	if err != nil {
		return fmt.Errorf("resource kind error: %w", err)
	}
	name, err := resource.LookupPath(cue.ParsePath("name")).String()
	if err != nil {
		return fmt.Errorf("resource name error: %w", err)
	}
	namespace, err := resource.LookupPath(cue.ParsePath("namespace")).String()
	if err != nil {
		return fmt.Errorf("resource namespace error: %w", err)
	}

	if obj.GetAPIVersion() != apiversion || obj.GetKind() != kind || obj.GetName() != name || obj.GetNamespace() != namespace {
		return fmt.Errorf("resource mismatch error: %w", err)
	}

	cueJson, err := inst.LookupPath(cue.ParsePath("output")).MarshalJSON()
	if err != nil {
		return fmt.Errorf("inst to string error: %w", err)
	}

	objJson, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("inst to string error: %w", err)
	}

	cueMap := make(map[string]interface{})
	err = json.Unmarshal(cueJson, &cueMap)
	if err != nil {
		return fmt.Errorf("unmarshal cue error: %w", err)
	}

	objMap := make(map[string]interface{})
	err = json.Unmarshal(objJson, &objMap)
	if err != nil {
		return fmt.Errorf("unmarshal obj error: %w", err)
	}

	patches := tools.MergeMap(objMap, cueMap)
	if err != nil {
		return fmt.Errorf("merge merge patches error: %w", err)
	}

	patcheBytes, err := json.Marshal(patches)
	if err != nil {
		return fmt.Errorf("marshal patches error: %w", err)
	}

	err = json.Unmarshal(patcheBytes, obj)
	if err != nil {
		return fmt.Errorf("unmarshal deployment error: %w", err)
	}

	return nil
}

