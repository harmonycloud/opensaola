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

package synchronizer

import (
	"fmt"
	"sort"
	"strings"

	zeusv1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func deriveMiddlewareReadinessDiagnostic(
	mid *zeusv1.Middleware,
	cr *unstructured.Unstructured,
	pods map[string]corev1.Pod,
	pvcs map[string]corev1.PersistentVolumeClaim,
) string {
	if diagnostic := middlewarePodWaitingDiagnostic(mid, cr, pods); diagnostic != "" {
		return diagnostic
	}
	if diagnostic := middlewarePodConditionDiagnostic(mid, cr, pods); diagnostic != "" {
		return diagnostic
	}
	if diagnostic := middlewarePVCPendingDiagnostic(mid, cr, pvcs); diagnostic != "" {
		return diagnostic
	}
	return ""
}

func middlewarePodConditionDiagnostic(mid *zeusv1.Middleware, cr *unstructured.Unstructured, pods map[string]corev1.Pod) string {
	podNames := sortedMapKeys(pods)
	for _, podName := range podNames {
		pod := pods[podName]
		for _, condition := range pod.Status.Conditions {
			if condition.Status != corev1.ConditionFalse && condition.Status != corev1.ConditionUnknown {
				continue
			}
			if condition.Type != corev1.PodScheduled && condition.Type != corev1.ContainersReady && condition.Type != corev1.PodReady {
				continue
			}
			if condition.Type != corev1.PodScheduled && hasTransientMiddlewareWaitingContainer(pod) {
				continue
			}
			return middlewareRuntimeDiagnostic(mid, cr, status.Diagnostic{
				Phase:              status.PhaseWorkloadReadiness,
				FailedObject:       status.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name},
				Owner:              objectRefFromUnstructured(cr),
				FieldPath:          fmt.Sprintf("status.conditions[type=%s]", condition.Type),
				Expected:           "pod condition True",
				Actual:             string(condition.Status),
				Generation:         mid.Generation,
				ObservedGeneration: mid.Status.ObservedGeneration,
				Next:               fmt.Sprintf("kubectl describe pod %s -n %s; kubectl get events -n %s --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", pod.Name, pod.Namespace, pod.Namespace, pod.Name),
				Cause: fmt.Errorf("%s", runtimeCause([]string{
					fmt.Sprintf("podCondition=%s", condition.Type),
					diagnosticValue("nodeName", pod.Spec.NodeName),
					diagnosticValue("serviceAccountName", pod.Spec.ServiceAccountName),
					fmt.Sprintf("reason=%s", condition.Reason),
					fmt.Sprintf("message=%s", condition.Message),
				})),
			})
		}
	}
	return ""
}

func middlewarePodWaitingDiagnostic(mid *zeusv1.Middleware, cr *unstructured.Unstructured, pods map[string]corev1.Pod) string {
	podNames := sortedMapKeys(pods)
	for _, podName := range podNames {
		pod := pods[podName]
		if waiting := firstMiddlewareWaitingContainer(pod); waiting != nil {
			return middlewareRuntimeDiagnostic(mid, cr, status.Diagnostic{
				Phase:              status.PhaseWorkloadReadiness,
				FailedObject:       status.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name},
				Owner:              objectRefFromUnstructured(cr),
				FieldPath:          waiting.fieldPath,
				Expected:           "container ready",
				Actual:             waiting.reason,
				Generation:         mid.Generation,
				ObservedGeneration: mid.Status.ObservedGeneration,
				Next:               fmt.Sprintf("kubectl describe pod %s -n %s; kubectl get events -n %s --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", pod.Name, pod.Namespace, pod.Namespace, pod.Name),
				Cause: fmt.Errorf("%s", runtimeCause([]string{
					fmt.Sprintf("container=%s", waiting.container),
					fmt.Sprintf("image=%s", waiting.image),
					diagnosticValue("nodeName", pod.Spec.NodeName),
					diagnosticValue("serviceAccountName", pod.Spec.ServiceAccountName),
					fmt.Sprintf("reason=%s", waiting.reason),
					fmt.Sprintf("message=%s", waiting.message),
				})),
			})
		}
	}
	return ""
}

func middlewarePVCPendingDiagnostic(mid *zeusv1.Middleware, cr *unstructured.Unstructured, pvcs map[string]corev1.PersistentVolumeClaim) string {
	pvcNames := sortedMapKeys(pvcs)
	for _, pvcName := range pvcNames {
		pvc := pvcs[pvcName]
		if pvc.Status.Phase != corev1.ClaimPending {
			continue
		}
		return middlewareRuntimeDiagnostic(mid, cr, status.Diagnostic{
			Phase:              status.PhaseWorkloadReadiness,
			FailedObject:       status.ObjectRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name},
			Owner:              objectRefFromUnstructured(cr),
			FieldPath:          "status.phase",
			Expected:           string(corev1.ClaimBound),
			Actual:             string(pvc.Status.Phase),
			Generation:         mid.Generation,
			ObservedGeneration: mid.Status.ObservedGeneration,
			Next:               fmt.Sprintf("kubectl describe pvc %s -n %s; kubectl get events -n %s --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", pvc.Name, pvc.Namespace, pvc.Namespace, pvc.Name),
			Cause: fmt.Errorf("%s", runtimeCause([]string{
				"pvc pending",
				fmt.Sprintf("phase=%s", pvc.Status.Phase),
				diagnosticValue("storageClassName", pvcStorageClassName(pvc)),
				diagnosticValue("accessModes", pvcAccessModes(pvc)),
				diagnosticValue("requestedStorage", pvcRequestedStorage(pvc)),
			})),
		})
	}
	return ""
}

func middlewareRuntimeDiagnostic(mid *zeusv1.Middleware, cr *unstructured.Unstructured, diagnostic status.Diagnostic) string {
	diagnostic.Controller = "middleware-synchronizer"
	diagnostic.Resource = status.ObjectRef{
		APIVersion: "middleware.cn/v1",
		Kind:       "Middleware",
		Namespace:  mid.Namespace,
		Name:       mid.Name,
	}
	if diagnostic.Owner.Kind == "" && cr != nil {
		diagnostic.Owner = objectRefFromUnstructured(cr)
	}
	return diagnostic.Message()
}

type middlewareWaitingContainer struct {
	container string
	image     string
	reason    string
	message   string
	fieldPath string
}

func firstMiddlewareWaitingContainer(pod corev1.Pod) *middlewareWaitingContainer {
	for _, container := range pod.Status.InitContainerStatuses {
		if container.State.Waiting == nil || isTransientMiddlewareWaitingReason(container.State.Waiting.Reason) {
			continue
		}
		return &middlewareWaitingContainer{
			container: container.Name,
			image:     container.Image,
			reason:    container.State.Waiting.Reason,
			message:   container.State.Waiting.Message,
			fieldPath: fmt.Sprintf("status.initContainerStatuses[name=%s].state.waiting", container.Name),
		}
	}
	for _, container := range pod.Status.ContainerStatuses {
		if container.State.Waiting == nil || isTransientMiddlewareWaitingReason(container.State.Waiting.Reason) {
			continue
		}
		return &middlewareWaitingContainer{
			container: container.Name,
			image:     container.Image,
			reason:    container.State.Waiting.Reason,
			message:   container.State.Waiting.Message,
			fieldPath: fmt.Sprintf("status.containerStatuses[name=%s].state.waiting", container.Name),
		}
	}
	return nil
}

func hasTransientMiddlewareWaitingContainer(pod corev1.Pod) bool {
	for _, container := range pod.Status.InitContainerStatuses {
		if container.State.Waiting != nil && isTransientMiddlewareWaitingReason(container.State.Waiting.Reason) {
			return true
		}
	}
	for _, container := range pod.Status.ContainerStatuses {
		if container.State.Waiting != nil && isTransientMiddlewareWaitingReason(container.State.Waiting.Reason) {
			return true
		}
	}
	return false
}

func isTransientMiddlewareWaitingReason(reason string) bool {
	switch reason {
	case "ContainerCreating", "PodInitializing":
		return true
	default:
		return false
	}
}

func objectRefFromUnstructured(obj *unstructured.Unstructured) status.ObjectRef {
	if obj == nil {
		return status.ObjectRef{}
	}
	return status.ObjectRef{
		APIVersion: obj.GetAPIVersion(),
		Kind:       obj.GetKind(),
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
	}
}

func sortedMapKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func runtimeCause(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, "; ")
}

func diagnosticValue(key, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return key + "=" + value
}

func pvcStorageClassName(pvc corev1.PersistentVolumeClaim) string {
	if pvc.Spec.StorageClassName == nil {
		return ""
	}
	return *pvc.Spec.StorageClassName
}

func pvcAccessModes(pvc corev1.PersistentVolumeClaim) string {
	if len(pvc.Spec.AccessModes) == 0 {
		return ""
	}
	modes := make([]string, 0, len(pvc.Spec.AccessModes))
	for _, mode := range pvc.Spec.AccessModes {
		modes = append(modes, string(mode))
	}
	return strings.Join(modes, ",")
}

func pvcRequestedStorage(pvc corev1.PersistentVolumeClaim) string {
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.IsZero() {
		return ""
	}
	return storage.String()
}
