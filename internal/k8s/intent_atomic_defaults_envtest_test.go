//go:build envtest

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
	"context"
	"reflect"
	"strings"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func testVersionedAtomicDefaults(t *testing.T, ctx context.Context, cli client.Client, resolver IntentSchemaResolver, namespace string) {
	const binding = `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: atomic-subject}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: fixture}
subjects:
- {kind: User, name: fixture}
- {kind: Group, name: fixture}
- {kind: ServiceAccount, name: fixture}
`
	const webhook = `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata: {name: atomic-rules}
webhooks:
- name: ignore.fixture.test
  admissionReviewVersions: [v1]
  sideEffects: None
  clientConfig: {url: 'https://fixture.invalid/no-requests'}
  rules:
  - operations: [CREATE]
    apiGroups: [fixture.invalid]
    apiVersions: [v1]
    resources: [fixtures]
`
	const policy = `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: atomic-match}
spec:
  matchConstraints:
    resourceRules:
    - operations: [CREATE]
      apiGroups: [fixture.invalid]
      apiVersions: [v1]
      resources: [fixtures]
  validations: [{expression: 'true'}]
`
	for _, tc := range []struct {
		name, manifest, change, declaredChange string
	}{
		{"rbac_subjects", binding, `[
			{"op":"test","path":"/subjects/0/apiGroup","value":"rbac.authorization.k8s.io"},
			{"op":"test","path":"/subjects/1/apiGroup","value":"rbac.authorization.k8s.io"},
			{"op":"add","path":"/subjects/2/apiGroup","value":"rbac.authorization.k8s.io"}]`, `[{"op":"replace","path":"/subjects/0/name","value":"other"}]`},
		{"webhook_rules", webhook, `[
			{"op":"test","path":"/webhooks/0/rules/0/scope","value":"*"},
			{"op":"replace","path":"/webhooks/0/rules/0/scope","value":"Namespaced"}]`, `[{"op":"replace","path":"/webhooks/0/rules/0/operations/0","value":"DELETE"}]`},
		{"admission_policy_rules", policy, `[
			{"op":"test","path":"/spec/matchConstraints/resourceRules/0/scope","value":"*"},
			{"op":"test","path":"/spec/matchConstraints/matchPolicy","value":"Equivalent"},
			{"op":"test","path":"/spec/matchConstraints/namespaceSelector","value":{}},
			{"op":"test","path":"/spec/matchConstraints/objectSelector","value":{}},
			{"op":"replace","path":"/spec/matchConstraints/resourceRules/0/scope","value":"Namespaced"}]`, `[{"op":"replace","path":"/spec/matchConstraints/resourceRules/0/operations/0","value":"DELETE"}]`},
		{"admission_policy_nonempty_selector", policy, `[
			{"op":"test","path":"/spec/matchConstraints/namespaceSelector","value":{}},
			{"op":"add","path":"/spec/matchConstraints/namespaceSelector/matchLabels","value":{"team":"external"}}]`, `[{"op":"replace","path":"/spec/matchConstraints/resourceRules/0/apiGroups/0","value":"other.invalid"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var desired unstructured.Unstructured
			if err := yaml.Unmarshal([]byte(tc.manifest), &desired); err != nil {
				t.Fatal(err)
			}
			desired.SetName(desired.GetName() + "-" + strings.ReplaceAll(tc.name, "_", "-"))
			if tc.manifest == binding {
				desired.SetNamespace(namespace)
			}
			converter, _, err := resolver.Resolve(ctx, desired.GroupVersionKind())
			if err != nil {
				t.Fatal(err)
			}
			observation := desired.DeepCopy()
			if err := cli.Create(ctx, observation); err != nil {
				t.Fatal(err)
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(&desired), observation); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDeclaredResourceIntent(converter, &desired, observation); err != nil {
				t.Fatal(err)
			}
			write, err := PlanDeclaredResourceIntent(converter, &desired, observation, &desired, &desired, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(write.Object.Object, observation.Object) {
				t.Fatal("server defaults triggered another write")
			}

			// Check the defaults in the real GET and change one in memory.
			patch, err := jsonpatch.DecodePatch([]byte(tc.change))
			if err != nil {
				t.Fatal(err)
			}
			actual, err := observation.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			modified, err := patch.Apply(actual)
			if err != nil {
				t.Fatalf("API returned unexpected defaults: %v", err)
			}
			var changed unstructured.Unstructured
			if err := changed.UnmarshalJSON(modified); err != nil {
				t.Fatal(err)
			}
			declaredPatch, err := jsonpatch.DecodePatch([]byte(tc.declaredChange))
			if err != nil {
				t.Fatal(err)
			}
			rewritten, err := declaredPatch.Apply(modified)
			if err != nil {
				t.Fatal(err)
			}
			var changedDeclaration unstructured.Unstructured
			if err := changedDeclaration.UnmarshalJSON(rewritten); err != nil {
				t.Fatal(err)
			}
			for _, plan := range []*DeclaredIntentMergeResult{write, restoreVerificationPlan(t, converter, &desired, write)} {
				if err := plan.Validate(converter, &changed); err != nil {
					t.Fatalf("rejected an undeclared field: %v", err)
				}
				if err := plan.Validate(converter, &changedDeclaration); err == nil {
					t.Fatal("accepted a rewritten declared field")
				}
			}

		})
	}
}
