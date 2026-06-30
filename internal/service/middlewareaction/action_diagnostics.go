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
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

const maxActionOutputBytes int64 = 1 << 20
const maxActionDiagnosticMessageBytes = 2048

var (
	actionAuthHeaderPattern = regexp.MustCompile(`(?i)(authorization|proxy-authorization)(\s*[:=]\s*)(Bearer|Basic)\s+[^,\s;]+`)
	actionSecretKVPattern   = regexp.MustCompile(`(?i)(authorization|proxy-authorization|cookie|x-api-key|x-auth-token|access[-_]?key|secret[-_]?key|api[-_]?key|token|password|passwd|pwd|secret|credential)(\s*[:=]\s*)("[^"]*"|'[^']*'|[^,\s;]+)`)
)

func safeActionDiagnosticMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "action diagnostic unavailable"
	}
	message = actionAuthHeaderPattern.ReplaceAllString(message, `${1}${2}${3} ***`)
	message = actionSecretKVPattern.ReplaceAllString(message, `${1}${2}***`)
	if len(message) > maxActionDiagnosticMessageBytes {
		message = message[:maxActionDiagnosticMessageBytes] + "..."
	}
	return message
}

func commandStepSuccessMessage(output []byte) string {
	return fmt.Sprintf("command completed; outputBytes=%d", len(output))
}

func commandStepError(err error, output []byte) error {
	return fmt.Errorf("command failed: %w; outputBytes=%d", err, len(output))
}

func kubectlExecSuccessMessage(command []string, stdout []byte) string {
	return fmt.Sprintf("kubectl exec completed; commandArgs=%d outputBytes=%d", len(command), len(stdout))
}

func kubectlExecError(err error, command []string, stdout []byte, stderr []byte) error {
	return fmt.Errorf("execute kubectl error: %w; commandArgs=%d stdoutBytes=%d stderrBytes=%d", err, len(command), len(stdout), len(stderr))
}

func httpStepSuccessMessage(method string, rawURL string, statusCode int, output []byte) string {
	return fmt.Sprintf("http request completed: method=%s url=%s status=%d responseBytes=%d", method, redactedURL(rawURL), statusCode, len(output))
}

func httpStepError(action string, method string, rawURL string, statusCode int, output []byte, err error) error {
	if err != nil {
		return fmt.Errorf("execute http %s failed: method=%s url=%s status=%d responseBytes=%d: %w", action, method, redactedURL(rawURL), statusCode, len(output), err)
	}
	return fmt.Errorf("execute http %s failed: method=%s url=%s status=%d responseBytes=%d", action, method, redactedURL(rawURL), statusCode, len(output))
}

func parseExposedOutputError(outputType string, stepName string) error {
	return fmt.Errorf("parse exposed %s output failed for step %q", outputType, stepName)
}

func readBoundedActionOutput(r io.Reader) ([]byte, bool, error) {
	output, err := io.ReadAll(io.LimitReader(r, maxActionOutputBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(output)) > maxActionOutputBytes {
		return output[:maxActionOutputBytes], true, nil
	}
	return output, false, nil
}

func redactedURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid-url>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}
