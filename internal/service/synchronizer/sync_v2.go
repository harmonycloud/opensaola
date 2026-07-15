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
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/mohae/deepcopy"
	"k8s.io/apimachinery/pkg/api/equality"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/tidwall/gjson"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func hasOwnerUID(ownerReferences []metav1.OwnerReference, ownerUID types.UID) bool {
	if ownerUID == "" {
		return false
	}
	for _, ownerReference := range ownerReferences {
		if ownerReference.UID == ownerUID {
			return true
		}
	}
	return false
}

type ownerCandidate struct {
	APIVersion string
	Kind       string
	Name       string
	UID        types.UID
}

func ownerReferencesMatch(ownerReferences []metav1.OwnerReference, candidates ...ownerCandidate) (matched, conflicted bool) {
	for _, candidate := range candidates {
		if hasOwnerUID(ownerReferences, candidate.UID) {
			return true, false
		}
	}
	for _, ownerReference := range ownerReferences {
		for _, candidate := range candidates {
			if candidate.UID == "" || ownerReference.UID == "" || ownerReference.UID == candidate.UID {
				continue
			}
			if ownerReference.APIVersion == candidate.APIVersion && ownerReference.Kind == candidate.Kind && ownerReference.Name == candidate.Name {
				return false, true
			}
		}
	}
	return false, false
}

func collectOwnedReplicaSets(replicaSetList []appsv1.ReplicaSet, candidates []ownerCandidate) map[string]appsv1.ReplicaSet {
	replicaSets := make(map[string]appsv1.ReplicaSet)
	for _, replicaSet := range replicaSetList {
		if matched, _ := ownerReferencesMatch(replicaSet.OwnerReferences, candidates...); matched {
			replicaSets[replicaSet.Name] = replicaSet
		}
	}
	return replicaSets
}

func collectOwnedPods(podList []corev1.Pod, candidates []ownerCandidate, middlewareName string) map[string]corev1.Pod {
	pods := make(map[string]corev1.Pod)
	for _, pod := range podList {
		matched, conflicted := ownerReferencesMatch(pod.OwnerReferences, candidates...)
		if matched {
			pods[pod.Name] = pod
			continue
		}
		if conflicted {
			continue
		}
		for _, key := range GeneralLabelKeys {
			if pod.GetLabels()[key] == middlewareName {
				pods[pod.Name] = pod
				break
			}
		}
	}
	return pods
}

func pvcBelongsToStatefulSet(pvc corev1.PersistentVolumeClaim, sts appsv1.StatefulSet) bool {
	if !pvcNameMatchesStatefulSetClaimTemplate(pvc.Name, sts) {
		return false
	}
	pvcLabels := pvc.GetLabels()
	if len(pvcLabels) == 0 {
		return false
	}
	for _, labelSet := range statefulSetPVCLabelSets(sts) {
		if labelsContainAll(pvcLabels, labelSet) {
			return true
		}
	}
	return false
}

func pvcNameMatchesStatefulSetClaimTemplate(pvcName string, sts appsv1.StatefulSet) bool {
	if pvcName == "" || sts.Name == "" {
		return false
	}
	for _, template := range sts.Spec.VolumeClaimTemplates {
		if template.Name == "" {
			continue
		}
		prefix := fmt.Sprintf("%s-%s-", template.Name, sts.Name)
		ordinal := strings.TrimPrefix(pvcName, prefix)
		if ordinal != pvcName && isStatefulSetPodOrdinal(ordinal) {
			return true
		}
	}
	return false
}

func isStatefulSetPodOrdinal(value string) bool {
	if value == "" {
		return false
	}
	ordinal, err := strconv.Atoi(value)
	return err == nil && ordinal >= 0
}

func statefulSetPVCLabelSets(sts appsv1.StatefulSet) []map[string]string {
	var labelSets []map[string]string
	// StatefulSet-created PVCs inherit selector matchLabels from the StatefulSet controller.
	if sts.Spec.Selector != nil && len(sts.Spec.Selector.MatchLabels) > 0 {
		labelSets = append(labelSets, maps.Clone(sts.Spec.Selector.MatchLabels))
	}
	if len(sts.Spec.Template.Labels) > 0 {
		labelSets = append(labelSets, maps.Clone(sts.Spec.Template.Labels))
	}
	return labelSets
}

func labelsContainAll(labels map[string]string, expected map[string]string) bool {
	if len(labels) == 0 || len(expected) == 0 {
		return false
	}
	for key, value := range expected {
		if value == "" || labels[key] != value {
			return false
		}
	}
	return true
}

func customResourcesFromStatus(status []byte) (v1.CustomResources, error) {
	var customResources v1.CustomResources
	if err := json.Unmarshal(status, &customResources); err != nil {
		return v1.CustomResources{}, err
	}
	return customResources, nil
}

func phaseFromGenericStatus(status []byte, fallback v1.Phase) v1.Phase {
	phase := fallback
	for _, key := range []string{"phase", "state"} {
		if value := strings.TrimSpace(gjson.GetBytes(status, key).String()); value != "" {
			phase = v1.Phase(value)
		}
	}
	return phase
}

// recomputeAndUpdateStatus reads the latest Middleware and CR state, rebuilds
// the full include list from the local informer cache, and writes the result back
// to Middleware.status if anything changed.
func recomputeAndUpdateStatus(ctx context.Context, cli client.Client, cr *unstructured.Unstructured, mid *v1.Middleware) {
	var err error

	nowMid, err := k8s.GetMiddleware(ctx, cli, mid.Name, mid.Namespace)
	if err != nil {
		// Middleware has been deleted; nothing to do.
		if apiErrors.IsNotFound(err) {
			log.FromContext(ctx).Info("recomputeAndUpdateStatus: middleware not found, skip", "namespace", mid.Namespace, "name", mid.Name)
			return
		}
		log.FromContext(ctx).Error(err, "recomputeAndUpdateStatus: get middleware error")
		return
	}
	tempCustomResources := deepcopy.Copy(nowMid.Status.CustomResources)
	previousPhase := nowMid.Status.CustomResources.Phase

	nowCr, err := k8s.GetCustomResource(ctx, cli, cr.GetName(), cr.GetNamespace(), cr.GroupVersionKind())
	if err != nil {
		// CR has been deleted; stop.
		if apiErrors.IsNotFound(err) {
			log.FromContext(ctx).Info("recomputeAndUpdateStatus: cr not found, skip",
				"gvk", cr.GroupVersionKind().String(), "namespace", cr.GetNamespace(), "name", cr.GetName())
			return
		}
		log.FromContext(ctx).Error(err, "recomputeAndUpdateStatus: get custom resource error")
		return
	}

	nowCrStatusBytes, err := json.Marshal(nowCr.Object["status"])
	if err != nil {
		log.FromContext(ctx).Error(err, "recomputeAndUpdateStatus: marshal cr status error")
		return
	}

	nowMid.Status.CustomResources, err = customResourcesFromStatus(nowCrStatusBytes)
	if err != nil {
		log.FromContext(ctx).Error(err, "recomputeAndUpdateStatus: unmarshal cr status error")
		return
	}

	// Replicas extraction
	var CRReplicasKeys = []string{"replicas", "size"}
	for _, key := range CRReplicasKeys {
		if gjson.GetBytes(nowCrStatusBytes, key).Exists() {
			nowMid.Status.CustomResources.Replicas = int(gjson.GetBytes(nowCrStatusBytes, key).Int())
		}
	}

	// Reason extraction
	var CRReasonKeys = []string{"reason"}
	for _, key := range CRReasonKeys {
		if gjson.GetBytes(nowCrStatusBytes, key).Exists() {
			nowMid.Status.CustomResources.Reason = gjson.GetBytes(nowCrStatusBytes, key).String()
		}
	}

	var (
		statefulsets = make(map[string]appsv1.StatefulSet)
		deployments  = make(map[string]appsv1.Deployment)
		daemonsets   = make(map[string]appsv1.DaemonSet)
		replicaSets  = make(map[string]appsv1.ReplicaSet)
		pods         = make(map[string]corev1.Pod)
		services     = make(map[string]corev1.Service)
		pvcs         = make(map[string]corev1.PersistentVolumeClaim)
	)
	baseOwnerCandidates := []ownerCandidate{
		{
			APIVersion: nowCr.GetAPIVersion(),
			Kind:       nowCr.GetKind(),
			Name:       nowCr.GetName(),
			UID:        nowCr.GetUID(),
		},
		{
			APIVersion: v1.GroupVersion.String(),
			Kind:       "Middleware",
			Name:       nowMid.Name,
			UID:        nowMid.UID,
		},
	}

	// Derive phase from native workloads using their complete typed object. All
	// other custom resources preserve the existing status.phase/status.state
	// passthrough behavior.
	switch nowCr.GroupVersionKind() {
	case appsv1.SchemeGroupVersion.WithKind("Deployment"):
		dep, depErr := k8s.GetDeployment(ctx, cli, nowCr.GetName(), nowCr.GetNamespace())
		if depErr != nil {
			log.FromContext(ctx).Error(depErr, "recomputeAndUpdateStatus: get deployment error")
			return
		}
		nowMid.Status.CustomResources.Phase = k8s.DeriveDeploymentPhase(dep, previousPhase)
		deployments[dep.Name] = *dep
	case appsv1.SchemeGroupVersion.WithKind("StatefulSet"):
		sts, stsErr := k8s.GetStatefulSet(ctx, cli, nowCr.GetName(), nowCr.GetNamespace())
		if stsErr != nil {
			log.FromContext(ctx).Error(stsErr, "recomputeAndUpdateStatus: get statefulset error")
			return
		}
		nowMid.Status.CustomResources.Phase = k8s.DeriveStatefulSetPhase(sts, previousPhase)
		statefulsets[sts.Name] = *sts
	case appsv1.SchemeGroupVersion.WithKind("DaemonSet"):
		ds, dsErr := k8s.GetDaemonSet(ctx, cli, nowCr.GetName(), nowCr.GetNamespace())
		if dsErr != nil {
			log.FromContext(ctx).Error(dsErr, "recomputeAndUpdateStatus: get daemonset error")
			return
		}
		nowMid.Status.CustomResources.Phase = k8s.DeriveDaemonSetPhase(ds, previousPhase)
		nowMid.Status.CustomResources.Replicas = int(ds.Status.DesiredNumberScheduled)
		daemonsets[ds.Name] = *ds
	default:
		nowMid.Status.CustomResources.Phase = phaseFromGenericStatus(nowCrStatusBytes, previousPhase)
	}

	// Obtain the namespace cache from the informer manager.
	// If the cache is not yet ready, fall back to no-op for this tick.
	cache := GetNsInformerManager().GetCache(nowMid.Namespace)
	if cache == nil {
		log.FromContext(ctx).Info("recomputeAndUpdateStatus: cache not ready, skip", "warning", true, "namespace", nowMid.Namespace)
		return
	}

	// StatefulSets — list from local cache, filter by ownerRef or label.
	//
	// Must full-list then filter locally: some middleware operators do not set
	// middleware.cn/app labels, so pre-filtering by label would miss resources.
	statefulsetList := cache.ListStatefulSets(nowMid.Namespace)
	for _, statefulset := range statefulsetList {
		matched, conflicted := ownerReferencesMatch(statefulset.OwnerReferences, baseOwnerCandidates...)
		if matched {
			statefulsets[statefulset.Name] = statefulset
			continue
		}
		if conflicted {
			continue
		}
		for _, key := range GeneralLabelKeys {
			if statefulset.GetLabels()[key] == nowMid.Name {
				statefulsets[statefulset.Name] = statefulset
				continue
			}
		}
	}
	nowMid.Status.CustomResources.Include.Statefulsets = []v1.IncludeModel{}
	for key := range statefulsets {
		nowMid.Status.CustomResources.Include.Statefulsets = append(nowMid.Status.CustomResources.Include.Statefulsets, v1.IncludeModel{
			Name: key,
		})
	}

	// Deployments — list from local cache, filter by ownerRef or label.
	deploymentList := cache.ListDeployments(nowMid.Namespace)
	for _, deployment := range deploymentList {
		matched, conflicted := ownerReferencesMatch(deployment.OwnerReferences, baseOwnerCandidates...)
		if matched {
			deployments[deployment.Name] = deployment
			continue
		}
		if conflicted {
			continue
		}
		for _, key := range GeneralLabelKeys {
			if deployment.GetLabels()[key] == nowMid.Name {
				deployments[deployment.Name] = deployment
				continue
			}
		}
	}
	nowMid.Status.CustomResources.Include.Deployments = []v1.IncludeModel{}
	for key := range deployments {
		nowMid.Status.CustomResources.Include.Deployments = append(nowMid.Status.CustomResources.Include.Deployments, v1.IncludeModel{
			Name: key,
		})
	}

	// DaemonSets — list from local cache, filter by ownerRef or label.
	daemonsetList := cache.ListDaemonSets(nowMid.Namespace)
	for _, daemonset := range daemonsetList {
		matched, conflicted := ownerReferencesMatch(daemonset.OwnerReferences, baseOwnerCandidates...)
		if matched {
			daemonsets[daemonset.Name] = daemonset
			continue
		}
		if conflicted {
			continue
		}
		for _, key := range GeneralLabelKeys {
			if daemonset.GetLabels()[key] == nowMid.Name {
				daemonsets[daemonset.Name] = daemonset
				continue
			}
		}
	}
	nowMid.Status.CustomResources.Include.Daemonsets = []v1.IncludeModel{}
	for key := range daemonsets {
		nowMid.Status.CustomResources.Include.Daemonsets = append(nowMid.Status.CustomResources.Include.Daemonsets, v1.IncludeModel{
			Name: key,
		})
	}

	// ReplicaSets — list from local cache, filter by ownerRef to known deployments.
	deploymentOwnerCandidates := make([]ownerCandidate, 0, len(deployments))
	for _, deployment := range deployments {
		deploymentOwnerCandidates = append(deploymentOwnerCandidates, ownerCandidate{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "Deployment",
			Name:       deployment.Name,
			UID:        deployment.UID,
		})
	}
	replicaSetList := cache.ListReplicaSets(nowMid.Namespace)
	for name, replicaSet := range collectOwnedReplicaSets(replicaSetList, deploymentOwnerCandidates) {
		replicaSets[name] = replicaSet
	}

	// Pods — list from local cache, filter by ownerRef or label.
	podOwnerCandidates := append([]ownerCandidate{}, baseOwnerCandidates...)
	for _, replicaSet := range replicaSets {
		podOwnerCandidates = append(podOwnerCandidates, ownerCandidate{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "ReplicaSet",
			Name:       replicaSet.Name,
			UID:        replicaSet.UID,
		})
	}
	for _, statefulset := range statefulsets {
		podOwnerCandidates = append(podOwnerCandidates, ownerCandidate{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "StatefulSet",
			Name:       statefulset.Name,
			UID:        statefulset.UID,
		})
	}
	for _, daemonset := range daemonsets {
		podOwnerCandidates = append(podOwnerCandidates, ownerCandidate{
			APIVersion: appsv1.SchemeGroupVersion.String(),
			Kind:       "DaemonSet",
			Name:       daemonset.Name,
			UID:        daemonset.UID,
		})
	}
	podList := cache.ListPods(nowMid.Namespace)
	for name, pod := range collectOwnedPods(podList, podOwnerCandidates, nowMid.Name) {
		pods[name] = pod
	}
	var podsPvcNameList []string
	nowMid.Status.CustomResources.Include.Pods = []v1.IncludeModel{}
	for key, value := range pods {
		nowMid.Status.CustomResources.Include.Pods = append(nowMid.Status.CustomResources.Include.Pods, v1.IncludeModel{
			Name: key,
		})
		for _, volume := range value.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName != "" {
				podsPvcNameList = append(podsPvcNameList, volume.PersistentVolumeClaim.ClaimName)
			}
		}
	}

	// Services — list from local cache, filter by ownerRef or label.
	serviceList := cache.ListServices(nowMid.Namespace)
	for _, service := range serviceList {
		matched, conflicted := ownerReferencesMatch(service.OwnerReferences, baseOwnerCandidates...)
		if matched {
			services[service.Name] = service
			continue
		}
		if conflicted {
			continue
		}
		for _, key := range GeneralLabelKeys {
			if service.GetLabels()[key] == nowMid.Name {
				services[service.Name] = service
				continue
			}
		}
	}
	nowMid.Status.CustomResources.Include.Services = []v1.IncludeModel{}
	for key, value := range services {
		svc := v1.IncludeModel{
			Name: key,
		}
		if value.GetLabels() != nil {
			svc.Source = value.GetLabels()[v1.LabelSource]
			svc.SourceName = value.GetLabels()[v1.LabelSourceName]
		}
		nowMid.Status.CustomResources.Include.Services = append(nowMid.Status.CustomResources.Include.Services, svc)
	}

	// PVCs — list from local cache, filter by ownerRef, label, StatefulSet claim template labels, or pod volume claim name.
	pvcList := cache.ListPVCs(nowMid.Namespace)
	for _, pvc := range pvcList {
		matched, conflicted := ownerReferencesMatch(pvc.OwnerReferences, baseOwnerCandidates...)
		if matched {
			pvcs[pvc.Name] = pvc
			continue
		}
		if conflicted {
			continue
		}
		for _, key := range GeneralLabelKeys {
			if pvc.GetLabels()[key] == nowMid.Name {
				pvcs[pvc.Name] = pvc
				continue
			}
		}
		for _, statefulset := range statefulsets {
			if pvcBelongsToStatefulSet(pvc, statefulset) {
				pvcs[pvc.Name] = pvc
				break
			}
		}
		for _, podPvcName := range podsPvcNameList {
			if pvc.Name == podPvcName {
				pvcs[pvc.Name] = pvc
				break
			}
		}
	}
	nowMid.Status.CustomResources.Include.Pvcs = []v1.IncludeModel{}
	for key := range pvcs {
		nowMid.Status.CustomResources.Include.Pvcs = append(nowMid.Status.CustomResources.Include.Pvcs, v1.IncludeModel{
			Name: key,
		})
	}
	if diagnostic := deriveMiddlewareReadinessDiagnostic(nowMid, nowCr, pods, pvcs); diagnostic != "" {
		nowMid.Status.CustomResources.Phase = v1.PhaseFailed
		nowMid.Status.CustomResources.Reason = diagnostic
	}

	// Disaster sync — unchanged from V1, still reads from API server via k8s helpers.
	nowMid.Status.CustomResources.Disaster = new(v1.Disaster)
	if _, ok := nowMid.GetAnnotations()[v1.AnnotationDisasterSyncer]; ok {
		split := strings.Split(nowMid.GetAnnotations()[v1.AnnotationDisasterSyncer], "/")
		if len(split) == 4 {
			group := split[0]
			version := split[1]
			kind := split[2]
			name := split[3]
			disasterSyncer, dsErr := k8s.GetCustomResource(ctx, cli, name, nowMid.Namespace, schema.GroupVersionKind{
				Group:   group,
				Version: version,
				Kind:    kind,
			})
			if dsErr != nil && !apiErrors.IsNotFound(dsErr) {
				log.FromContext(ctx).Error(dsErr, "recomputeAndUpdateStatus: get disaster syncer error")
				return
			}
			if disasterSyncer != nil {
				disasterSyncerBytes, marshalErr := json.Marshal(disasterSyncer)
				if marshalErr != nil {
					log.FromContext(ctx).Error(marshalErr, "recomputeAndUpdateStatus: marshal disaster syncer error")
					return
				}
				nowMid.Status.CustomResources.Disaster.Gossip = new(v1.Gossip)
				if gjson.GetBytes(disasterSyncerBytes, "spec.config.advertiseAddr").Exists() {
					nowMid.Status.CustomResources.Disaster.Gossip.AdvertiseAddress = gjson.GetBytes(disasterSyncerBytes, "spec.config.advertiseAddr").String()
				}
				if gjson.GetBytes(disasterSyncerBytes, "spec.config.advertisePort").Exists() {
					nowMid.Status.CustomResources.Disaster.Gossip.AdvertisePort = gjson.GetBytes(disasterSyncerBytes, "spec.config.advertisePort").Int()
				}
				if gjson.GetBytes(disasterSyncerBytes, "status.Phase").Exists() {
					nowMid.Status.CustomResources.Disaster.Gossip.Phase = gjson.GetBytes(disasterSyncerBytes, "status.Phase").String()
				}
				if gjson.GetBytes(disasterSyncerBytes, "status.localClusterStatus.clusterRole").Exists() {
					nowMid.Status.CustomResources.Disaster.Gossip.ClusterRole = gjson.GetBytes(disasterSyncerBytes, "status.localClusterStatus.clusterRole").String()
				}
				if gjson.GetBytes(disasterSyncerBytes, "status.localClusterStatus.clusterPhase").Exists() {
					nowMid.Status.CustomResources.Disaster.Gossip.ClusterPhase = gjson.GetBytes(disasterSyncerBytes, "status.localClusterStatus.clusterPhase").String()
				}
				if gjson.GetBytes(disasterSyncerBytes, "spec.DRCluster.role").Exists() {
					nowMid.Status.CustomResources.Disaster.Gossip.Role = gjson.GetBytes(disasterSyncerBytes, "spec.DRCluster.role").String()
				}
				if gjson.GetBytes(disasterSyncerBytes, "status.GossipPhase").Exists() {
					nowMid.Status.CustomResources.Disaster.Gossip.GossipPhase = gjson.GetBytes(disasterSyncerBytes, "status.GossipPhase").String()
				}
			}
		}

		// DataSyncer annotation — unchanged from V1.
		if _, ok := nowMid.GetAnnotations()[v1.AnnotationDataSyncer]; ok {
			split := strings.Split(nowMid.GetAnnotations()[v1.AnnotationDataSyncer], "/")
			if len(split) == 4 {
				group := split[0]
				version := split[1]
				kind := split[2]
				name := split[3]
				dataSyncer, dtErr := k8s.GetCustomResource(ctx, cli, name, nowMid.Namespace, schema.GroupVersionKind{
					Group:   group,
					Version: version,
					Kind:    kind,
				})
				if dtErr != nil && !apiErrors.IsNotFound(dtErr) {
					log.FromContext(ctx).Error(dtErr, "recomputeAndUpdateStatus: get data syncer error")
					return
				}
				if dataSyncer != nil {
					dataSyncerBytes, marshalErr := json.Marshal(dataSyncer)
					if marshalErr != nil {
						log.FromContext(ctx).Error(marshalErr, "recomputeAndUpdateStatus: marshal data syncer error")
						return
					}
					nowMid.Status.CustomResources.Disaster.Data = new(v1.Data)
					var AddressKeys = []string{"spec.config.target.address", "spec.helmvalues.config.target.address"}
					for _, key := range AddressKeys {
						if gjson.GetBytes(dataSyncerBytes, key).Exists() {
							nowMid.Status.CustomResources.Disaster.Data.Address = gjson.GetBytes(dataSyncerBytes, key).String()
						}
					}
					var OppositeAddress = []string{"spec.config.source.address", "spec.helmvalues.config.source.address"}
					for _, key := range OppositeAddress {
						if gjson.GetBytes(dataSyncerBytes, key).Exists() {
							nowMid.Status.CustomResources.Disaster.Data.OppositeAddress = gjson.GetBytes(dataSyncerBytes, key).String()
						}
					}
					var PhaseKeys = []string{"status.phase"}
					for _, key := range PhaseKeys {
						if gjson.GetBytes(dataSyncerBytes, key).Exists() {
							nowMid.Status.CustomResources.Disaster.Data.Phase = gjson.GetBytes(dataSyncerBytes, key).String()
						}
					}
					var OppositeClusterNameKeys = []string{"spec.config.source.clusterName", "spec.helmvalues.config.source.clusterName"}
					for _, key := range OppositeClusterNameKeys {
						if gjson.GetBytes(dataSyncerBytes, key).Exists() {
							nowMid.Status.CustomResources.Disaster.Data.OppositeClusterName = gjson.GetBytes(dataSyncerBytes, key).String()
						}
					}
					var OppositeClusterNamespaceKeys = []string{"spec.config.source.clusterNamespace", "spec.helmvalues.config.source.clusterNamespace"}
					for _, key := range OppositeClusterNamespaceKeys {
						if gjson.GetBytes(dataSyncerBytes, key).Exists() {
							nowMid.Status.CustomResources.Disaster.Data.OppositeClusterNamespace = gjson.GetBytes(dataSyncerBytes, key).String()
						}
					}
				}
			}
		}

		if _, ok := nowMid.GetAnnotations()[v1.AnnotationOppositeClusterId]; ok {
			nowMid.Status.CustomResources.Disaster.Data.OppositeClusterId = nowMid.GetAnnotations()[v1.AnnotationOppositeClusterId]
		}
	}

	// Conditions — enrich pod list with role/type from CR status conditions.
	var PodNameKeys = []string{"name", "podName"}
	for _, key := range ConditionKeys {
		if gjson.GetBytes(nowCrStatusBytes, key).Exists() {
		loop1:
			for _, condition := range gjson.GetBytes(nowCrStatusBytes, key).Array() {
				for _, nameKey := range PodNameKeys {
					for idx, pod := range nowMid.Status.CustomResources.Include.Pods {
						if condition.Get(nameKey).String() == pod.Name {
							nowMid.Status.CustomResources.Include.Pods[idx].Type = condition.Get("type").String()
							continue loop1
						}
					}
					if condition.Get(nameKey).String() != "" {
						nowMid.Status.CustomResources.Include.Pods = append(nowMid.Status.CustomResources.Include.Pods, v1.IncludeModel{
							Name: condition.Get(nameKey).String(),
							Type: condition.Get("type").String(),
						})
					}
				}
			}
		}
	}

	nowMid.Status.CustomResources.CreationTimestamp = nowCr.GetCreationTimestamp()

	// Sort all include lists for deterministic comparison.
	sort.Slice(nowMid.Status.CustomResources.Include.Statefulsets, func(i, j int) bool {
		return nowMid.Status.CustomResources.Include.Statefulsets[i].Name < nowMid.Status.CustomResources.Include.Statefulsets[j].Name
	})
	sort.Slice(nowMid.Status.CustomResources.Include.Deployments, func(i, j int) bool {
		return nowMid.Status.CustomResources.Include.Deployments[i].Name < nowMid.Status.CustomResources.Include.Deployments[j].Name
	})
	sort.Slice(nowMid.Status.CustomResources.Include.Daemonsets, func(i, j int) bool {
		return nowMid.Status.CustomResources.Include.Daemonsets[i].Name < nowMid.Status.CustomResources.Include.Daemonsets[j].Name
	})
	sort.Slice(nowMid.Status.CustomResources.Include.Pods, func(i, j int) bool {
		return nowMid.Status.CustomResources.Include.Pods[i].Name < nowMid.Status.CustomResources.Include.Pods[j].Name
	})
	sort.Slice(nowMid.Status.CustomResources.Include.Services, func(i, j int) bool {
		return nowMid.Status.CustomResources.Include.Services[i].Name < nowMid.Status.CustomResources.Include.Services[j].Name
	})
	sort.Slice(nowMid.Status.CustomResources.Include.Pvcs, func(i, j int) bool {
		return nowMid.Status.CustomResources.Include.Pvcs[i].Name < nowMid.Status.CustomResources.Include.Pvcs[j].Name
	})

	// Skip update if status is unchanged.
	if equality.Semantic.DeepEqual(tempCustomResources, nowMid.Status.CustomResources) {
		return
	}

	// Only patch customResources field, not full status replacement.
	// SyncV2 runs in a separate goroutine — full replacement would overwrite
	// state/conditions written by the controller's sequential defers.
	computedCR := nowMid.Status.CustomResources
	if updateErr := k8s.PatchMiddlewareStatusFields(ctx, cli, nowMid.Name, nowMid.Namespace, func(s *v1.MiddlewareStatus) {
		s.CustomResources = computedCR
	}); updateErr != nil {
		log.FromContext(ctx).Error(updateErr, "recomputeAndUpdateStatus: update middleware status error")
	}
}

// SyncCustomResourceV2 is an event-driven replacement for SyncCustomResource.
// Instead of a fixed ticker, it registers the namespace with NsInformerManager and
// registers a per-Middleware Debouncer that fires recomputeAndUpdateStatus on any
// relevant resource change in the namespace.
func SyncCustomResourceV2(ctx context.Context, cli client.Client, cr *unstructured.Unstructured, mid *v1.Middleware) error {
	if cr == nil {
		return fmt.Errorf("cr is nil")
	}

	ns := mid.Namespace
	midKey := fmt.Sprintf("%s/%s", ns, mid.Name)

	// 1. Ensure NsInformerManager is available.
	mgr := GetNsInformerManager()
	if mgr == nil {
		return fmt.Errorf("NsInformerManager not started")
	}

	// 2. Register namespace informers; blocks until cache is synced or ctx canceled.
	if err := mgr.Register(ctx, ns, midKey); err != nil {
		return fmt.Errorf("register informer for ns %s: %w", ns, err)
	}

	// 3. Register the debouncer; informer events route here via NotifyNamespace.
	triggerFn := func() {
		recomputeAndUpdateStatus(ctx, cli, cr, mid)
	}
	RegisterDebouncer(ns, mid.Name, triggerFn)

	// 4. Fire once immediately so status is current before the first event arrives.
	go triggerFn()

	// 5. Guard against duplicate goroutines using the same stopChan mechanism as V1.
	key := fmt.Sprintf(SyncCustomResourceStopChanMapKey, cr.GroupVersionKind().String(), cr.GetNamespace(), cr.GetName())
	stopChan := make(chan struct{})
	actual, loaded := SyncCustomResourceStopChanMap.LoadOrStore(key, stopChan)
	if loaded {
		// Another goroutine is already running for this CR; discard ours.
		safeClose(stopChan)
		log.FromContext(ctx).Info("SyncCustomResourceV2 already running", "key", key)
		return nil
	}
	stopChan, ok := actual.(chan struct{})
	if !ok {
		return fmt.Errorf("SyncCustomResourceV2 stop channel %s has unexpected type %T", key, actual)
	}

	defer func() {
		SyncCustomResourceStopChanMap.Delete(key)
		UnregisterDebouncer(ns, mid.Name)
		mgr.Unregister(ns, midKey)
	}()

	// 6. Block until context is canceled or stop is signaled externally.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stopChan:
		log.FromContext(ctx).Info("SyncCustomResourceV2 stop", "gvk", cr.GroupVersionKind().String(), "namespace", cr.GetNamespace(), "name", cr.GetName())
		return nil
	}
}
