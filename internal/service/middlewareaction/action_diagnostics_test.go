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
	"strings"
	"testing"
)

func TestActionDiagnosticsDoNotIncludeSensitivePayloads(t *testing.T) {
	secret := "super-secret-token"
	rawURL := "https://user:password@example.com/api/v1/apply?token=" + secret + "#frag"
	output := []byte("response contains " + secret)

	messages := []string{
		commandStepSuccessMessage(output),
		commandStepError(fmt.Errorf("exit status 1"), output).Error(),
		kubectlExecSuccessMessage([]string{"sh", "-c", "echo " + secret}, output),
		kubectlExecError(fmt.Errorf("exit status 1"), []string{"sh", "-c", "echo " + secret}, output, []byte(secret)).Error(),
		httpStepSuccessMessage("POST", rawURL, 200, output),
		httpStepError("response read", "POST", rawURL, 500, output, fmt.Errorf("read failed")).Error(),
		safeActionDiagnosticMessage("Authorization: Bearer " + secret + " password=" + secret + " token=" + secret),
	}

	for _, message := range messages {
		for _, forbidden := range []string{secret, "user:password", "response contains"} {
			if strings.Contains(message, forbidden) {
				t.Fatalf("expected diagnostic %q not to contain %q", message, forbidden)
			}
		}
	}
	if got := redactedURL(rawURL); got != "https://example.com/api/v1/apply" {
		t.Fatalf("unexpected redacted URL: %q", got)
	}
}

func TestReadBoundedActionOutputTruncatesOversizedResponses(t *testing.T) {
	input := strings.NewReader(strings.Repeat("a", int(maxActionOutputBytes)+1))

	output, truncated, err := readBoundedActionOutput(input)
	if err != nil {
		t.Fatalf("readBoundedActionOutput: %v", err)
	}
	if !truncated {
		t.Fatal("expected oversized output to be marked truncated")
	}
	if int64(len(output)) != maxActionOutputBytes {
		t.Fatalf("expected %d bytes, got %d", maxActionOutputBytes, len(output))
	}
}
