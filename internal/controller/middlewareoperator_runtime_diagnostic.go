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

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/harmonycloud/opensaola/internal/service/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type runtimeObjectSnapshot struct {
	replicaSets []appsv1.ReplicaSet
	pods        []corev1.Pod
	pvcs        []corev1.PersistentVolumeClaim
	events      []corev1.Event
	err         error
}

func (r *MiddlewareOperatorRuntimeReconciler) collectRuntimeObjects(ctx context.Context, deployment *appsv1.Deployment) runtimeObjectSnapshot {
	var snapshot runtimeObjectSnapshot
	logger := log.FromContext(ctx).WithValues(
		"deploymentNamespace", deployment.Namespace,
		"deploymentName", deployment.Name,
	)

	var replicaSets appsv1.ReplicaSetList
	if err := r.List(ctx, &replicaSets, client.InNamespace(deployment.Namespace)); err != nil {
		logger.Error(err, "list ReplicaSets for runtime diagnostics failed")
		snapshot.err = errors.Join(snapshot.err, fmt.Errorf("list ReplicaSets for runtime diagnostics: %w", err))
	} else {
		snapshot.replicaSets = replicaSets.Items
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(deployment.Namespace)); err != nil {
		logger.Error(err, "list Pods for runtime diagnostics failed")
		snapshot.err = errors.Join(snapshot.err, fmt.Errorf("list Pods for runtime diagnostics: %w", err))
	} else {
		snapshot.pods = pods.Items
	}

	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs, client.InNamespace(deployment.Namespace)); err != nil {
		logger.Error(err, "list PVCs for runtime diagnostics failed")
		snapshot.err = errors.Join(snapshot.err, fmt.Errorf("list PVCs for runtime diagnostics: %w", err))
	} else {
		snapshot.pvcs = pvcs.Items
	}

	var events corev1.EventList
	if err := r.List(ctx, &events, client.InNamespace(deployment.Namespace)); err != nil {
		logger.Error(err, "list Events for runtime diagnostics failed")
		snapshot.err = errors.Join(snapshot.err, fmt.Errorf("list Events for runtime diagnostics: %w", err))
	} else {
		snapshot.events = events.Items
	}

	return snapshot
}

func deriveRuntimeDiagnostic(
	moName string,
	deployment *appsv1.Deployment,
	replicaSets []appsv1.ReplicaSet,
	pods []corev1.Pod,
	pvcs []corev1.PersistentVolumeClaim,
	events []corev1.Event,
	collectionErrors ...error,
) string {
	relatedReplicaSets := filterRuntimeReplicaSets(deployment, replicaSets)
	relatedPods := filterRuntimePods(deployment, relatedReplicaSets, pods)
	relatedPVCs := filterRuntimePVCs(deployment, relatedPods, pvcs)
	relatedEvents := filterRuntimeEvents(deployment, relatedReplicaSets, relatedPods, relatedPVCs, events)
	collectionErr := errors.Join(collectionErrors...)

	if deploymentRuntimeReady(deployment) {
		return "Ready"
	}
	if diagnostic := podWaitingDiagnostic(moName, deployment, relatedPods, relatedEvents); diagnostic != "" {
		return diagnostic
	}
	if diagnostic := pvcPendingDiagnostic(moName, deployment, relatedPVCs, relatedEvents); diagnostic != "" {
		return diagnostic
	}
	if deployment.Status.ObservedGeneration > 0 && deployment.Generation > 0 && deployment.Status.ObservedGeneration < deployment.Generation {
		return runtimeDiagnostic(moName, deployment, status.Diagnostic{
			Phase:              status.PhaseRuntimeReconcile,
			FailedObject:       deploymentObjectRef(deployment),
			Owner:              deploymentObjectRef(deployment),
			Generation:         deployment.Generation,
			ObservedGeneration: deployment.Status.ObservedGeneration,
			Cause:              fmt.Errorf("deployment status is stale"),
			Next:               fmt.Sprintf("kubectl rollout status deployment/%s -n %s; kubectl describe deployment/%s -n %s", deployment.Name, deployment.Namespace, deployment.Name, deployment.Namespace),
		})
	}
	if collectionErr != nil {
		return runtimeDiagnostic(moName, deployment, status.Diagnostic{
			Phase:              status.PhaseRuntimeReconcile,
			FailedObject:       deploymentObjectRef(deployment),
			Owner:              deploymentObjectRef(deployment),
			FieldPath:          "runtimeObjectSnapshot",
			Expected:           "list ReplicaSets, Pods, PVCs and Events",
			Actual:             "list failed",
			Generation:         deployment.Generation,
			ObservedGeneration: deployment.Status.ObservedGeneration,
			Cause:              collectionErr,
			Next:               fmt.Sprintf("check operator RBAC for apps/replicasets and core pods,persistentvolumeclaims,events; kubectl auth can-i list pods -n %s --as=<operator-service-account>", deployment.Namespace),
		})
	}
	if diagnostic := warningEventDiagnostic(moName, deployment, relatedEvents); diagnostic != "" {
		return diagnostic
	}
	if diagnostic := deploymentNotReadyDiagnostic(moName, deployment); diagnostic != "" {
		return diagnostic
	}

	return deriveRuntimePhase(&deployment.Status)
}

func deploymentNotReadyDiagnostic(moName string, deployment *appsv1.Deployment) string {
	if deploymentRuntimeReady(deployment) {
		return ""
	}
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	if desired == 0 && deployment.Status.Replicas == 0 {
		return ""
	}

	condition := deploymentFailureCondition(deployment.Status.Conditions)
	causeParts := []string{
		fmt.Sprintf("replicas=%d", deployment.Status.Replicas),
		fmt.Sprintf("updatedReplicas=%d", deployment.Status.UpdatedReplicas),
		fmt.Sprintf("readyReplicas=%d", deployment.Status.ReadyReplicas),
		fmt.Sprintf("availableReplicas=%d", deployment.Status.AvailableReplicas),
		fmt.Sprintf("unavailableReplicas=%d", deployment.Status.UnavailableReplicas),
	}
	actual := runtimeCause(causeParts)
	if condition != nil {
		causeParts = append(causeParts,
			fmt.Sprintf("deploymentCondition=%s", condition.Type),
			fmt.Sprintf("conditionStatus=%s", condition.Status),
			diagnosticValue("reason", condition.Reason),
			diagnosticValue("message", condition.Message),
		)
		if condition.Reason != "" {
			actual = condition.Reason
		}
	}

	return runtimeDiagnostic(moName, deployment, status.Diagnostic{
		Phase:              status.PhaseWorkloadReadiness,
		FailedObject:       deploymentObjectRef(deployment),
		Owner:              deploymentObjectRef(deployment),
		FieldPath:          "status",
		Expected:           "observedGeneration>=generation, updatedReplicas==replicas, readyReplicas==replicas, availableReplicas==replicas, unavailableReplicas=0",
		Actual:             actual,
		Generation:         deployment.Generation,
		ObservedGeneration: deployment.Status.ObservedGeneration,
		Next:               fmt.Sprintf("kubectl describe deployment %s -n %s; kubectl rollout status deployment/%s -n %s", deployment.Name, deployment.Namespace, deployment.Name, deployment.Namespace),
		Cause:              fmt.Errorf("%s", runtimeCause(causeParts)),
	})
}

func deploymentFailureCondition(conditions []appsv1.DeploymentCondition) *appsv1.DeploymentCondition {
	for i := range conditions {
		if conditions[i].Type == appsv1.DeploymentReplicaFailure && conditions[i].Status == corev1.ConditionTrue {
			return &conditions[i]
		}
	}
	for i := range conditions {
		if conditions[i].Type == appsv1.DeploymentProgressing && conditions[i].Status == corev1.ConditionFalse {
			return &conditions[i]
		}
	}
	for i := range conditions {
		if conditions[i].Type == appsv1.DeploymentAvailable && conditions[i].Status == corev1.ConditionFalse {
			return &conditions[i]
		}
	}
	return nil
}

func podWaitingDiagnostic(moName string, deployment *appsv1.Deployment, pods []corev1.Pod, events []corev1.Event) string {
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].Name < pods[j].Name
	})
	for _, pod := range pods {
		if waiting := firstWaitingContainer(pod); waiting != nil {
			event := latestEventForObject(events, "Pod", pod.Name)
			causeParts := []string{
				fmt.Sprintf("container=%s", waiting.container),
				fmt.Sprintf("image=%s", waiting.image),
				diagnosticValue("nodeName", pod.Spec.NodeName),
				diagnosticValue("serviceAccountName", pod.Spec.ServiceAccountName),
				fmt.Sprintf("reason=%s", waiting.reason),
				fmt.Sprintf("message=%s", waiting.message),
			}
			causeParts = append(causeParts, eventDetails(event)...)
			cause := runtimeCause(causeParts)
			return runtimeDiagnostic(moName, deployment, status.Diagnostic{
				Phase:              status.PhaseWorkloadReadiness,
				FailedObject:       podObjectRef(&pod),
				Owner:              deploymentObjectRef(deployment),
				FieldPath:          waiting.fieldPath,
				Expected:           "container ready",
				Actual:             waiting.reason,
				Generation:         deployment.Generation,
				ObservedGeneration: deployment.Status.ObservedGeneration,
				Next:               fmt.Sprintf("kubectl describe pod %s -n %s; kubectl get events -n %s --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", pod.Name, pod.Namespace, pod.Namespace, pod.Name),
				Cause:              fmt.Errorf("%s", cause),
			})
		}
		if diagnostic := podConditionDiagnostic(moName, deployment, &pod, events); diagnostic != "" {
			return diagnostic
		}
	}
	return ""
}

func podConditionDiagnostic(moName string, deployment *appsv1.Deployment, pod *corev1.Pod, events []corev1.Event) string {
	for _, condition := range pod.Status.Conditions {
		if condition.Status != corev1.ConditionFalse && condition.Status != corev1.ConditionUnknown {
			continue
		}
		if condition.Type != corev1.PodScheduled && condition.Type != corev1.ContainersReady && condition.Type != corev1.PodReady {
			continue
		}
		event := latestEventForObject(events, "Pod", pod.Name)
		causeParts := []string{
			fmt.Sprintf("podCondition=%s", condition.Type),
			diagnosticValue("nodeName", pod.Spec.NodeName),
			diagnosticValue("serviceAccountName", pod.Spec.ServiceAccountName),
			fmt.Sprintf("reason=%s", condition.Reason),
			fmt.Sprintf("message=%s", condition.Message),
		}
		causeParts = append(causeParts, eventDetails(event)...)
		cause := runtimeCause(causeParts)
		return runtimeDiagnostic(moName, deployment, status.Diagnostic{
			Phase:              status.PhaseWorkloadReadiness,
			FailedObject:       podObjectRef(pod),
			Owner:              deploymentObjectRef(deployment),
			FieldPath:          fmt.Sprintf("status.conditions[type=%s]", condition.Type),
			Expected:           "pod condition True",
			Actual:             string(condition.Status),
			Generation:         deployment.Generation,
			ObservedGeneration: deployment.Status.ObservedGeneration,
			Next:               fmt.Sprintf("kubectl describe pod %s -n %s; kubectl get events -n %s --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", pod.Name, pod.Namespace, pod.Namespace, pod.Name),
			Cause:              fmt.Errorf("%s", cause),
		})
	}
	return ""
}

func pvcPendingDiagnostic(moName string, deployment *appsv1.Deployment, pvcs []corev1.PersistentVolumeClaim, events []corev1.Event) string {
	sort.Slice(pvcs, func(i, j int) bool {
		return pvcs[i].Name < pvcs[j].Name
	})
	for _, pvc := range pvcs {
		if pvc.Status.Phase != corev1.ClaimPending {
			continue
		}
		event := latestEventForObject(events, "PersistentVolumeClaim", pvc.Name)
		causeParts := []string{
			"pvc pending",
			fmt.Sprintf("phase=%s", pvc.Status.Phase),
			diagnosticValue("storageClassName", pvcStorageClassName(pvc)),
			diagnosticValue("accessModes", pvcAccessModes(pvc)),
			diagnosticValue("requestedStorage", pvcRequestedStorage(pvc)),
		}
		causeParts = append(causeParts, eventDetails(event)...)
		cause := runtimeCause(causeParts)
		return runtimeDiagnostic(moName, deployment, status.Diagnostic{
			Phase:              status.PhaseWorkloadReadiness,
			FailedObject:       pvcObjectRef(&pvc),
			Owner:              deploymentObjectRef(deployment),
			FieldPath:          "status.phase",
			Expected:           string(corev1.ClaimBound),
			Actual:             string(pvc.Status.Phase),
			Generation:         deployment.Generation,
			ObservedGeneration: deployment.Status.ObservedGeneration,
			Next:               fmt.Sprintf("kubectl describe pvc %s -n %s; kubectl get events -n %s --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", pvc.Name, pvc.Namespace, pvc.Namespace, pvc.Name),
			Cause:              fmt.Errorf("%s", cause),
		})
	}
	return ""
}

func warningEventDiagnostic(moName string, deployment *appsv1.Deployment, events []corev1.Event) string {
	event := latestInterestingEvent(events)
	if event == nil {
		return ""
	}
	return runtimeDiagnostic(moName, deployment, status.Diagnostic{
		Phase:              status.PhaseWorkloadReadiness,
		FailedObject:       eventObjectRef(event),
		Owner:              deploymentObjectRef(deployment),
		FieldPath:          "events",
		Expected:           "no Warning events",
		Actual:             event.Reason,
		Generation:         deployment.Generation,
		ObservedGeneration: deployment.Status.ObservedGeneration,
		Next:               fmt.Sprintf("kubectl describe %s %s -n %s; kubectl get events -n %s --field-selector involvedObject.name=%s --sort-by=.lastTimestamp", strings.ToLower(event.InvolvedObject.Kind), event.InvolvedObject.Name, event.Namespace, event.Namespace, event.InvolvedObject.Name),
		Cause:              fmt.Errorf("%s", runtimeCause(eventDetails(event))),
	})
}

func deploymentRuntimeReady(deployment *appsv1.Deployment) bool {
	status := deployment.Status
	if deployment.Generation > 0 && status.ObservedGeneration > 0 && status.ObservedGeneration < deployment.Generation {
		return false
	}
	return status.Replicas > 0 &&
		status.ReadyReplicas == status.Replicas &&
		status.AvailableReplicas == status.Replicas &&
		status.UpdatedReplicas == status.Replicas &&
		status.UnavailableReplicas == 0
}

func runtimeDiagnostic(moName string, deployment *appsv1.Deployment, diagnostic status.Diagnostic) string {
	diagnostic.Controller = "middlewareoperator-runtime"
	diagnostic.Resource = status.ObjectRef{
		APIVersion: "middleware.cn/v1",
		Kind:       "MiddlewareOperator",
		Namespace:  deployment.Namespace,
		Name:       moName,
	}
	return diagnostic.Message()
}

type waitingContainer struct {
	container string
	image     string
	reason    string
	message   string
	fieldPath string
}

func firstWaitingContainer(pod corev1.Pod) *waitingContainer {
	for _, container := range pod.Status.InitContainerStatuses {
		if container.State.Waiting == nil {
			continue
		}
		return &waitingContainer{
			container: container.Name,
			image:     container.Image,
			reason:    container.State.Waiting.Reason,
			message:   container.State.Waiting.Message,
			fieldPath: fmt.Sprintf("status.initContainerStatuses[name=%s].state.waiting", container.Name),
		}
	}
	for _, container := range pod.Status.ContainerStatuses {
		if container.State.Waiting == nil {
			continue
		}
		return &waitingContainer{
			container: container.Name,
			image:     container.Image,
			reason:    container.State.Waiting.Reason,
			message:   container.State.Waiting.Message,
			fieldPath: fmt.Sprintf("status.containerStatuses[name=%s].state.waiting", container.Name),
		}
	}
	return nil
}

func filterRuntimeReplicaSets(deployment *appsv1.Deployment, replicaSets []appsv1.ReplicaSet) []appsv1.ReplicaSet {
	filtered := make([]appsv1.ReplicaSet, 0, len(replicaSets))
	for _, replicaSet := range replicaSets {
		if objectOwnedBy(replicaSet.OwnerReferences, "apps/v1", "Deployment", deployment.Name) {
			filtered = append(filtered, replicaSet)
		}
	}
	return filtered
}

func filterRuntimePods(deployment *appsv1.Deployment, replicaSets []appsv1.ReplicaSet, pods []corev1.Pod) []corev1.Pod {
	replicaSetNames := make(map[string]struct{}, len(replicaSets))
	for _, replicaSet := range replicaSets {
		replicaSetNames[replicaSet.Name] = struct{}{}
	}

	filtered := make([]corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		if pod.Namespace != deployment.Namespace {
			continue
		}
		if objectOwnedBy(pod.OwnerReferences, "apps/v1", "Deployment", deployment.Name) || podMatchesDeploymentSelector(deployment, pod) {
			filtered = append(filtered, pod)
			continue
		}
		for _, ownerReference := range pod.OwnerReferences {
			if ownerReference.Kind != "ReplicaSet" {
				continue
			}
			if _, ok := replicaSetNames[ownerReference.Name]; ok {
				filtered = append(filtered, pod)
				break
			}
		}
	}
	return filtered
}

func filterRuntimePVCs(deployment *appsv1.Deployment, pods []corev1.Pod, pvcs []corev1.PersistentVolumeClaim) []corev1.PersistentVolumeClaim {
	claimNames := make(map[string]struct{})
	for _, pod := range pods {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil || volume.PersistentVolumeClaim.ClaimName == "" {
				continue
			}
			claimNames[fmt.Sprintf("%s/%s", pod.Namespace, volume.PersistentVolumeClaim.ClaimName)] = struct{}{}
		}
	}

	filtered := make([]corev1.PersistentVolumeClaim, 0, len(pvcs))
	for _, pvc := range pvcs {
		if pvc.Namespace != deployment.Namespace {
			continue
		}
		if _, ok := claimNames[fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)]; ok {
			filtered = append(filtered, pvc)
			continue
		}
		if objectOwnedBy(pvc.OwnerReferences, "apps/v1", "Deployment", deployment.Name) || labelsMatchDeploymentSelector(deployment, pvc.Labels) {
			filtered = append(filtered, pvc)
		}
	}
	return filtered
}

func filterRuntimeEvents(
	deployment *appsv1.Deployment,
	replicaSets []appsv1.ReplicaSet,
	pods []corev1.Pod,
	pvcs []corev1.PersistentVolumeClaim,
	events []corev1.Event,
) []corev1.Event {
	related := map[string]struct{}{
		eventKey("Deployment", deployment.Name): {},
	}
	for _, replicaSet := range replicaSets {
		related[eventKey("ReplicaSet", replicaSet.Name)] = struct{}{}
	}
	for _, pod := range pods {
		related[eventKey("Pod", pod.Name)] = struct{}{}
	}
	for _, pvc := range pvcs {
		related[eventKey("PersistentVolumeClaim", pvc.Name)] = struct{}{}
	}

	filtered := make([]corev1.Event, 0, len(events))
	for _, event := range events {
		if event.Namespace != deployment.Namespace {
			continue
		}
		if _, ok := related[eventKey(event.InvolvedObject.Kind, event.InvolvedObject.Name)]; !ok {
			continue
		}
		if !isInterestingEvent(event) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func latestEventForObject(events []corev1.Event, kind, name string) *corev1.Event {
	var latest *corev1.Event
	for i := range events {
		event := &events[i]
		if event.InvolvedObject.Kind != kind || event.InvolvedObject.Name != name {
			continue
		}
		if latest == nil || eventTimestamp(event).After(eventTimestamp(latest)) {
			latest = event
		}
	}
	return latest
}

func latestInterestingEvent(events []corev1.Event) *corev1.Event {
	var latest *corev1.Event
	for i := range events {
		event := &events[i]
		if !isInterestingEvent(*event) {
			continue
		}
		if latest == nil || eventTimestamp(event).After(eventTimestamp(latest)) {
			latest = event
		}
	}
	return latest
}

func isInterestingEvent(event corev1.Event) bool {
	if event.Type == corev1.EventTypeWarning {
		return true
	}
	switch event.Reason {
	case "BackOff", "ErrImagePull", "Failed", "FailedAttachVolume", "FailedBinding", "FailedCreate", "FailedMount", "FailedPull", "FailedScheduling", "ImagePullBackOff", "Unhealthy":
		return true
	default:
		return false
	}
}

func eventTimestamp(event *corev1.Event) time.Time {
	switch {
	case !event.EventTime.IsZero():
		return event.EventTime.Time
	case !event.LastTimestamp.IsZero():
		return event.LastTimestamp.Time
	case !event.FirstTimestamp.IsZero():
		return event.FirstTimestamp.Time
	default:
		return event.CreationTimestamp.Time
	}
}

func eventDetails(event *corev1.Event) []string {
	if event == nil {
		return nil
	}
	return []string{
		diagnosticValue("eventType", event.Type),
		diagnosticValue("eventReason", event.Reason),
		diagnosticValue("eventMessage", event.Message),
		diagnosticValue("eventCount", fmt.Sprintf("%d", event.Count)),
		diagnosticValue("eventFirstTimestamp", formatEventTime(event.FirstTimestamp.Time)),
		diagnosticValue("eventLastTimestamp", formatEventTime(eventTimestamp(event))),
		diagnosticValue("eventSource", event.Source.Component),
		diagnosticValue("reportingController", event.ReportingController),
	}
}

func formatEventTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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

func objectOwnedBy(ownerReferences []metav1.OwnerReference, apiVersion, kind, name string) bool {
	for _, ownerReference := range ownerReferences {
		if ownerReference.Kind != kind || ownerReference.Name != name {
			continue
		}
		if ownerReference.APIVersion == "" || ownerReference.APIVersion == apiVersion {
			return true
		}
	}
	return false
}

func podMatchesDeploymentSelector(deployment *appsv1.Deployment, pod corev1.Pod) bool {
	return labelsMatchDeploymentSelector(deployment, pod.Labels)
}

func labelsMatchDeploymentSelector(deployment *appsv1.Deployment, objectLabels map[string]string) bool {
	if deployment.Spec.Selector == nil {
		return false
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil || selector.Empty() {
		return false
	}
	return selector.Matches(labels.Set(objectLabels))
}

func eventKey(kind, name string) string {
	return kind + "/" + name
}

func deploymentObjectRef(deployment *appsv1.Deployment) status.ObjectRef {
	return status.ObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: deployment.Namespace, Name: deployment.Name}
}

func podObjectRef(pod *corev1.Pod) status.ObjectRef {
	return status.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
}

func pvcObjectRef(pvc *corev1.PersistentVolumeClaim) status.ObjectRef {
	return status.ObjectRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: pvc.Namespace, Name: pvc.Name}
}

func eventObjectRef(event *corev1.Event) status.ObjectRef {
	return status.ObjectRef{APIVersion: event.InvolvedObject.APIVersion, Kind: event.InvolvedObject.Kind, Namespace: event.Namespace, Name: event.InvolvedObject.Name}
}

func (r *MiddlewareOperatorRuntimeReconciler) runtimePodToDeploymentRequests(ctx context.Context, object client.Object) []reconcile.Request {
	pod, ok := object.(*corev1.Pod)
	if !ok || pod == nil {
		return nil
	}
	return r.podToDeploymentRequests(ctx, pod)
}

func (r *MiddlewareOperatorRuntimeReconciler) runtimePVCToDeploymentRequests(ctx context.Context, object client.Object) []reconcile.Request {
	pvc, ok := object.(*corev1.PersistentVolumeClaim)
	if !ok || pvc == nil {
		return nil
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(pvc.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "list Pods for PVC runtime event failed", "pvcNamespace", pvc.Namespace, "pvcName", pvc.Name)
		return r.middlewareOperatorDeploymentRequestsForNamespace(ctx, pvc.Namespace)
	}

	requestsByName := make(map[types.NamespacedName]reconcile.Request)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !podUsesPVC(pod, pvc.Name) {
			continue
		}
		for _, request := range r.podToDeploymentRequests(ctx, pod) {
			requestsByName[request.NamespacedName] = request
		}
	}
	if len(requestsByName) == 0 {
		return r.middlewareOperatorDeploymentRequestsForNamespace(ctx, pvc.Namespace)
	}
	return requestsFromMap(requestsByName)
}

func (r *MiddlewareOperatorRuntimeReconciler) runtimeEventToDeploymentRequests(ctx context.Context, object client.Object) []reconcile.Request {
	event, ok := object.(*corev1.Event)
	if !ok || event == nil || !isInterestingEvent(*event) {
		return nil
	}

	switch event.InvolvedObject.Kind {
	case "Deployment":
		return r.deploymentNameToRuntimeRequest(ctx, event.Namespace, event.InvolvedObject.Name)
	case "ReplicaSet":
		return r.replicaSetNameToDeploymentRequests(ctx, event.Namespace, event.InvolvedObject.Name)
	case "Pod":
		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{Namespace: event.Namespace, Name: event.InvolvedObject.Name}, &pod); err != nil {
			log.FromContext(ctx).Error(err, "get Pod for runtime Event failed", "podNamespace", event.Namespace, "podName", event.InvolvedObject.Name)
			return r.middlewareOperatorDeploymentRequestsForNamespace(ctx, event.Namespace)
		}
		return r.podToDeploymentRequests(ctx, &pod)
	case "PersistentVolumeClaim":
		pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: event.Namespace, Name: event.InvolvedObject.Name}}
		return r.runtimePVCToDeploymentRequests(ctx, pvc)
	default:
		return nil
	}
}

func (r *MiddlewareOperatorRuntimeReconciler) podToDeploymentRequests(ctx context.Context, pod *corev1.Pod) []reconcile.Request {
	for _, ownerReference := range pod.OwnerReferences {
		switch ownerReference.Kind {
		case "Deployment":
			return r.deploymentNameToRuntimeRequest(ctx, pod.Namespace, ownerReference.Name)
		case "ReplicaSet":
			return r.replicaSetNameToDeploymentRequests(ctx, pod.Namespace, ownerReference.Name)
		}
	}

	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(pod.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "list Deployments for Pod runtime event failed", "podNamespace", pod.Namespace, "podName", pod.Name)
		return nil
	}
	requests := make(map[types.NamespacedName]reconcile.Request)
	for i := range deployments.Items {
		deployment := &deployments.Items[i]
		if !hasMiddlewareOperatorOwner(deployment) || !podMatchesDeploymentSelector(deployment, *pod) {
			continue
		}
		key := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
		requests[key] = reconcile.Request{NamespacedName: key}
	}
	return requestsFromMap(requests)
}

func (r *MiddlewareOperatorRuntimeReconciler) replicaSetNameToDeploymentRequests(ctx context.Context, namespace, name string) []reconcile.Request {
	var replicaSet appsv1.ReplicaSet
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &replicaSet); err != nil {
		log.FromContext(ctx).Error(err, "get ReplicaSet for runtime event failed", "replicaSetNamespace", namespace, "replicaSetName", name)
		return r.middlewareOperatorDeploymentRequestsForNamespace(ctx, namespace)
	}
	for _, ownerReference := range replicaSet.OwnerReferences {
		if ownerReference.Kind == "Deployment" {
			return r.deploymentNameToRuntimeRequest(ctx, namespace, ownerReference.Name)
		}
	}
	return nil
}

func (r *MiddlewareOperatorRuntimeReconciler) deploymentNameToRuntimeRequest(ctx context.Context, namespace, name string) []reconcile.Request {
	var deployment appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &deployment); err != nil {
		log.FromContext(ctx).Error(err, "get Deployment for runtime event failed", "deploymentNamespace", namespace, "deploymentName", name)
		return nil
	}
	if !hasMiddlewareOperatorOwner(&deployment) {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}}
}

func (r *MiddlewareOperatorRuntimeReconciler) middlewareOperatorDeploymentRequestsForNamespace(ctx context.Context, namespace string) []reconcile.Request {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(namespace)); err != nil {
		log.FromContext(ctx).Error(err, "list MiddlewareOperator Deployments for runtime event failed", "namespace", namespace)
		return nil
	}
	requests := make(map[types.NamespacedName]reconcile.Request)
	for i := range deployments.Items {
		deployment := &deployments.Items[i]
		if !hasMiddlewareOperatorOwner(deployment) {
			continue
		}
		key := types.NamespacedName{Namespace: deployment.Namespace, Name: deployment.Name}
		requests[key] = reconcile.Request{NamespacedName: key}
	}
	return requestsFromMap(requests)
}

func podUsesPVC(pod *corev1.Pod, claimName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claimName {
			return true
		}
	}
	return false
}

func requestsFromMap(requests map[types.NamespacedName]reconcile.Request) []reconcile.Request {
	result := make([]reconcile.Request, 0, len(requests))
	for _, request := range requests {
		result = append(result, request)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].NamespacedName.String() < result[j].NamespacedName.String()
	})
	return result
}

func runtimeEventChanged(oldEvent, newEvent *corev1.Event) bool {
	if oldEvent == nil || newEvent == nil {
		return true
	}
	return oldEvent.Type != newEvent.Type ||
		oldEvent.Reason != newEvent.Reason ||
		oldEvent.Message != newEvent.Message ||
		oldEvent.Count != newEvent.Count ||
		!eventTimestamp(oldEvent).Equal(eventTimestamp(newEvent))
}

func runtimePodChanged(oldPod, newPod *corev1.Pod) bool {
	if oldPod == nil || newPod == nil {
		return true
	}
	return !reflect.DeepEqual(oldPod.Status, newPod.Status) ||
		!reflect.DeepEqual(oldPod.Spec.Volumes, newPod.Spec.Volumes) ||
		!reflect.DeepEqual(oldPod.OwnerReferences, newPod.OwnerReferences) ||
		!reflect.DeepEqual(oldPod.Labels, newPod.Labels)
}
