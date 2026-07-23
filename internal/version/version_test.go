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

package version_test

import (
	"testing"

	"github.com/harmonycloud/opensaola/internal/version"
)

func TestCurrent_Defaults(t *testing.T) {
	t.Parallel()

	want := version.Info{
		Version:   "dev",
		GitCommit: "unknown",
		BuildDate: "unknown",
	}
	if got := version.Current(); got != want {
		t.Fatalf("Current() = %#v, want %#v", got, want)
	}
}

func TestInfo_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info version.Info
		want string
	}{
		{
			name: "release metadata",
			info: version.Info{
				Version:   "v1.2.3",
				GitCommit: "0123456789abcdef0123456789abcdef01234567",
				BuildDate: "2026-07-14T08:09:10+08:00",
			},
			want: "Version: v1.2.3\n" +
				"Git Commit: 0123456789abcdef0123456789abcdef01234567\n" +
				"Build Date: 2026-07-14T08:09:10+08:00",
		},
		{
			name: "development metadata",
			info: version.Info{
				Version:   "dev",
				GitCommit: "unknown",
				BuildDate: "unknown",
			},
			want: "Version: dev\nGit Commit: unknown\nBuild Date: unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.info.String(); got != tt.want {
				t.Fatalf("Info.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
