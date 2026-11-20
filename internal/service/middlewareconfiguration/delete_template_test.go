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

package middlewareconfiguration

import (
	"context"
	"testing"

	"github.com/harmonycloud/opensaola/pkg/tools"
)

func TestDeleteIdentityFromPackageTemplates(t *testing.T) {
	tests := []struct {
		name           string
		template       string
		wantAPIVersion string
		wantKind       string
		wantName       string
		wantNamespace  string
	}{
		{
			name: "standard YAML template",
			template: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Globe.Name }}-mysql
  namespace: {{ .Globe.Namespace }}
spec:
  replicas: 1`,
			wantAPIVersion: "apps/v1",
			wantKind:       "Deployment",
			wantName:       "demo-mysql",
			wantNamespace:  "monitoring",
		},
		{
			name: "explicit literal namespace (cross-namespace render)",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Globe.Name }}-dashboard
  namespace: monitoring`,
			wantAPIVersion: "v1",
			wantKind:       "ConfigMap",
			wantName:       "demo-dashboard",
			wantNamespace:  "monitoring",
		},
		{
			name: "namespace under spec must not be extracted",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Globe.Name }}-config
spec:
  namespace: should-not-be-extracted`,
			wantAPIVersion: "v1",
			wantKind:       "ConfigMap",
			wantName:       "demo-config",
			wantNamespace:  "",
		},
		{
			name: "template with comments",
			template: `# this is a comment
apiVersion: v1
kind: ConfigMap
metadata:
  # name comment
  name: {{ .Globe.Name }}-config
  labels:
    app: test`,
			wantAPIVersion: "v1",
			wantKind:       "ConfigMap",
			wantName:       "demo-config",
		},
		{
			name: "metadata.name with quotes",
			template: `apiVersion: v1
kind: Secret
metadata:
  name: "{{ .Globe.Name }}-secret"`,
			wantAPIVersion: "v1",
			wantKind:       "Secret",
			wantName:       "demo-secret",
		},
		{
			name: "no metadata section",
			template: `apiVersion: v1
kind: Namespace
spec:
  finalizers: []`,
			wantAPIVersion: "v1",
			wantKind:       "Namespace",
			wantName:       "",
		},
		{
			name: "metadata followed by other top-level fields",
			template: `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ .Globe.Name }}-sts
spec:
  serviceName: test`,
			wantAPIVersion: "apps/v1",
			wantKind:       "StatefulSet",
			wantName:       "demo-sts",
		},
		{
			name:           "empty template",
			template:       "",
			wantAPIVersion: "",
			wantKind:       "",
			wantName:       "",
		},
		{
			name: "comments only",
			template: `# comment1
# comment2`,
			wantAPIVersion: "",
			wantKind:       "",
			wantName:       "",
		},
		{
			name: "CRD type template",
			template: `apiVersion: middleware.cn/v1
kind: Redis
metadata:
  name: {{ .Globe.Name }}
  namespace: {{ .Globe.Namespace }}`,
			wantAPIVersion: "middleware.cn/v1",
			wantKind:       "Redis",
			wantName:       "demo",
			wantNamespace:  "monitoring",
		},
		{
			name: "name not under metadata (should not be extracted)",
			template: `apiVersion: v1
kind: ConfigMap
spec:
  name: should-not-be-extracted
metadata:
  name: correct-name`,
			wantAPIVersion: "v1",
			wantKind:       "ConfigMap",
			wantName:       "correct-name",
		},
		{
			name: "metadata with multiple indentation levels",
			template: `apiVersion: v1
kind: Service
metadata:
  name: {{ .Globe.Name }}-svc
  labels:
    app: {{ .Globe.Name }}
    tier: frontend
  annotations:
    desc: test
spec:
  ports:
    - port: 80`,
			wantAPIVersion: "v1",
			wantKind:       "Service",
			wantName:       "demo-svc",
		},
		{
			name: "nacos derived mysql scenario (regression key case)",
			template: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Globe.Name }}-nacos-mysql
  namespace: {{ .Globe.Namespace }}
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: mysql
          image: mysql:5.7`,
			wantAPIVersion: "apps/v1",
			wantKind:       "Deployment",
			wantName:       "demo-nacos-mysql",
			wantNamespace:  "monitoring",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity, err := parseDeleteIdentity(tt.template)
			if tt.wantAPIVersion == "" && err != nil {
				return // An empty document has no deletion target.
			}
			if err != nil {
				t.Fatal(err)
			}
			values := &tools.TemplateValues{Globe: map[string]interface{}{"Name": "demo", "Namespace": "monitoring"}}
			for _, tc := range []struct {
				key   string
				field deleteIdentityField
				want  string
			}{
				{"apiVersion", identity.APIVersion, tt.wantAPIVersion},
				{"kind", identity.Kind, tt.wantKind},
				{"metadata.name", identity.Name, tt.wantName},
				{"metadata.namespace", identity.Namespace, tt.wantNamespace},
			} {
				if !tc.field.Present() && tc.want == "" {
					continue
				}
				got, err := tc.field.Render(context.Background(), values)
				if err != nil || got != tc.want {
					t.Errorf("%s: got %q, %v; want %q", tc.key, got, err, tc.want)
				}
			}
		})
	}
}

func TestDeleteGVKRejectsMalformedIdentityBeforeDiscovery(t *testing.T) {
	for _, test := range []struct {
		version, kind string
		valid         bool
	}{
		{"v1", "ConfigMap", true},
		{"example.io/v1alpha1", "LogPolicy", true},
		{"v1/extra/path", "ConfigMap", false},
		{"example.io/", "ConfigMap", false},
		{"/v1", "ConfigMap", false},
		{"'v1'", "ConfigMap", false},
		{"v1", "'ConfigMap'", false},
		{"v1", "<no value>", false},
	} {
		t.Run(test.version+"_"+test.kind, func(t *testing.T) {
			gvk, err := deleteResourceGVK(test.version, test.kind)
			if (err == nil) != test.valid || (test.valid && (gvk.GroupVersion().String() != test.version || gvk.Kind != test.kind)) {
				t.Fatalf("unexpected deletion identity %v, error %v", gvk, err)
			}
		})
	}
}
