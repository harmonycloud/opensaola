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

package status

import (
	"errors"
	"fmt"
	"strings"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type FailurePhase string

const (
	PhaseConfigValidation  FailurePhase = "config-validation"
	PhaseTemplateRender    FailurePhase = "template-render"
	PhasePackageBuild      FailurePhase = "package-build"
	PhaseImageDiscovery    FailurePhase = "image-discovery-export"
	PhaseRemotePullDeploy  FailurePhase = "remote-pull-deploy"
	PhaseRuntimeReconcile  FailurePhase = "runtime-reconcile"
	PhaseWorkloadReadiness FailurePhase = "workload-readiness"
)

type ObjectRef struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

func ObjectRefFromObject(obj metav1.Object, gvk schema.GroupVersionKind) ObjectRef {
	ref := ObjectRef{
		APIVersion: gvk.GroupVersion().String(),
		Kind:       gvk.Kind,
		Namespace:  obj.GetNamespace(),
		Name:       obj.GetName(),
	}
	return ref
}

func (r ObjectRef) String() string {
	var b strings.Builder
	if r.APIVersion != "" {
		b.WriteString(r.APIVersion)
		b.WriteString("/")
	}
	if r.Kind != "" {
		b.WriteString(r.Kind)
	} else {
		b.WriteString("<unknown-kind>")
	}
	if r.Namespace != "" {
		b.WriteString(" ")
		b.WriteString(r.Namespace)
		b.WriteString("/")
		b.WriteString(r.Name)
	} else if r.Name != "" {
		b.WriteString(" ")
		b.WriteString(r.Name)
	}
	return b.String()
}

type Diagnostic struct {
	Phase              FailurePhase
	Controller         string
	Resource           ObjectRef
	FailedObject       ObjectRef
	Owner              ObjectRef
	FieldPath          string
	Expected           string
	Actual             string
	Generation         int64
	ObservedGeneration int64
	Next               string
	Cause              error
}

func (d *Diagnostic) Error() string {
	return d.Message()
}

func (d *Diagnostic) Unwrap() error {
	return d.Cause
}

func (d *Diagnostic) Message() string {
	if d == nil {
		return ""
	}

	parts := make([]string, 0, 13)
	if d.Phase != "" {
		parts = append(parts, "phase="+string(d.Phase))
	}
	if d.Controller != "" {
		parts = append(parts, "controller="+d.Controller)
	}
	if d.Resource.Kind != "" || d.Resource.Name != "" {
		parts = append(parts, "resource="+d.Resource.String())
	}
	if d.FailedObject.Kind != "" || d.FailedObject.Name != "" {
		parts = append(parts, "failedObject="+d.FailedObject.String())
	}
	if d.Owner.Kind != "" || d.Owner.Name != "" {
		parts = append(parts, "ownerRef="+d.Owner.String())
	}
	if d.FieldPath != "" {
		parts = append(parts, "fieldPath="+d.FieldPath)
	}
	if d.Expected != "" {
		parts = append(parts, "expected="+d.Expected)
	}
	if d.Actual != "" {
		parts = append(parts, "actual="+d.Actual)
	}
	if d.Generation != 0 {
		parts = append(parts, fmt.Sprintf("generation=%d", d.Generation))
	}
	if d.ObservedGeneration != 0 {
		parts = append(parts, fmt.Sprintf("observedGeneration=%d", d.ObservedGeneration))
	}
	if d.Generation != 0 && d.ObservedGeneration != 0 && d.ObservedGeneration < d.Generation {
		parts = append(parts, "staleStatus=true")
	}
	if category := CauseCategory(d.Cause); category != "" {
		parts = append(parts, "causeCategory="+category)
	}
	if status := apiStatusFromError(d.Cause); status != nil {
		parts = append(parts, "apiStatusReason="+string(status.Reason))
		if status.Code != 0 {
			parts = append(parts, fmt.Sprintf("apiStatusCode=%d", status.Code))
		}
		if status.Details != nil {
			if status.Details.Kind != "" {
				parts = append(parts, "apiStatusKind="+status.Details.Kind)
			}
			if status.Details.Name != "" {
				parts = append(parts, "apiStatusName="+status.Details.Name)
			}
			if causes := formatStatusCauses(status.Details.Causes); causes != "" {
				parts = append(parts, "apiStatusCauses="+causes)
			}
		}
	}
	if d.Cause != nil {
		parts = append(parts, "cause="+d.Cause.Error())
	}
	if d.Next != "" {
		parts = append(parts, "next="+d.Next)
	}
	return strings.Join(parts, "; ")
}

func WrapDiagnostic(err error, base Diagnostic) error {
	if err == nil {
		return nil
	}
	var diagnostic *Diagnostic
	if errors.As(err, &diagnostic) {
		return err
	}
	base.Cause = err
	return &base
}

func CauseCategory(err error) string {
	if err == nil {
		return ""
	}
	if status := apiStatusFromError(err); status != nil {
		switch status.Reason {
		case metav1.StatusReasonForbidden, metav1.StatusReasonUnauthorized:
			return "RBACForbidden"
		case metav1.StatusReasonInvalid, metav1.StatusReasonBadRequest:
			return "Validation"
		case metav1.StatusReasonConflict:
			return "Conflict"
		case metav1.StatusReasonTooManyRequests, metav1.StatusReasonTimeout, metav1.StatusReasonServerTimeout:
			return "APIServerTransient"
		case metav1.StatusReasonNotFound:
			if status.Details != nil && status.Details.Kind != "" {
				return "ResourceNotFound"
			}
			return "CRDDiscovery"
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "x509") || strings.Contains(msg, "certificate signed by unknown authority") || strings.Contains(msg, "tls"):
		return "RegistryTLS"
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "authentication required") || strings.Contains(msg, "pull access denied"):
		return "RegistryAuth"
	case strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "name unknown") || (strings.Contains(msg, "not found") && (strings.Contains(msg, "image") || strings.Contains(msg, "registry") || strings.Contains(msg, "repository"))):
		return "RegistryNotFound"
	case strings.Contains(msg, "imagepullbackoff") || strings.Contains(msg, "errimagepull") || strings.Contains(msg, "failed to pull image") || strings.Contains(msg, "pull access denied"):
		return "ImagePull"
	case strings.Contains(msg, "registry") || strings.Contains(msg, "repository") || strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "name unknown"):
		return "Registry"
	case strings.Contains(msg, "forbidden") || strings.Contains(msg, "rbac") || strings.Contains(msg, "permission"):
		return "RBACForbidden"
	case strings.Contains(msg, "failedmount") || strings.Contains(msg, "failedattachvolume") || strings.Contains(msg, "mountvolume") || strings.Contains(msg, "unable to attach or mount volumes"):
		return "VolumeMountFailed"
	case strings.Contains(msg, "failedscheduling") || strings.Contains(msg, "unschedulable"):
		return "SchedulingUnschedulable"
	case strings.Contains(msg, "failedbinding") || strings.Contains(msg, "persistentvolumeclaim") || strings.Contains(msg, "pvc pending"):
		return "PVCPending"
	case strings.Contains(msg, "progressdeadlineexceeded") || strings.Contains(msg, "exceeded its progress deadline"):
		return "RolloutStalled"
	case strings.Contains(msg, "minimumreplicasunavailable") || strings.Contains(msg, "does not have minimum availability"):
		return "WorkloadUnavailable"
	case strings.Contains(msg, "rbac"):
		return "RBAC"
	case strings.Contains(msg, "no matches for kind") || strings.Contains(msg, "server could not find the requested resource"):
		return "CRDDiscovery"
	case strings.Contains(msg, "not found"):
		return "ResourceNotFound"
	case strings.Contains(msg, "template") || strings.Contains(msg, "parse"):
		return "TemplateRender"
	case strings.Contains(msg, "validation") || strings.Contains(msg, "invalid") || strings.Contains(msg, "required"):
		return "Validation"
	default:
		return "Error"
	}
}

func DiagnosticLogValues(err error) []any {
	var diagnostic *Diagnostic
	if !errors.As(err, &diagnostic) || diagnostic == nil {
		return nil
	}
	values := []any{
		"failurePhase", string(diagnostic.Phase),
		"failureCauseCategory", CauseCategory(diagnostic.Cause),
	}
	if status := apiStatusFromError(diagnostic.Cause); status != nil {
		values = append(values,
			"apiStatusReason", string(status.Reason),
			"apiStatusCode", status.Code,
		)
		if status.Details != nil {
			values = append(values,
				"apiStatusKind", status.Details.Kind,
				"apiStatusName", status.Details.Name,
			)
			if causes := formatStatusCauses(status.Details.Causes); causes != "" {
				values = append(values, "apiStatusCauses", causes)
			}
		}
	}
	if diagnostic.Controller != "" {
		values = append(values, "controller", diagnostic.Controller)
	}
	if diagnostic.Resource.Kind != "" || diagnostic.Resource.Name != "" {
		values = append(values,
			"resourceAPIVersion", diagnostic.Resource.APIVersion,
			"resourceKind", diagnostic.Resource.Kind,
			"resourceNamespace", diagnostic.Resource.Namespace,
			"resourceName", diagnostic.Resource.Name,
		)
	}
	if diagnostic.FailedObject.Kind != "" || diagnostic.FailedObject.Name != "" {
		values = append(values,
			"failedObjectAPIVersion", diagnostic.FailedObject.APIVersion,
			"failedObjectKind", diagnostic.FailedObject.Kind,
			"failedObjectNamespace", diagnostic.FailedObject.Namespace,
			"failedObjectName", diagnostic.FailedObject.Name,
		)
	}
	if diagnostic.Owner.Kind != "" || diagnostic.Owner.Name != "" {
		values = append(values,
			"ownerRefAPIVersion", diagnostic.Owner.APIVersion,
			"ownerRefKind", diagnostic.Owner.Kind,
			"ownerRefNamespace", diagnostic.Owner.Namespace,
			"ownerRefName", diagnostic.Owner.Name,
		)
	}
	if diagnostic.FieldPath != "" {
		values = append(values, "failedFieldPath", diagnostic.FieldPath)
	}
	if diagnostic.Expected != "" {
		values = append(values, "expected", diagnostic.Expected)
	}
	if diagnostic.Actual != "" {
		values = append(values, "actual", diagnostic.Actual)
	}
	if diagnostic.Generation != 0 {
		values = append(values, "generation", diagnostic.Generation)
	}
	if diagnostic.ObservedGeneration != 0 {
		values = append(values, "observedGeneration", diagnostic.ObservedGeneration)
	}
	if diagnostic.Generation != 0 && diagnostic.ObservedGeneration != 0 && diagnostic.ObservedGeneration < diagnostic.Generation {
		values = append(values, "staleStatus", true)
	}
	if diagnostic.Next != "" {
		values = append(values, "next", diagnostic.Next)
	}
	return values
}

func apiStatusFromError(err error) *metav1.Status {
	if err == nil {
		return nil
	}
	var apiStatus apiErrors.APIStatus
	if errors.As(err, &apiStatus) {
		status := apiStatus.Status()
		return &status
	}
	return nil
}

func formatStatusCauses(causes []metav1.StatusCause) string {
	if len(causes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(causes))
	for _, cause := range causes {
		fields := make([]string, 0, 3)
		if cause.Type != "" {
			fields = append(fields, "type="+string(cause.Type))
		}
		if cause.Field != "" {
			fields = append(fields, "field="+cause.Field)
		}
		if cause.Message != "" {
			fields = append(fields, "message="+cause.Message)
		}
		if len(fields) > 0 {
			parts = append(parts, strings.Join(fields, ","))
		}
	}
	return strings.Join(parts, "|")
}
