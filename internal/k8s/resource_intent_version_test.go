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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestIntentReaderVersionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		pause   bool
		wantErr bool
	}{
		{name: "legacy ordinary record", version: 1},
		{name: "declared ordinary record", version: 2},
		{name: "missing version rejected", version: 0, wantErr: true},
		{name: "future version rejected", version: 3, wantErr: true},
		{name: "pause snapshot is not ordinary history", version: 1, pause: true, wantErr: true},
		{name: "v2 does not reinterpret pause snapshot", version: 2, pause: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			owner, live := intentUnitOwner(), intentUnitResource()
			ref, err := intentOwnerReference(owner)
			if err != nil {
				t.Fatal(err)
			}
			live.SetOwnerReferences([]metav1.OwnerReference{ref})
			record := resourceIntent{Version: tc.version, OwnerUID: owner.UID, ResourceUID: live.GetUID(), SchemaHash: "schema", Desired: intentObject(live)}
			if tc.pause {
				record.Pause = &reconcilePauseData{}
			}
			secret, err := buildResourceIntentSecret(owner, record)
			if err != nil {
				t.Fatal(err)
			}
			secret.Name, secret.GenerateName = "version-record", ""
			setIntentPointer(live, secret.Name)
			cli := intentUnitClient(t, owner, live, secret)
			writer := &ManagedResourceWriter{Client: cli, Reader: cli}
			loaded, _, err := writer.loadResourceIntent(ctx, owner, live, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected compatibility result: %v", err)
			}
			if !tc.wantErr && (loaded == nil || loaded.Version != tc.version) {
				t.Fatal("reader silently reinterpreted a record version")
			}
			if err := cli.Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
				t.Fatalf("reading incompatible history must not delete it: %v", err)
			}
		})
	}
}
