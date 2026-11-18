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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestExecuteExposedOutputReachesNextStep(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "no-kubeconfig"))
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	const large int64 = 9007199254740993
	deadline := large
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "default"},
		Spec:       corev1.PodSpec{ActiveDeadlineSeconds: &deadline},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ids":[9007199254740992,9007199254740993],"ratio":1.5}`)
	})
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "a\nb\n")
	})
	body := make(chan string, 1)
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		body <- string(b)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	objectStep := func(step, format, name string) v1.Step {
		return v1.Step{
			Name: step, Type: StepTypeKubectlGet, Output: v1.Output{Expose: true, Type: format},
			CUE: fmt.Sprintf(`parameters: resource: {apiversion: "v1", kind: "Pod", name: %q, namespace: "default"}`, name),
		}
	}
	steps := []v1.Step{
		{Name: "cmd", CMD: v1.CMD{Command: []string{"printf 'ids: [9007199254740992, 9007199254740993]\\n'"}}, Output: v1.Output{Expose: true, Type: "yaml"}},
		{Name: "cmdtext", CMD: v1.CMD{Command: []string{"printf 'a\\nb\\n'"}}, Output: v1.Output{Expose: true, Type: "string"}},
		{Name: "http", HTTP: v1.Http{URL: server.URL + "/json", Method: "GET"}, Output: v1.Output{Expose: true, Type: "json"}},
		{Name: "httptext", HTTP: v1.Http{URL: server.URL + "/text", Method: "GET"}, Output: v1.Output{Expose: true, Type: "string"}},
		{Name: "cmdnull", CMD: v1.CMD{Command: []string{"printf null"}}, Output: v1.Output{Expose: true, Type: "json"}},
		{Name: "cmdempty", CMD: v1.CMD{Command: []string{"printf ''"}}, Output: v1.Output{Expose: true, Type: "yaml"}},
		objectStep("resourceJSON", "json", pod.Name),
		objectStep("resourceYAML", "yaml", pod.Name),
		objectStep("missingJSON", "json", "missing"),
		objectStep("missingYAML", "yaml", "missing"),
		objectStep("getText", "string", pod.Name),
		{Name: "consume", HTTP: v1.Http{
			URL: server.URL + "/result", Method: "POST", Body: `{{ .Step.cmd.output | toJson }}
{{ .Step.http.output | toJson }}
{{ .Step.resourceJSON.output.spec.activeDeadlineSeconds }} {{ .Step.resourceYAML.output.spec.activeDeadlineSeconds }}
{{ hasKey .Step.missingJSON "output" }} {{ toJson .Step.missingJSON.output }}
{{ hasKey .Step.missingYAML "output" }} {{ toJson .Step.missingYAML.output }}
{{ .Step.cmdnull.output | toJson }} {{ .Step.cmdempty.output | toJson }}
{{ hasKey .Step.getText "output" }}
{{ .Step.cmdtext.output | toJson }} {{ .Step.httptext.output | toJson }}`}},
	}
	baseline := &v1.MiddlewareActionBaseline{ObjectMeta: metav1.ObjectMeta{Name: "exposed"}, Spec: v1.MiddlewareActionBaselineSpec{Steps: steps}}
	action := &v1.MiddlewareAction{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"}, Spec: v1.MiddlewareActionSpec{Baseline: baseline.Name}}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(baseline, action, pod).WithStatusSubresource(action).Build()
	if err := Execute(context.Background(), cli, action); err != nil {
		t.Fatal(err)
	}
	const want = `{"ids":[9007199254740992,9007199254740993]}
{"ids":[9007199254740992,9007199254740993],"ratio":1.5}
9007199254740993 9007199254740993
true null
true {}
null {}
false
"ab" "a\nb\n"`
	select {
	case got := <-body:
		if got != want {
			t.Fatalf("consumer received:\n%s\nwant:\n%s", got, want)
		}
	default:
		t.Fatal("consumer was never called")
	}
}

func TestExposeStepOutputRejectsInvalidObjects(t *testing.T) {
	for _, tt := range []struct{ format, input string }{
		{"json", "secret-data"},
		{"json", "[]"},
		{"yaml", "secret-data: ["},
		{"yaml", "- secret-data"},
	} {
		ctx := context.Background()
		step := v1.Step{Name: "source", Output: v1.Output{Expose: true, Type: tt.format}}
		if err := exposeStepOutput(&ctx, step, []byte(tt.input)); err == nil || err.Error() != parseExposedOutputError(tt.format, step.Name).Error() {
			t.Fatalf("%s invalid input: got %v", tt.format, err)
		}
	}
}
