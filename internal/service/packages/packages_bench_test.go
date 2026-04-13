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

package packages

import (
	"bytes"
	"testing"
)

func BenchmarkCompressDecompress(b *testing.B) {
	input := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123456789"), 4096) // ~144KB

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		compressed, _, err := Compress(input)
		if err != nil {
			b.Fatal(err)
		}
		out, err := DeCompress(compressed)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != len(input) {
			b.Fatalf("size mismatch: got %d want %d", len(out), len(input))
		}
	}
}
