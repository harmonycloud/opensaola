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
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestList(t *testing.T) {
	SetDataNamespace("lxt")
	type args struct {
		ctx context.Context
		cli client.Client
		opt Option
	}
	tests := []struct {
		name    string
		args    args
		want    []*Package
		wantErr bool
	}{
		{
			name: "xx",
			args: args{
				ctx: context.Background(),
				opt: Option{
					// LabelComponent: "opensaola",
					// LabelPackageVersion: "1.0.1",
				},
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := List(tt.args.ctx, tt.args.cli, tt.args.opt)
			if err == nil {
				t.Fatalf("List() expected error with nil client, got nil")
			}
		})
	}
}
