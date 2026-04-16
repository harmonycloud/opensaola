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

package k8s

import (
	"bytes"
	"context"
	"fmt"

	"github.com/harmonycloud/opensaola/internal/k8s/kubeclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type ExecCommandInContainerParameter struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace"`
	Container   string   `json:"container"`
	ClusterAddr string   `json:"cluster_addr"`
	Token       string   `json:"token"`
	TlsInsecure bool     `json:"tls_insecure"`
	Command     []string `json:"command"`
}

// ExecCommandInContainer executes a command in the specified Pod and container.
func ExecCommandInContainer(ctx context.Context, config *rest.Config, parameter ExecCommandInContainerParameter) (string, error) {
	clientSet, err := kubeclient.GetClientSet()
	if err != nil {
		return "", fmt.Errorf("error getting clientset: %w", err)
	}
	// Create REST request
	req := clientSet.CoreV1().RESTClient().Post().
		Resource(parameter.Kind).
		Name(parameter.Name).
		Namespace(parameter.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: parameter.Container,
			Command:   parameter.Command,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	// Create SPDY executor
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("error creating SPDY executor: %w", err)
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	if err != nil {
		return "", fmt.Errorf("error executing command: %w; stderr: %s", err, stderr.String())
	}

	// Return stdout
	return stdout.String(), nil
}
