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
	"maps"
	"runtime"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/cuecontext"
	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/k8s/kubeclient"
	"github.com/harmonycloud/opensaola/internal/service/status"
	"github.com/harmonycloud/opensaola/pkg/tools"
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
	kjson "sigs.k8s.io/json"
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
			conditionExecuteCue.Failed(*ctx, safeActionDiagnosticMessage(err.Error()), m.Generation)
		} else {
			conditionExecuteCue.SuccessWithMsg(*ctx, safeActionDiagnosticMessage(msg), m.Generation)
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
				return kubectlExecError(err, execCommand, stdout.Bytes(), stderr.Bytes())
			}

			msg = kubectlExecSuccessMessage(execCommand, stdout.Bytes())

			if step.Output.Expose {
				return exposeStepOutput(ctx, step, stdout.Bytes())
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
				entry := make(map[string]any)
				if step.Output.Type == "json" || step.Output.Type == "yaml" {
					var content map[string]any
					if obj != nil {
						content = obj.Object
					}
					entry["output"] = exposedObjectValue(step.Output.Type, content)
				}
				storeStepOutput(ctx, step.Name, entry)
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
			var outputMap map[string]any
			if err := kjson.UnmarshalCaseSensitivePreserveInts(outputJson, &outputMap); err != nil {
				return fmt.Errorf("decode action output: %w", err)
			}
			target := &unstructured.Unstructured{}
			target.SetAPIVersion(apiversion)
			target.SetKind(kind)
			target.SetName(name)
			target.SetNamespace(namespace)
			if err := editActionResource(*ctx, cli, target, labels, annotations, outputMap); err != nil {
				return err
			}
		}
	}

	return nil
}

// editActionResource applies a one-off CUE overlay to exactly the object read.
func editActionResource(ctx context.Context, cli client.Client, target *unstructured.Unstructured, labels, annotations map[string]string, output map[string]any) error {
	live, err := k8s.GetCustomResource(ctx, cli, target.GetName(), target.GetNamespace(), target.GroupVersionKind())
	if err != nil && !apiErrors.IsNotFound(err) {
		return err
	}
	create := apiErrors.IsNotFound(err)
	after := target.DeepCopy()
	if !create {
		after = live.DeepCopy()
	}
	if len(labels) != 0 {
		current := after.GetLabels()
		if current == nil {
			current = map[string]string{}
		}
		maps.Copy(current, labels)
		after.SetLabels(current)
	}
	if len(annotations) != 0 {
		current := after.GetAnnotations()
		if current == nil {
			current = map[string]string{}
		}
		maps.Copy(current, annotations)
		after.SetAnnotations(current)
	}
	if create {
		after.Object = tools.MergeMap(after.Object, output)
	} else {
		after, err = mergeActionOutput(ctx, k8s.NewManagedResourceWriter(cli).IntentSchemas, after, output)
		if err != nil {
			return err
		}
	}
	if after.GroupVersionKind() != target.GroupVersionKind() || client.ObjectKeyFromObject(after) != client.ObjectKeyFromObject(target) {
		return fmt.Errorf("action output changed the target identity")
	}
	if create {
		if err := k8s.ValidateActionMetadata(nil, after); err != nil {
			return err
		}
		spec, _ := after.Object["spec"].(map[string]any)
		if len(spec) == 0 {
			return nil
		}
		return cli.Create(ctx, after, client.FieldOwner("opensaola-action"))
	}
	return k8s.PatchActionResource(ctx, cli, live, after)
}

// A one-off edit has no deletion history. Use the same list keys as the
// shared writer for business fields. Explicit status and metadata edits keep
// their existing semantics and are checked against the original target below.
func mergeActionOutput(ctx context.Context, schemas k8s.IntentSchemaResolver, after *unstructured.Unstructured, output map[string]any) (*unstructured.Unstructured, error) {
	envelope := make(map[string]any)
	fields := make(map[string]any)
	for key, value := range output {
		switch key {
		case "apiVersion", "kind", "metadata", "status":
			envelope[key] = value
		default:
			fields[key] = value
		}
	}
	targetGVK := after.GroupVersionKind()
	targetKey := client.ObjectKeyFromObject(after)
	after.Object = tools.MergeMap(after.Object, envelope)
	if after.GroupVersionKind() != targetGVK || client.ObjectKeyFromObject(after) != targetKey {
		return nil, fmt.Errorf("action output changed the target identity")
	}
	if len(fields) == 0 {
		return after, nil
	}
	if schemas == nil {
		return nil, fmt.Errorf("action field edit requires a resource schema resolver")
	}
	converter, _, err := schemas.Resolve(ctx, targetGVK)
	if err != nil {
		return nil, err
	}
	declaration := &unstructured.Unstructured{Object: fields}
	declaration.SetGroupVersionKind(targetGVK)
	declaration.SetName(targetKey.Name)
	declaration.SetNamespace(targetKey.Namespace)
	return k8s.OverlayActionFields(converter, after, declaration)
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
