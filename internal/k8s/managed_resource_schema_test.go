/*
Copyright 2026 The OpenSaola Authors.

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
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMutableWriteRequiresSchema(t *testing.T) {
	owner := intentUnitOwner()
	desired := intentUnitResource()
	for _, tc := range []struct {
		name, configuration string
		exists, rebuild     bool
	}{
		{name: "create primary"}, {name: "update primary", exists: true},
		{name: "create configuration", configuration: "cfg"}, {name: "adopt configuration", configuration: "cfg", exists: true},
		{name: "rebuild missing primary", rebuild: true}, {name: "recover rebuilt primary", rebuild: true, exists: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []client.Object{owner}
			if tc.exists {
				objects = append(objects, desired.DeepCopy())
			}
			// A nil write client makes any attempted API write (including dry-run)
			// fail this test. Reads still use a real fake tracker for both states.
			w := &ManagedResourceWriter{Reader: intentUnitClient(t, objects...)}
			write := w.Reconcile
			if tc.rebuild {
				write = w.RecreateMissing
			}
			if err := write(t.Context(), owner, desired, tc.configuration); err == nil || !strings.Contains(err.Error(), "intent schema resolver") {
				t.Fatalf("missing schema must block this write, got %v", err)
			}
		})
	}
}
