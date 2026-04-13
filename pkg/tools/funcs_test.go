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
	"fmt"
	"io"
	"k8s.io/apimachinery/pkg/util/yaml"
	"os"
	"testing"
)

func Test_toCue(t *testing.T) {
	open, err := os.Open("test.yaml")
	if err != nil {
		return
	}
	x, _ := io.ReadAll(open)
	mp := make(map[string]interface{})
	err = yaml.Unmarshal(x, &mp)
	if err != nil {
		panic(err)
	}

	type args struct {
		data interface{}
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test1",
			args: args{
				data: mp,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := toCue(tt.args.data)
			fmt.Println(s)
		})
	}
}
