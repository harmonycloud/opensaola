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
	"fmt"
	"runtime"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/k8s"
	"github.com/opensaola/opensaola/internal/k8s/kubeclient"
	"github.com/opensaola/opensaola/internal/service/status"
	"github.com/opensaola/opensaola/pkg/tools"
	"github.com/opensaola/opensaola/pkg/tools/ctxkeys"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// executeCue executes a CUE step
func executeCue(ctx *context.Context, cli client.Client, step v1.Step, m *v1.MiddlewareAction) (err error) {
	conditionExecuteCue := status.GetCondition(*ctx, &m.Status.Conditions, fmt.Sprintf("STEP-%s", step.Name))
	var msg string
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			log.FromContext(*ctx).Error(fmt.Errorf("panic: %v", r), "panic recovered in action execution", "stack", string(buf[:n]))
			err = fmt.Errorf("panic: %v", r)
		}

		if err != nil {
			conditionExecuteCue.Failed(*ctx, err.Error(), m.Generation)
		} else {
			conditionExecuteCue.SuccessWithMsg(*ctx, msg, m.Generation)
		}
		if updateErr := k8s.UpdateMiddlewareActionStatus(*ctx, cli, m); updateErr != nil {
			log.FromContext(*ctx).Error(updateErr, "update middleware action status error")
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

	if conditionExecuteCue.Status != metav1.ConditionTrue {
		switch step.Type {
		case StepTypeKubectlExec:
			// Build CUE value
			var (
				cueCtx              = cuecontext.New()
				inst                = cueCtx.CompileString(step.CUE)
				kind                string
				name                string
				namespace           string
				container           string
				execCommandIterator cue.Iterator
				execCommand         []string
			)

			// Extract fields
			resource := inst.LookupPath(cue.ParsePath("parameters.resource"))
			kind, err = resource.LookupPath(cue.ParsePath("kind")).String()
			if err != nil {
				return fmt.Errorf("resource kind error: %w", err)
			}
			name, err = resource.LookupPath(cue.ParsePath("name")).String()
			if err != nil {
				return fmt.Errorf("resource name error: %w", err)
			}
			namespace, err = resource.LookupPath(cue.ParsePath("namespace")).String()
			if err != nil {
				return fmt.Errorf("resource namespace error: %w", err)
			}
			container, err = resource.LookupPath(cue.ParsePath("container")).String()
			if err != nil {
				return fmt.Errorf("resource container error: %w", err)
			}
			execCommandIterator, err = resource.LookupPath(cue.ParsePath("execCommand")).List()
			if err != nil {
				return fmt.Errorf("resource container error: %w", err)
			}

			var clientSet *kubernetes.Clientset
			clientSet, err = kubeclient.GetClientSet()
			if err != nil {
				return err
			}

			for execCommandIterator.Next() {
				var cmdString string
				cmdString, err = execCommandIterator.Value().String()
				if err != nil {
					return err
				}
				execCommand = append(execCommand, cmdString)
			}

			req := clientSet.CoreV1().RESTClient().Post().
				Resource(kind).
				Name(name).
				Namespace(namespace).
				SubResource("exec").
				VersionedParams(&corev1.PodExecOptions{
					Container: container,
					Command:   execCommand,
					Stdin:     false,
					Stdout:    true,
					Stderr:    true,
					TTY:       false,
				}, scheme.ParameterCodec)

			var cfg *rest.Config
			cfg, err = ctrl.GetConfig()
			if err != nil {
				return fmt.Errorf("get kubeconfig error: %w", err)
			}

			var executor remotecommand.Executor
			executor, err = remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
			if err != nil {
				return err
			}

			var stdout, stderr bytes.Buffer
			err = executor.StreamWithContext(*ctx, remotecommand.StreamOptions{
				Stdout: &stdout,
				Stderr: &stderr,
				Tty:    false,
			})
			if err != nil {
				return fmt.Errorf("execute kubectl error: %s output: %s command: %s", stderr.String(), stdout.String(), strings.Join(execCommand, " "))
			}

			msg = fmt.Sprintf("output: %s command: %s", stdout.String(), strings.Join(execCommand, " "))

			output := stdout.Bytes()
			if step.Output.Expose {
				stepMap := ctxkeys.StepFrom(*ctx)
				stepEntry := make(map[string]interface{})
				stepMap[step.Name] = stepEntry

				var outputMap = make(map[string]interface{})
				switch step.Output.Type {
				case "json":
					err = json.Unmarshal(output, &outputMap)
					if err != nil {
						return err
					}
					stepEntry["output"] = outputMap
				case "yaml":
					err = yaml.Unmarshal(output, &outputMap)
					if err != nil {
						return err
					}
					stepEntry["output"] = outputMap
				case "string":
					stepEntry["output"] = string(output)
				}
				*ctx = ctxkeys.WithStep(*ctx, stepMap)
			}
		case StepTypeKubectlGet:
			// Build CUE value
			var (
				cueCtx     = cuecontext.New()
				inst       = cueCtx.CompileString(step.CUE)
				apiversion string
				kind       string
				name       string
				namespace  string
			)

			// Extract fields
			resource := inst.LookupPath(cue.ParsePath("parameters.resource"))
			apiversion, err = resource.LookupPath(cue.ParsePath("apiversion")).String()
			if err != nil {
				return fmt.Errorf("resource apiversion error: %w", err)
			}
			kind, err = resource.LookupPath(cue.ParsePath("kind")).String()
			if err != nil {
				return fmt.Errorf("resource kind error: %w", err)
			}
			name, err = resource.LookupPath(cue.ParsePath("name")).String()
			if err != nil {
				return fmt.Errorf("resource name error: %w", err)
			}
			namespace, err = resource.LookupPath(cue.ParsePath("namespace")).String()
			if err != nil {
				return fmt.Errorf("resource namespace error: %w", err)
			}

			var obj = new(unstructured.Unstructured)
			obj.SetAPIVersion(apiversion)
			obj.SetKind(kind)
			obj.SetName(name)
			obj.SetNamespace(namespace)

			obj, getCRerr := k8s.GetCustomResource(*ctx, cli, name, namespace, obj.GroupVersionKind())
			if getCRerr != nil && !apiErrors.IsNotFound(getCRerr) {
				return getCRerr
			}

			if step.Output.Expose {
				stepMap := ctxkeys.StepFrom(*ctx)
				stepEntry := make(map[string]interface{})
				stepMap[step.Name] = stepEntry

				var outputMap = make(map[string]interface{})
				switch step.Output.Type {
				case "json":
					var output []byte
					output, err = json.Marshal(obj)
					if err != nil {
						return err
					}
					err = json.Unmarshal(output, &outputMap)
					if err != nil {
						return err
					}
					stepEntry["output"] = outputMap
				case "yaml":
					var output []byte
					output, err = yaml.Marshal(obj)
					if err != nil {
						return err
					}
					err = yaml.Unmarshal(output, &outputMap)
					if err != nil {
						return err
					}
					stepEntry["output"] = outputMap
				case "string":

				}
				*ctx = ctxkeys.WithStep(*ctx, stepMap)
			}
		case StepTypeKubectlEdit:
			// Build CUE value
			var (
				cueCtx      = cuecontext.New()
				inst        = cueCtx.CompileString(step.CUE)
				apiversion  string
				kind        string
				name        string
				namespace   string
				labels      map[string]string
				annotations map[string]string
			)

			// Extract fields
			resource := inst.LookupPath(cue.ParsePath("parameters.resource"))
			apiversion, err = resource.LookupPath(cue.ParsePath("apiversion")).String()
			if err != nil {
				return fmt.Errorf("resource apiversion error: %w", err)
			}
			kind, err = resource.LookupPath(cue.ParsePath("kind")).String()
			if err != nil {
				return fmt.Errorf("resource kind error: %w", err)
			}
			name, err = resource.LookupPath(cue.ParsePath("name")).String()
			if err != nil {
				return fmt.Errorf("resource name error: %w", err)
			}
			namespace, err = resource.LookupPath(cue.ParsePath("namespace")).String()
			if err != nil {
				return fmt.Errorf("resource namespace error: %w", err)
			}

			// Parse labels and annotations
			var labelsBytes []byte
			labelsBytes, err = resource.LookupPath(cue.ParsePath("labels")).MarshalJSON()
			if err == nil {
				if err := json.Unmarshal(labelsBytes, &labels); err != nil {
					return fmt.Errorf("unmarshal labels failed: %w", err)
				}
			}
			var annotationsBytes []byte
			annotationsBytes, err = resource.LookupPath(cue.ParsePath("annotations")).MarshalJSON()
			if err == nil {
				if err := json.Unmarshal(annotationsBytes, &annotations); err != nil {
					return fmt.Errorf("unmarshal annotations failed: %w", err)
				}
			}

			var outputJson []byte
			outputJson, err = inst.LookupPath(cue.ParsePath("output")).MarshalJSON()
			if err != nil {
				return fmt.Errorf("resource output error: %w", err)
			}
			outputMap, err := tools.JsonToMap(outputJson)
			if err != nil {
				return fmt.Errorf("json to map error: %w", err)
			}

			var obj = new(unstructured.Unstructured)
			obj.SetAPIVersion(apiversion)
			obj.SetKind(kind)
			obj.SetName(name)
			obj.SetNamespace(namespace)
			obj.SetLabels(labels)
			obj.SetAnnotations(annotations)

			cr, getCRerr := k8s.GetCustomResource(*ctx, cli, name, namespace, obj.GroupVersionKind())
			if getCRerr != nil && !apiErrors.IsNotFound(getCRerr) {
				return getCRerr
			}

			// Create if not exists
			if getCRerr != nil && apiErrors.IsNotFound(getCRerr) {
				var objJson []byte
				objJson, err = json.Marshal(obj)
				if err != nil {
					return fmt.Errorf("json marshal error: %w", err)
				}
				var objMap = make(map[string]interface{})
				err = json.Unmarshal(objJson, &objMap)
				if err != nil {
					return fmt.Errorf("json unmarshal error: %w", err)
				}
				patch := tools.MergeMap(objMap, outputMap)
				var patchJson []byte
				patchJson, err = json.Marshal(patch)
				if err != nil {
					return fmt.Errorf("json marshal error: %w", err)
				}
				err = json.Unmarshal(patchJson, &obj)
				if err != nil {
					return fmt.Errorf("json unmarshal error: %w", err)
				}
				// TODO: some resources have no spec, need to handle
				// Skip creation if spec is empty or has no keys
				specMap, ok := obj.Object["spec"].(map[string]interface{})
				if !ok || len(specMap) == 0 {
					return nil
				}

				err = k8s.CreateCustomResource(*ctx, cli, obj)
				if err != nil {
					return fmt.Errorf("CreateCustomResource err :%w", err)
				}
			} else {

				crAnnotations := cr.GetAnnotations()
				for k, v := range annotations {
					crAnnotations[k] = v
				}
				cr.SetAnnotations(crAnnotations)

				crLabels := cr.GetLabels()
				for k, v := range labels {
					crLabels[k] = v
				}
				cr.SetLabels(crLabels)

				var crJson []byte
				crJson, err = json.Marshal(cr)
				if err != nil {
					return fmt.Errorf("json marshal error: %w", err)
				}
				var crMap = make(map[string]interface{})
				err = json.Unmarshal(crJson, &crMap)
				if err != nil {
					return fmt.Errorf("json unmarshal error: %w", err)
				}
				patch := tools.MergeMap(crMap, outputMap)
				var patchJson []byte
				patchJson, err = json.Marshal(patch)
				if err != nil {
					return fmt.Errorf("json marshal error: %w", err)
				}
				err = json.Unmarshal(patchJson, cr)
				if err != nil {
					return fmt.Errorf("json unmarshal error: %w", err)
				}
				err = k8s.PatchCustomResource(*ctx, cli, cr)
				if err != nil {
					return fmt.Errorf("UpdateCustomResource err :%w", err)
				}
			}
		}
	}

	return nil
}

// mapCue maps CUE to JSON
func mapCue(ctx context.Context, step *v1.Step) error {
	if step.CUE == "" {
		return nil
	}
	// Build CUE value
	bi := build.NewContext().NewInstance("-", nil)
	if err := tools.ParseAndAddCueFile(bi, "-", step.CUE); err != nil {
		return fmt.Errorf("parse and add cue file error: %w", err)
	}
	inst := cuecontext.New().BuildInstance(bi)
	instBytes, err := inst.MarshalJSON()
	if err != nil {
		return fmt.Errorf("inst to string error: %w", err)
	}
	step.CUE = string(instBytes)
	return nil
}
