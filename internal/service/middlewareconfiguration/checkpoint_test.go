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

package middlewareconfiguration

import (
	"context"
	"testing"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInventoryCheckpointIsolatesUnrelatedStatus(t *testing.T) {
	ctx := context.Background()
	mid := inventoryTestOwner()
	mid.Generation = 4
	mid.Status.Reason = "persisted by status updater"
	cli := fake.NewClientBuilder().WithScheme(inventoryTestClient(t).Scheme()).WithStatusSubresource(mid).WithObjects(mid).Build()
	if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
		t.Fatal(err)
	}
	stale := mid.DeepCopy()
	mid.Status.Reason = "unpublished local change"
	entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
	if _, err := beginConfigurationWrite(ctx, cli, mid, entry, entry.ResourceUID); err != nil {
		t.Fatal(err)
	}
	if err := commitConfigurationInventory(ctx, cli, stale, nil); err == nil {
		t.Fatal("stale owner discarded a later pending write")
	}
	if err := finishConfigurationWrite(ctx, cli, mid, &entry); err != nil {
		t.Fatal(err)
	}
	if mid.Status.RenderedConfigurationResourcesGeneration != 0 {
		t.Fatal("partial publication marked the entire generation complete")
	}
	if err := commitConfigurationInventory(ctx, cli, mid, []v1.RenderedConfigurationResource{entry}); err != nil {
		t.Fatal(err)
	}
	if mid.Status.Reason != "unpublished local change" {
		t.Fatal("checkpoint replaced an in-memory condition update")
	}
	stored := &v1.Middleware{}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Reason != "persisted by status updater" || stored.Status.RenderedConfigurationResourcesGeneration != 4 || stored.Status.PendingConfigurationWrite != nil || len(stored.Status.RenderedConfigurationResources) != 1 || mid.ResourceVersion != stored.ResourceVersion {
		t.Fatalf("wrong post-commit status: %#v", stored.Status)
	}
}

func TestInventoryRecoveryRequiresWriteReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, token, uid, configuration string
		wantErr, wantConfirmed          bool
	}{
		{"existing target before write", "previous", "resource-uid", "optional-config", false, false},
		{"replaced target carrying copied metadata", "attempt", "replacement", "optional-config", false, false},
		{"different source with matching token", "attempt", "resource-uid", "other", true, false},
		{"verified existing target", "attempt", "resource-uid", "optional-config", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mid := inventoryTestOwner()
			entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
			prior := entry
			prior.Name = "already-confirmed"
			mid.Status.RenderedConfigurationResources = []v1.RenderedConfigurationResource{prior}
			mid.Status.PendingConfigurationWrite = &v1.PendingConfigurationWrite{ID: "attempt", Resource: entry, ObservedUID: entry.ResourceUID}
			live := inventoryLiveObject(mid, entry, entry.ResourceUID, true)
			if tc.uid == "replacement" {
				live.SetUID("replacement")
			}
			a := live.GetAnnotations()
			a[v1.AnnotationConfigurationWriteID] = tc.token
			a[v1.LabelConfigurations] = tc.configuration
			live.SetAnnotations(a)
			cli := fake.NewClientBuilder().WithScheme(inventoryTestClient(t).Scheme()).WithStatusSubresource(mid).WithObjects(mid, live).Build()
			err := recoverConfigurationWrite(ctx, cli, mid)
			if (err != nil) != tc.wantErr || (mid.Status.PendingConfigurationWrite != nil) != tc.wantErr {
				t.Fatalf("recovery status: err=%v, pending=%v", err, mid.Status.PendingConfigurationWrite)
			}
			actual := mid.Status.RenderedConfigurationResources
			if len(actual) == 0 || actual[0].Name != prior.Name || (len(actual) == 2) != tc.wantConfirmed {
				t.Fatalf("wrong confirmed records: %#v", actual)
			}
		})
	}
}

func TestOpeningInventoryCheckpointReadsCurrentPause(t *testing.T) {
	ctx := context.Background()
	mid := inventoryTestOwner()
	cli := fake.NewClientBuilder().WithScheme(inventoryTestClient(t).Scheme()).WithStatusSubresource(mid).WithObjects(mid).Build()
	if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
		t.Fatal(err)
	}
	stale := mid.DeepCopy()
	mid.SetAnnotations(map[string]string{v1.AnnotationSuspendReconcile: "true"})
	if err := cli.Update(ctx, mid); err != nil {
		t.Fatal(err)
	}
	entry := inventoryTestResource(true, v1.ConfigurationDisablePolicyDelete)
	if _, err := beginConfigurationWrite(ctx, cli, stale, entry, entry.ResourceUID); err == nil {
		t.Fatal("began writing after the owner was paused")
	}
	if err := cli.Get(ctx, client.ObjectKeyFromObject(mid), mid); err != nil {
		t.Fatal(err)
	}
	if mid.Status.PendingConfigurationWrite != nil {
		t.Fatal("registered pending write against a paused owner")
	}
}
