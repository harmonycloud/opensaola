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
	"sort"
	"strings"

	"github.com/mohae/deepcopy"
	"k8s.io/apimachinery/pkg/api/equality"

	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/k8s"
	"github.com/tidwall/gjson"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

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

	err = json.Unmarshal(nowCrStatusBytes, &nowMid.Status.CustomResources)
	if err != nil {
		log.FromContext(ctx).Error(err, "recomputeAndUpdateStatus: unmarshal cr status error")
		return
	}

	// Phase extraction
	var CRPhaseKeys = []string{"phase", "state"}
	switch cr.GroupVersionKind().Kind {
	case "StatefulSet":
		nowMid.Status.CustomResources.Phase = v1.Phase(k8s.GetStatefulSetsPhase(nowCrStatusBytes))
	case "Deployment":
		nowMid.Status.CustomResources.Phase = v1.Phase(k8s.GetDeploymentPhase(nowCrStatusBytes))
	default:
		for _, key := range CRPhaseKeys {
			if gjson.GetBytes(nowCrStatusBytes, key).Exists() {
				nowMid.Status.CustomResources.Phase = v1.Phase(gjson.GetBytes(nowCrStatusBytes, key).String())
			}
		}
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

	// Get the single workload object that is the CR itself (by kind).
	switch nowCr.GetKind() {
	case "Deployment":
		dep, depErr := k8s.GetDeployment(ctx, cli, nowCr.GetName(), nowCr.GetNamespace())
		if depErr != nil {
			log.FromContext(ctx).Error(depErr, "recomputeAndUpdateStatus: get deployment error")
			return
		}
		deployments[dep.Name] = *dep
	case "StatefulSet":
		sts, stsErr := k8s.GetStatefulSet(ctx, cli, nowCr.GetName(), nowCr.GetNamespace())
		if stsErr != nil {
			log.FromContext(ctx).Error(stsErr, "recomputeAndUpdateStatus: get statefulset error")
			return
		}
		statefulsets[sts.Name] = *sts
	case "DaemonSet":
		ds, dsErr := k8s.GetDaemonSet(ctx, cli, nowCr.GetName(), nowCr.GetNamespace())
		if dsErr != nil {
			log.FromContext(ctx).Error(dsErr, "recomputeAndUpdateStatus: get daemonset error")
			return
		}
		daemonsets[ds.Name] = *ds
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
		for _, ownerReference := range statefulset.OwnerReferences {
			if ownerReference.Kind == nowCr.GetKind() && ownerReference.Name == nowCr.GetName() && ownerReference.APIVersion == nowCr.GetAPIVersion() {
				statefulsets[statefulset.Name] = statefulset
				continue
			}
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
		for _, ownerReference := range deployment.OwnerReferences {
			if ownerReference.Kind == nowCr.GetKind() && ownerReference.Name == nowCr.GetName() && ownerReference.APIVersion == nowCr.GetAPIVersion() {
				deployments[deployment.Name] = deployment
				continue
			}
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
		for _, ownerReference := range daemonset.OwnerReferences {
			if ownerReference.Kind == nowCr.GetKind() && ownerReference.Name == nowCr.GetName() && ownerReference.APIVersion == nowCr.GetAPIVersion() {
				daemonsets[daemonset.Name] = daemonset
				continue
			}
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
	replicaSetList := cache.ListReplicaSets(nowMid.Namespace)
	for _, replicaSet := range replicaSetList {
		for _, ownerReference := range replicaSet.OwnerReferences {
			for _, deployment := range deployments {
				if ownerReference.Kind == deployment.Kind && ownerReference.Name == deployment.GetName() && ownerReference.APIVersion == deployment.APIVersion {
					replicaSets[replicaSet.Name] = replicaSet
					break
				}
			}
		}
	}

	// Pods — list from local cache, filter by ownerRef or label.
	podList := cache.ListPods(nowMid.Namespace)
	for _, pod := range podList {
		for _, ownerReference := range pod.OwnerReferences {
			if ownerReference.Kind == nowCr.GetKind() && ownerReference.Name == nowCr.GetName() && ownerReference.APIVersion == nowCr.GetAPIVersion() {
				pods[pod.Name] = pod
				continue
			}
			for _, replicaSet := range replicaSets {
				if ownerReference.Kind == replicaSet.Kind && ownerReference.Name == replicaSet.GetName() && ownerReference.APIVersion == replicaSet.APIVersion {
					pods[pod.Name] = pod
					break
				}
			}
			for _, statefulset := range statefulsets {
				if ownerReference.Kind == statefulset.Kind && ownerReference.Name == statefulset.GetName() && ownerReference.APIVersion == statefulset.APIVersion {
					pods[pod.Name] = pod
					break
				}
			}
			for _, daemonset := range daemonsets {
				if ownerReference.Kind == daemonset.Kind && ownerReference.Name == daemonset.GetName() && ownerReference.APIVersion == daemonset.APIVersion {
					pods[pod.Name] = pod
					break
				}
			}
		}
		for _, key := range GeneralLabelKeys {
			if pod.GetLabels()[key] == nowMid.Name {
				pods[pod.Name] = pod
				continue
			}
		}
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
		for _, ownerReference := range service.OwnerReferences {
			if ownerReference.Kind == nowCr.GetKind() && ownerReference.Name == nowCr.GetName() && ownerReference.APIVersion == nowCr.GetAPIVersion() {
				services[service.Name] = service
				continue
			}
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

	// PVCs — list from local cache, filter by ownerRef, label, or pod volume claim name.
	pvcList := cache.ListPVCs(nowMid.Namespace)
	for _, pvc := range pvcList {
		for _, ownerReference := range pvc.OwnerReferences {
			if ownerReference.Kind == nowCr.GetKind() && ownerReference.Name == nowCr.GetName() && ownerReference.APIVersion == nowCr.GetAPIVersion() {
				pvcs[pvc.Name] = pvc
				continue
			}
		}
		for _, key := range GeneralLabelKeys {
			if pvc.GetLabels()[key] == nowMid.Name {
				pvcs[pvc.Name] = pvc
				continue
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

	// 2. Register namespace informers; blocks until cache is synced or ctx cancelled.
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
	stopChan = actual.(chan struct{})

	defer func() {
		SyncCustomResourceStopChanMap.Delete(key)
		UnregisterDebouncer(ns, mid.Name)
		mgr.Unregister(ns, midKey)
	}()

	// 6. Block until context is cancelled or stop is signalled externally.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stopChan:
		log.FromContext(ctx).Info("SyncCustomResourceV2 stop", "gvk", cr.GroupVersionKind().String(), "namespace", cr.GetNamespace(), "name", cr.GetName())
		return nil
	}
}
