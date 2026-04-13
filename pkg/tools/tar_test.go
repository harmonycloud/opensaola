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

package tools

import (
	"archive/tar"
	"bytes"
	"testing"
)

// makeTar creates a TAR archive in memory from the given entries.
// Each entry is a name/content pair. Set content to nil to create a directory entry.
func makeTar(t *testing.T, entries []struct {
	name    string
	content []byte
	isDir   bool
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if e.isDir {
			if err := tw.WriteHeader(&tar.Header{
				Name:     e.name,
				Typeflag: tar.TypeDir,
				Mode:     0o755,
			}); err != nil {
				t.Fatalf("write dir header: %v", err)
			}
		} else {
			if err := tw.WriteHeader(&tar.Header{
				Name:     e.name,
				Size:     int64(len(e.content)),
				Typeflag: tar.TypeReg,
				Mode:     0o644,
			}); err != nil {
				t.Fatalf("write header: %v", err)
			}
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write content: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func TestReadTarInfo_ValidTar(t *testing.T) {
	data := makeTar(t, []struct {
		name    string
		content []byte
		isDir   bool
	}{
		{name: "pkg/metadata.yaml", content: []byte("name: test")},
		{name: "pkg/baselines/default.yaml", content: []byte("version: 1")},
	})

	info, err := ReadTarInfo(data)
	if err != nil {
		t.Fatalf("ReadTarInfo returned error: %v", err)
	}
	if len(info.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(info.Files))
	}
	// First path component "pkg" should be stripped.
	if got := string(info.Files["metadata.yaml"]); got != "name: test" {
		t.Errorf("metadata.yaml content = %q, want %q", got, "name: test")
	}
	if got := string(info.Files["baselines/default.yaml"]); got != "version: 1" {
		t.Errorf("baselines/default.yaml content = %q, want %q", got, "version: 1")
	}
}

func TestReadTarInfo_EmptyTar(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	info, err := ReadTarInfo(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadTarInfo returned error: %v", err)
	}
	if len(info.Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(info.Files))
	}
}

func TestReadTarInfo_StripPrefix(t *testing.T) {
	data := makeTar(t, []struct {
		name    string
		content []byte
		isDir   bool
	}{
		{name: "mypackage/sub/dir/file.txt", content: []byte("hello")},
	})

	info, err := ReadTarInfo(data)
	if err != nil {
		t.Fatalf("ReadTarInfo returned error: %v", err)
	}
	// "mypackage" stripped, remainder is "sub/dir/file.txt".
	if _, ok := info.Files["sub/dir/file.txt"]; !ok {
		t.Errorf("expected key 'sub/dir/file.txt', got keys: %v", keysOf(info.Files))
	}
}

func TestReadTarInfo_SkipDirectories(t *testing.T) {
	data := makeTar(t, []struct {
		name    string
		content []byte
		isDir   bool
	}{
		{name: "pkg/", content: nil, isDir: true},
		{name: "pkg/subdir/", content: nil, isDir: true},
		{name: "pkg/file.txt", content: []byte("data")},
	})

	info, err := ReadTarInfo(data)
	if err != nil {
		t.Fatalf("ReadTarInfo returned error: %v", err)
	}
	// Only the regular file should be present; directories are skipped.
	if len(info.Files) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(info.Files), keysOf(info.Files))
	}
	if _, ok := info.Files["file.txt"]; !ok {
		t.Errorf("expected key 'file.txt', got keys: %v", keysOf(info.Files))
	}
}

// keysOf returns the keys of a map for diagnostic output.
func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
