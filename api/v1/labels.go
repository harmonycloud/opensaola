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

package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Label, Annotation, Finalizer constants
// These constants define the K8s resource API conventions and belong to the api/v1 layer.
const (
	LabelPackageVersion = "middleware.cn/packageversion"
	LabelComponent      = "middleware.cn/component"
	LabelProject        = "middleware.cn/project"
	LabelPackageName    = "middleware.cn/packagename"
	LabelApp            = "middleware.cn/app"
	LabelConfigurations = "middleware.cn/configurations"

	LabelBaseline  = "middleware.cn/baseline"
	LabelUpdate    = "middleware.cn/update"
	LabelEnabled   = "middleware.cn/enabled"
	LabelInstall   = "middleware.cn/install"
	LabelUnInstall = "middleware.cn/uninstall"

	AnnotationInstallDigest  = "middleware.cn/installDigest"
	AnnotationInstallError   = "middleware.cn/installError"
	AnnotationUninstallError = "middleware.cn/uninstallError"
	// AnnotationSuspendReconcile temporarily prevents a Middleware or
	// MiddlewareOperator from writing its desired state to managed child
	// resources. Deletion/finalizer cleanup remains enabled.
	AnnotationSuspendReconcile = "middleware.cn/suspend-reconcile"
	// AnnotationResumePolicy selects how a paused Middleware resumes. The
	// policy is consumed while suspend-reconcile is still true, avoiding a
	// child-resource write window during recovery.
	AnnotationResumePolicy = "middleware.cn/resume-policy"

	AnnotationConfigurationOwnershipPolicy = "middleware.cn/configurationOwnershipPolicy"
	AnnotationConfigurationDeletePolicy    = "middleware.cn/configurationDeletePolicy"
	// AnnotationConfigurationDisablePolicy controls whether a rendered resource is deleted
	// after its MiddlewareConfiguration stops rendering it. Without an explicit policy,
	// PVC and PV resources are retained while other resource types are deleted; CRDs are
	// always retained by the disable lifecycle.
	AnnotationConfigurationDisablePolicy = "middleware.cn/configurationDisablePolicy"
	// AnnotationConfigurationOwnerUID and AnnotationConfigurationUID are written to
	// lifecycle-managed rendered resources so disable cleanup can verify identity safely.
	AnnotationConfigurationOwnerUID = "middleware.cn/configurationOwnerUID"
	AnnotationConfigurationUID      = "middleware.cn/configurationUID"

	LabelSource     = "middleware.cn/source"
	LabelSourceName = "middleware.cn/sourcename"

	LabelNoOperator = "middleware.cn/nooperator"
)

const (
	ConfigurationOwnershipPolicyManaged = "managed"

	ConfigurationDeletePolicyDelete = "delete"
	ConfigurationDeletePolicyOrphan = "orphan"

	ConfigurationDisablePolicyDelete = "delete"
	ConfigurationDisablePolicyOrphan = "orphan"
)

const (
	AnnotationDisasterSyncer    = "middleware.cn/disasterSyncer"
	AnnotationDataSyncer        = "middleware.cn/dataSyncer"
	AnnotationOppositeClusterId = "middleware.cn/oppositeClusterId"
)

// ReconcileResumePolicy describes a one-shot request for leaving a
// reconciliation pause. It remains an annotation because the controller
// consumes it after completing the requested action.
type ReconcileResumePolicy string

const (
	// ReconcileResumePolicyMerge adopts non-conflicting actual CR spec changes
	// before restoring normal desired-state reconciliation.
	ReconcileResumePolicyMerge ReconcileResumePolicy = "merge"
	// ReconcileResumePolicyApply explicitly discards actual CR changes and
	// reapplies OpenSaola's existing desired state.
	ReconcileResumePolicyApply ReconcileResumePolicy = "apply"
)

// IsReconcileSuspended reports whether a reconciliation write pause is enabled
// by annotations. The value is deliberately strict so an absent or malformed
// annotation never changes normal reconciliation behavior.
func IsReconcileSuspended(annotations map[string]string) bool {
	return annotations[AnnotationSuspendReconcile] == "true"
}

// ReconcileResumePolicyFor returns a supported resume policy. Unknown values
// are not silently treated as apply because that could overwrite a live change made during a pause.
func ReconcileResumePolicyFor(annotations map[string]string) (ReconcileResumePolicy, bool) {
	policy := ReconcileResumePolicy(annotations[AnnotationResumePolicy])
	switch policy {
	case ReconcileResumePolicyMerge, ReconcileResumePolicyApply:
		return policy, true
	default:
		return "", false
	}
}

// IsMiddlewareReconcileWriteSuspended keeps the primary Middleware write path
// closed after an unsafe direct unpause. A supported resume policy is the
// explicit approval to let the controller perform its next desired-state
// apply; without it, the durable ReconcilePaused condition remains a guard.
// MiddlewareOperator intentionally retains its independent pause semantics.
func IsMiddlewareReconcileWriteSuspended(annotations map[string]string, conditions []metav1.Condition) bool {
	if IsReconcileSuspended(annotations) {
		return true
	}
	if _, approved := ReconcileResumePolicyFor(annotations); approved {
		return false
	}
	for _, condition := range conditions {
		if condition.Type == CondTypeReconcilePaused && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// Finalizer names
const (
	FinalizerMiddleware         = "middleware.cn/middleware-cleanup"
	FinalizerMiddlewareOperator = "middleware.cn/middlewareoperator-cleanup"
	FinalizerPackageSecret      = "middleware.cn/package-secret-cleanup"
)

const (
	Secret     = "secret"
	FieldOwner = "opensaola"
)
