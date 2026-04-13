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

package middlewareoperator

import (
	"testing"

	v1 "github.com/opensaola/opensaola/api/v1"
	"github.com/opensaola/opensaola/internal/service/consts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHandleResourceDeleteNoOperatorReturnsNil(t *testing.T) {
	mo := &v1.MiddlewareOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-mo",
			Namespace:   "default",
			Annotations: map[string]string{v1.LabelNoOperator: "true"},
		},
	}

	if err := HandleResource(nil, nil, consts.HandleActionDelete, mo); err != nil {
		t.Fatalf("expected nil error for nooperator delete, got %v", err)
	}
}
