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
	"testing"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestDiagnosticMessage_FieldPathAndStaleGeneration(t *testing.T) {
	t.Parallel()

	msg := (&Diagnostic{
		Phase: PhaseConfigValidation,
		Resource: ObjectRef{
			APIVersion: "middleware.cn/v1",
			Kind:       "Middleware",
			Namespace:  "default",
			Name:       "milvus",
		},
		FieldPath:          "spec.necessary.resource.etcd.volume",
		Expected:           "present",
		Actual:             "missing",
		Generation:         7,
		ObservedGeneration: 6,
		Cause:              errors.New("required parameter missing"),
		Next:               "set spec.necessary.resource.etcd.volume on the Middleware or provide a baseline default",
	}).Message()

	for _, want := range []string{
		"phase=config-validation",
		"resource=middleware.cn/v1/Middleware default/milvus",
		"fieldPath=spec.necessary.resource.etcd.volume",
		"expected=present",
		"actual=missing",
		"generation=7",
		"observedGeneration=6",
		"staleStatus=true",
		"causeCategory=Validation",
		"next=set spec.necessary.resource.etcd.volume",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected diagnostic message to contain %q, got %q", want, msg)
		}
	}
}

func TestDiagnosticMessage_DownstreamObjectRef(t *testing.T) {
	t.Parallel()

	msg := (&Diagnostic{
		Phase: PhaseRuntimeReconcile,
		Resource: ObjectRef{
			APIVersion: "middleware.cn/v1",
			Kind:       "Middleware",
			Namespace:  "default",
			Name:       "mysql",
		},
		FailedObject: ObjectRef{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
			Namespace:  "default",
			Name:       "mysql-primary",
		},
		Owner: ObjectRef{
			APIVersion: "middleware.cn/v1",
			Kind:       "Middleware",
			Namespace:  "default",
			Name:       "mysql",
		},
		Cause: errors.New("admission webhook denied the request"),
	}).Message()

	for _, want := range []string{
		"failedObject=apps/v1/StatefulSet default/mysql-primary",
		"ownerRef=middleware.cn/v1/Middleware default/mysql",
		"cause=admission webhook denied the request",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected diagnostic message to contain %q, got %q", want, msg)
		}
	}
}

func TestDiagnosticMessage_PreservesRegistryTLSCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("ErrImagePull: failed to pull image \"10.10.101.172:443/middleware/kubectl:v1.30.14\": x509: certificate signed by unknown authority")
	msg := (&Diagnostic{
		Phase:        PhaseRemotePullDeploy,
		FailedObject: ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: "middleware-operator", Name: "opensaola-install-crds"},
		Cause:        cause,
		Next:         "check node containerd registry trust under /etc/containerd/certs.d for 10.10.101.172:443",
	}).Message()

	for _, want := range []string{
		"phase=remote-pull-deploy",
		"failedObject=v1/Pod middleware-operator/opensaola-install-crds",
		"causeCategory=RegistryTLS",
		"ErrImagePull",
		"x509: certificate signed by unknown authority",
		"next=check node containerd registry trust",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected diagnostic message to contain %q, got %q", want, msg)
		}
	}
}

func TestCauseCategory_ImagePullRegistryTLSPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "ErrImagePull with x509 is RegistryTLS",
			err:  errors.New("ErrImagePull: failed to pull image: x509: certificate signed by unknown authority"),
			want: "RegistryTLS",
		},
		{
			name: "ImagePullBackOff without TLS is ImagePull",
			err:  errors.New("ImagePullBackOff: Back-off pulling image \"registry.local/middleware/foo:v1\""),
			want: "ImagePull",
		},
		{
			name: "manifest unknown is RegistryNotFound",
			err:  errors.New("manifest unknown: repository middleware/milvus not found"),
			want: "RegistryNotFound",
		},
		{
			name: "generic Kubernetes object not found is ResourceNotFound",
			err:  errors.New("get deployment middleware/demo: not found"),
			want: "ResourceNotFound",
		},
		{
			name: "PVC pending is PVCPending",
			err:  errors.New("PersistentVolumeClaim data pvc pending: FailedBinding"),
			want: "PVCPending",
		},
		{
			name: "Deployment progress deadline is RolloutStalled",
			err:  errors.New("deploymentCondition=Progressing; reason=ProgressDeadlineExceeded; message=Deployment exceeded its progress deadline"),
			want: "RolloutStalled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CauseCategory(tt.err); got != tt.want {
				t.Fatalf("CauseCategory() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCauseCategory_KubernetesAPIStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "wrapped forbidden is RBACForbidden",
			err:  fmt.Errorf("create custom resource: %w", apiErrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "demo", errors.New("cannot create resource"))),
			want: "RBACForbidden",
		},
		{
			name: "wrapped invalid is Validation",
			err: fmt.Errorf("apply custom resource: %w", apiErrors.NewInvalid(
				schema.GroupKind{Group: "middleware.cn", Kind: "Middleware"},
				"demo",
				field.ErrorList{field.Required(field.NewPath("spec", "necessary", "resource", "etcd", "volume"), "missing required volume")},
			)),
			want: "Validation",
		},
		{
			name: "wrapped conflict is Conflict",
			err:  fmt.Errorf("update status: %w", apiErrors.NewConflict(schema.GroupResource{Group: "middleware.cn", Resource: "middlewareoperators"}, "demo", errors.New("resource version conflict"))),
			want: "Conflict",
		},
		{
			name: "wrapped not found is ResourceNotFound",
			err:  fmt.Errorf("get deployment: %w", apiErrors.NewNotFound(schema.GroupResource{Group: "apps", Resource: "deployments"}, "demo")),
			want: "ResourceNotFound",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CauseCategory(tt.err); got != tt.want {
				t.Fatalf("CauseCategory() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiagnosticMessage_IncludesKubernetesAPIStatusDetails(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("apply custom resource: %w", apiErrors.NewInvalid(
		schema.GroupKind{Group: "middleware.cn", Kind: "Middleware"},
		"demo",
		field.ErrorList{field.Required(field.NewPath("spec", "necessary", "resource", "etcd", "volume"), "missing required volume")},
	))
	msg := (&Diagnostic{
		Phase:        PhaseRuntimeReconcile,
		FailedObject: ObjectRef{APIVersion: "middleware.cn/v1", Kind: "Middleware", Namespace: "default", Name: "demo"},
		Cause:        cause,
	}).Message()

	for _, want := range []string{
		"causeCategory=Validation",
		"apiStatusReason=Invalid",
		"apiStatusCode=422",
		"apiStatusKind=Middleware",
		"apiStatusName=demo",
		"apiStatusCauses=type=FieldValueRequired,field=spec.necessary.resource.etcd.volume",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected diagnostic message to contain %q, got %q", want, msg)
		}
	}
}

func TestDiagnosticLogValues_IncludeOwnerAndStaleStatus(t *testing.T) {
	t.Parallel()

	err := &Diagnostic{
		Phase:      PhaseWorkloadReadiness,
		Controller: "middlewareoperator-runtime",
		Resource: ObjectRef{
			APIVersion: "middleware.cn/v1",
			Kind:       "MiddlewareOperator",
			Namespace:  "middleware-operator",
			Name:       "opensaola",
		},
		FailedObject:       ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: "middleware-operator", Name: "opensaola-install-crds"},
		Owner:              ObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "middleware-operator", Name: "opensaola-controller-manager"},
		FieldPath:          "status.containerStatuses[name=kubectl].state.waiting",
		Expected:           "container ready",
		Actual:             "ImagePullBackOff",
		Generation:         4,
		ObservedGeneration: 3,
		Next:               "kubectl describe pod opensaola-install-crds -n middleware-operator",
		Cause:              errors.New("x509: certificate signed by unknown authority"),
	}

	values := DiagnosticLogValues(err)
	for _, want := range []any{
		"controller", "middlewareoperator-runtime",
		"ownerRefKind", "Deployment",
		"ownerRefName", "opensaola-controller-manager",
		"expected", "container ready",
		"actual", "ImagePullBackOff",
		"staleStatus", true,
		"next", "kubectl describe pod opensaola-install-crds -n middleware-operator",
	} {
		if !containsAny(values, want) {
			t.Fatalf("expected DiagnosticLogValues to contain %#v, got %#v", want, values)
		}
	}
}

func TestDiagnosticLogValues_IncludeKubernetesAPIStatusDetails(t *testing.T) {
	t.Parallel()

	err := &Diagnostic{
		Phase: PhaseRuntimeReconcile,
		Cause: apiErrors.NewForbidden(schema.GroupResource{Group: "", Resource: "pods"}, "demo", errors.New("cannot list pods")),
	}

	values := DiagnosticLogValues(err)
	for _, want := range []any{
		"failureCauseCategory", "RBACForbidden",
		"apiStatusReason", string(metav1.StatusReasonForbidden),
		"apiStatusCode", int32(403),
		"apiStatusKind", "pods",
		"apiStatusName", "demo",
	} {
		if !containsAny(values, want) {
			t.Fatalf("expected DiagnosticLogValues to contain %#v, got %#v", want, values)
		}
	}
}

func containsAny(values []any, want any) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
