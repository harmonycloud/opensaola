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
	"testing"
)

func TestDeleteTemplateLineValues(t *testing.T) {
	tests := []struct {
		name              string
		template          string
		wantAPIVersion    string
		wantKind          string
		wantNameExpr      string
		wantNamespaceExpr string
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
			wantAPIVersion:    "apps/v1",
			wantKind:          "Deployment",
			wantNameExpr:      "{{ .Globe.Name }}-mysql",
			wantNamespaceExpr: "{{ .Globe.Namespace }}",
		},
		{
			name: "explicit literal namespace (cross-namespace render)",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Globe.Name }}-dashboard
  namespace: monitoring`,
			wantAPIVersion:    "v1",
			wantKind:          "ConfigMap",
			wantNameExpr:      "{{ .Globe.Name }}-dashboard",
			wantNamespaceExpr: "monitoring",
		},
		{
			name: "namespace under spec must not be extracted",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Globe.Name }}-config
spec:
  namespace: should-not-be-extracted`,
			wantAPIVersion:    "v1",
			wantKind:          "ConfigMap",
			wantNameExpr:      "{{ .Globe.Name }}-config",
			wantNamespaceExpr: "",
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
			wantNameExpr:   "{{ .Globe.Name }}-config",
		},
		{
			name: "metadata.name with quotes",
			template: `apiVersion: v1
kind: Secret
metadata:
  name: "{{ .Globe.Name }}-secret"`,
			wantAPIVersion: "v1",
			wantKind:       "Secret",
			wantNameExpr:   `"{{ .Globe.Name }}-secret"`,
		},
		{
			name: "no metadata section",
			template: `apiVersion: v1
kind: Namespace
spec:
  finalizers: []`,
			wantAPIVersion: "v1",
			wantKind:       "Namespace",
			wantNameExpr:   "",
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
			wantNameExpr:   "{{ .Globe.Name }}-sts",
		},
		{
			name:           "empty template",
			template:       "",
			wantAPIVersion: "",
			wantKind:       "",
			wantNameExpr:   "",
		},
		{
			name: "comments only",
			template: `# comment1
# comment2`,
			wantAPIVersion: "",
			wantKind:       "",
			wantNameExpr:   "",
		},
		{
			name: "CRD type template",
			template: `apiVersion: middleware.cn/v1
kind: Redis
metadata:
  name: {{ .Globe.Name }}
  namespace: {{ .Globe.Namespace }}`,
			wantAPIVersion:    "middleware.cn/v1",
			wantKind:          "Redis",
			wantNameExpr:      "{{ .Globe.Name }}",
			wantNamespaceExpr: "{{ .Globe.Namespace }}",
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
			wantNameExpr:   "correct-name",
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
			wantNameExpr:   "{{ .Globe.Name }}-svc",
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
			wantAPIVersion:    "apps/v1",
			wantKind:          "Deployment",
			wantNameExpr:      "{{ .Globe.Name }}-nacos-mysql",
			wantNamespaceExpr: "{{ .Globe.Namespace }}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAPI, gotKind, gotName, gotNS := deleteTemplateLineValues(tt.template)
			if gotAPI != tt.wantAPIVersion {
				t.Errorf("apiVersion: got %q, want %q", gotAPI, tt.wantAPIVersion)
			}
			if gotKind != tt.wantKind {
				t.Errorf("kind: got %q, want %q", gotKind, tt.wantKind)
			}
			if gotName != tt.wantNameExpr {
				t.Errorf("nameExpr: got %q, want %q", gotName, tt.wantNameExpr)
			}
			if gotNS != tt.wantNamespaceExpr {
				t.Errorf("namespaceExpr: got %q, want %q", gotNS, tt.wantNamespaceExpr)
			}
		})
	}
}

func TestNormalizeRenderedName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain name",
			input: "my-deployment",
			want:  "my-deployment",
		},
		{
			name:  "leading and trailing spaces",
			input: "  my-deployment  ",
			want:  "my-deployment",
		},
		{
			name:  "double-quoted",
			input: `"my-deployment"`,
			want:  "my-deployment",
		},
		{
			name:  "single-quoted",
			input: "'my-deployment'",
			want:  "my-deployment",
		},
		{
			name:  "quotes with spaces",
			input: `  "my-deployment"  `,
			want:  "my-deployment",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  "",
		},
		{
			name:  "nacos-mysql name",
			input: "yzk-nacos-mysql",
			want:  "yzk-nacos-mysql",
		},
		{
			name:  "rendered template with quotes",
			input: `"yzk-nacos-mysql"`,
			want:  "yzk-nacos-mysql",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRenderedName(tt.input)
			if got != tt.want {
				t.Errorf("normalizeRenderedName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
