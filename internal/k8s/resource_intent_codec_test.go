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
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func intentSizeFixture(size int, random bool) *unstructured.Unstructured {
	desired := intentUnitResource()
	value := strings.Repeat("rendered-configuration-value-", size/29+1)
	if random {
		data := make([]byte, size)
		source := rand.New(rand.NewSource(42))
		alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
		for i := range data {
			data[i] = alphabet[source.Intn(len(alphabet))]
		}
		value = string(data)
	}
	desired.Object["data"] = map[string]any{"large": value, "keep": "yes"}
	return desired
}

func TestIntentCodecRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		size       int
		random     bool
		wantPacked bool
	}{
		{name: "compressible", size: 64 << 10, wantPacked: true},
		{name: "json_above_512_kib", size: 800 << 10, wantPacked: true},
		{name: "json_above_secret_payload_limit", size: 3 << 20, wantPacked: true},
		{name: "random_data_never_expands", size: 128 << 10, random: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := resourceIntent{Version: 1, OwnerUID: "owner", ResourceUID: "resource", SchemaHash: "schema", Desired: intentSizeFixture(tc.size, tc.random)}
			data, err := encodeResourceIntent(want)
			if err != nil {
				t.Fatal(err)
			}
			packed, ok := data[intentZstdKey]
			if !tc.random && ok != tc.wantPacked {
				t.Fatalf("packed=%v want=%v", ok, tc.wantPacked)
			}
			raw, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			stored := data[intentJSONKey]
			if ok {
				stored = packed
			}
			if len(stored) > len(raw) {
				t.Fatal("storage encoding expanded the record")
			}
			got, err := decodeResourceIntent(data)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*got, want) {
				t.Fatal("record changed during codec round trip")
			}
		})
	}
}

func TestIntentCodecRejectsCorruptOrAmbiguousData(t *testing.T) {
	encoder, err := intentEncoder()
	if err != nil {
		t.Fatal(err)
	}
	record := resourceIntent{Version: 1, Desired: intentSizeFixture(64<<10, false)}
	data, err := encodeResourceIntent(record)
	if err != nil {
		t.Fatal(err)
	}
	packed := data[intentZstdKey]
	corrupt := bytes.Clone(packed)
	corrupt[len(corrupt)-1] ^= 0xff
	for _, tc := range []struct {
		name string
		data map[string][]byte
	}{
		{name: "missing"},
		{name: "unknown_encoding", data: map[string][]byte{"intent.gzip": packed}},
		{name: "both_keys", data: map[string][]byte{intentJSONKey: []byte("{}"), intentZstdKey: packed}},
		{name: "invalid_frame", data: map[string][]byte{intentZstdKey: []byte("sensitive-value")}},
		{name: "truncated_frame", data: map[string][]byte{intentZstdKey: packed[:len(packed)/2]}},
		{name: "checksum_mismatch", data: map[string][]byte{intentZstdKey: corrupt}},
		{name: "plain_invalid_json", data: map[string][]byte{intentJSONKey: []byte("sensitive-value")}},
		{name: "compressed_invalid_json", data: map[string][]byte{intentZstdKey: encoder.EncodeAll([]byte("sensitive-value"), nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeResourceIntent(tc.data)
			if err == nil {
				t.Fatal("invalid record accepted")
			}
			if strings.Contains(err.Error(), "sensitive-value") {
				t.Fatal("payload leaked into error")
			}
		})
	}
}

func TestIntentCodecConcurrentRecords(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				want := resourceIntent{Version: 1, SchemaHash: fmt.Sprintf("worker-%d-record-%d", id, i), Desired: intentSizeFixture(32<<10, false)}
				data, err := encodeResourceIntent(want)
				if err != nil {
					t.Error(err)
					return
				}
				got, err := decodeResourceIntent(data)
				if err != nil {
					t.Error(err)
					return
				}
				if !reflect.DeepEqual(*got, want) {
					t.Error("concurrent records contaminated one another")
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestIntentLargeRecordPersistenceHasNoApplicationCeiling(t *testing.T) {
	owner := intentUnitOwner()
	live := intentUnitResource()
	desired := intentSizeFixture(3<<20, false)
	cli := intentUnitClient(t)
	writer := &ManagedResourceWriter{Client: cli, Reader: cli}
	record, err := writer.prepareResourceIntent(context.Background(), owner, live, desired, "", "schema", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Data[intentZstdKey]) == 0 {
		t.Fatal("large record not compressed")
	}
	setIntentPointer(live, record.Name)
	loaded, _, err := writer.loadResourceIntent(context.Background(), owner, live, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Desired.Object, intentObject(desired).Object) {
		t.Fatal("large record truncated")
	}
}

func TestIntentCollectionReadsBothRecordEncodings(t *testing.T) {
	for _, plain := range []bool{false, true} {
		name := "zstd"
		if plain {
			name = "plain_json"
		}
		t.Run(name, func(t *testing.T) {
			owner := intentUnitOwner()
			live := intentUnitResource()
			ref, err := intentOwnerReference(owner)
			if err != nil {
				t.Fatal(err)
			}
			live.SetOwnerReferences([]metav1.OwnerReference{ref})
			desired := intentSizeFixture(800<<10, false)
			cli := intentUnitClient(t, owner, live)
			writer := &ManagedResourceWriter{Client: cli, Reader: cli}
			secret, err := writer.prepareResourceIntent(context.Background(), owner, live, desired, "", "schema", nil)
			if err != nil {
				t.Fatal(err)
			}
			if plain {
				record, err := decodeResourceIntent(secret.Data)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				secret.Data = map[string][]byte{intentJSONKey: raw}
			}
			// Fake clients do not populate Secret UID; this copy is sufficient for the
			// collector's identity validation and no deletion occurs for a current record.
			secret.UID = "record-uid"
			secret.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
			if err := cli.Get(context.Background(), client.ObjectKeyFromObject(live), live); err != nil {
				t.Fatal(err)
			}
			setIntentPointer(live, secret.Name)
			if err := cli.Update(context.Background(), live); err != nil {
				t.Fatal(err)
			}
			collector := NewResourceIntentCollector(cli, cli)
			deleted, err := collector.collect(context.Background(), secret, time.Now())
			if err != nil || deleted {
				t.Fatalf("current record deleted=%v err=%v", deleted, err)
			}
		})
	}
}
