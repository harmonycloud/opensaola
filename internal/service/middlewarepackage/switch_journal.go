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

package middlewarepackage

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/packages"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The header is mutable. Sources and Changes are stored once in the prepared snapshot.
type switchJournal struct {
	Phase     string
	Target    string
	TargetUID types.UID
	Digest    string
	Failure   string         `json:",omitempty"`
	Terminal  bool           `json:",omitempty"`
	Sources   []switchSource `json:"-"`
	Changes   []switchChange `json:"-"`
}

type switchSnapshot struct {
	Sources []switchSource
	Changes []switchChange
}

type switchJournalHeader struct {
	switchJournal
	SnapshotDigest string `json:",omitempty"`
	Parts          int    `json:",omitempty"`
}

const (
	switchRecordType  corev1.SecretType = "middleware.cn/package-switch.v1"
	switchPartType    corev1.SecretType = "middleware.cn/package-switch-part.v1"
	switchHeaderKey                     = "journal"
	switchSnapshotKey                   = "snapshot.gz"
	switchPartSize                      = 768 * 1024 // room for the journal header within the Secret data limit
)

func switchRecordName(component string) string {
	sum := sha256.Sum256([]byte(component))
	return fmt.Sprintf("package-switch-%x", sum[:16])
}

func createSwitchJournal(ctx context.Context, cli client.Client, component string, target *corev1.Secret, j *switchJournal) (*corev1.Secret, error) {
	if component == "" || target == nil || target.Namespace != packages.DataNamespace || target.Name == "" || target.UID == "" {
		return nil, fmt.Errorf("invalid package switch target")
	}
	if j == nil || j.Target != target.Name || j.TargetUID != target.UID || j.Phase != "planning" || j.Digest == "" || len(j.Changes)+len(j.Sources) != 0 {
		return nil, fmt.Errorf("invalid initial package switch identity or plan")
	}
	header, err := json.Marshal(switchJournalHeader{switchJournal: *j})
	if err != nil {
		return nil, err
	}
	// The record outlives the target archive to allow rollback during deletion.
	record := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: switchRecordName(component), Namespace: packages.DataNamespace,
			Annotations: map[string]string{v1.LabelComponent: component},
		},
		Type: switchRecordType,
		Data: map[string][]byte{switchHeaderKey: header},
	}
	if err := cli.Create(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

func decodeSwitchRecord(record *corev1.Secret, component string) (switchJournalHeader, error) {
	var h switchJournalHeader
	if component == "" || record.Namespace != packages.DataNamespace || record.Name != switchRecordName(component) || record.Annotations[v1.LabelComponent] != component || record.Type != switchRecordType || record.UID == "" || record.ResourceVersion == "" {
		return h, fmt.Errorf("Secret %s is not the package switch record for component %q", client.ObjectKeyFromObject(record), component)
	}
	if err := json.Unmarshal(record.Data[switchHeaderKey], &h); err != nil {
		return h, fmt.Errorf("decode package switch %s: %w", record.Name, err)
	}
	switch h.Phase {
	case "planning", "rollback", "prepared", "committed":
	default:
		return h, fmt.Errorf("unknown package switch phase %q", h.Phase)
	}
	if h.Target == "" || h.TargetUID == "" || h.Digest == "" || h.Parts < 0 || (h.SnapshotDigest == "" && (h.Parts != 0 || h.Phase == "prepared" || h.Phase == "committed")) {
		return h, fmt.Errorf("incomplete package switch record %s", record.Name)
	}
	return h, nil
}

func switchPartName(record *corev1.Secret, digest string, i int) string {
	// Different plans and replacement records may have in-flight writers simultaneously.
	sum := sha256.Sum256([]byte(string(record.UID) + "/" + digest))
	return fmt.Sprintf("package-switch-part-%x-%d", sum[:16], i)
}

func readSwitchPart(ctx context.Context, reader client.Reader, record *corev1.Secret, name string) ([]byte, error) {
	var part corev1.Secret
	if err := reader.Get(ctx, client.ObjectKey{Namespace: record.Namespace, Name: name}, &part); err != nil {
		return nil, err
	}
	owner := metav1.GetControllerOfNoCopy(&part)
	if part.Type != switchPartType || !ptr.Deref(part.Immutable, false) || owner == nil || owner.Kind != "Secret" || owner.APIVersion != "v1" || owner.Name != record.Name || owner.UID != record.UID || len(part.Data[switchSnapshotKey]) == 0 {
		return nil, fmt.Errorf("Secret %s/%s is not an immutable part of package switch %s", record.Namespace, name, record.Name)
	}
	return part.Data[switchSnapshotKey], nil
}

func readSwitchJournal(ctx context.Context, cli client.Client, component string) (*corev1.Secret, *switchJournal, error) {
	reader := k8s.NewManagedResourceWriter(cli).Reader
	record := new(corev1.Secret)
	err := reader.Get(ctx, client.ObjectKey{Namespace: packages.DataNamespace, Name: switchRecordName(component)}, record)
	if err != nil {
		return nil, nil, client.IgnoreNotFound(err)
	}
	h, err := decodeSwitchRecord(record, component)
	if err != nil {
		return nil, nil, err
	}
	if h.SnapshotDigest != "" {
		data := record.Data[switchSnapshotKey]
		if h.Parts > 0 {
			var parts bytes.Buffer
			for i := range h.Parts {
				part, err := readSwitchPart(ctx, reader, record, switchPartName(record, h.SnapshotDigest, i))
				if err != nil {
					return nil, nil, fmt.Errorf("read package switch part %d: %w", i, err)
				}
				parts.Write(part)
			}
			data = parts.Bytes()
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != h.SnapshotDigest {
			return nil, nil, fmt.Errorf("package switch %s snapshot checksum mismatch", record.Name)
		}
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, nil, err
		}
		defer func() { _ = zr.Close() }()
		var snapshot switchSnapshot
		// Read to EOF so gzip verifies the entire stream before accepting the snapshot.
		raw, err := io.ReadAll(zr)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return nil, nil, err
		}
		h.Sources, h.Changes = snapshot.Sources, snapshot.Changes
	}
	return record, &h.switchJournal, nil
}

func stageSwitchSnapshot(ctx context.Context, cli client.Client, record *corev1.Secret, h *switchJournalHeader) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(switchSnapshot{h.Sources, h.Changes}); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	h.SnapshotDigest = hex.EncodeToString(sum[:])
	if buf.Len() <= switchPartSize {
		return buf.Bytes(), nil
	}
	// Orphans from an interrupted preparation also belong to the record; GC
	// can remove them even when its published header never referenced them.
	owner := metav1.OwnerReference{APIVersion: "v1", Kind: "Secret", Name: record.Name, UID: record.UID, Controller: ptr.To(true)}
	for buf.Len() > 0 {
		data := buf.Next(switchPartSize)
		name := switchPartName(record, h.SnapshotDigest, h.Parts)
		part := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: record.Namespace, Name: name, OwnerReferences: []metav1.OwnerReference{owner}},
			Type:       switchPartType, Immutable: ptr.To(true), Data: map[string][]byte{switchSnapshotKey: data},
		}
		if err := cli.Create(ctx, part); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return nil, err
			}
			existing, err := readSwitchPart(ctx, k8s.NewManagedResourceWriter(cli).Reader, record, name)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(data, existing) {
				return nil, fmt.Errorf("package switch part %s does not match prepared data", name)
			}
		}
		h.Parts++
	}
	return nil, nil
}

func saveSwitchJournal(ctx context.Context, cli client.Client, record *corev1.Secret, j *switchJournal) error {
	h, err := decodeSwitchRecord(record, record.Annotations[v1.LabelComponent])
	if err != nil {
		return err
	}
	if j.Target != h.Target || j.TargetUID != h.TargetUID || j.Digest != h.Digest {
		return fmt.Errorf("package switch target identity changed")
	}
	was := h.Phase
	if !(was == "planning" && (j.Phase == "prepared" || j.Phase == "rollback") || was == "prepared" && (j.Phase == "committed" || j.Phase == "rollback")) {
		return fmt.Errorf("invalid package switch transition %s to %s", was, j.Phase)
	}
	h.switchJournal = *j
	h.Failure = truncateBytes(j.Failure, 4096)
	data := map[string][]byte{}
	if j.Phase == "prepared" {
		inline, err := stageSwitchSnapshot(ctx, cli, record, &h)
		if err != nil {
			return err
		}
		if inline != nil {
			data[switchSnapshotKey] = inline
		}
	}
	data[switchHeaderKey], err = json.Marshal(h)
	if err != nil {
		return err
	}
	// Commit the phase and snapshot pointer atomically. Only newly added data
	// crosses the wire; later phase changes leave the prepared snapshot in place.
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]string{"uid": string(record.UID), "resourceVersion": record.ResourceVersion},
		"data":     data,
	})
	if err != nil {
		return err
	}
	next := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: record.Namespace, Name: record.Name}}
	if err := cli.Patch(ctx, next, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return err
	}
	*record = *next
	return nil
}

func deleteSwitchJournal(ctx context.Context, cli client.Client, record *corev1.Secret) error {
	if _, err := decodeSwitchRecord(record, record.Annotations[v1.LabelComponent]); err != nil {
		return err
	}
	return client.IgnoreNotFound(cli.Delete(ctx, record, client.Preconditions{UID: &record.UID, ResourceVersion: &record.ResourceVersion}))
}
