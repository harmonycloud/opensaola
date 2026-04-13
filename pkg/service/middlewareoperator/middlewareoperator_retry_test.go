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
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/OpenSaola/opensaola/api/v1"
	"github.com/OpenSaola/opensaola/pkg/resource/logger"
	"github.com/OpenSaola/opensaola/pkg/service/consts"
	"github.com/OpenSaola/opensaola/pkg/service/status"
	"github.com/rs/zerolog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	logger.Initialize(zerolog.ErrorLevel)
}

func TestMarkPackageUnavailable_WithinTimeout(t *testing.T) {
	conditions := []metav1.Condition{}
	updating := status.GetCondition(nil, &conditions, v1.CondTypeUpdating)

	err := markPackageUnavailable(updating, "v1", "not-found", 1)
	if !errors.Is(err, consts.ErrPackageNotReady) {
		t.Fatalf("expected ErrPackageNotReady, got %v", err)
	}
	if updating.Status != metav1.ConditionUnknown {
		t.Fatalf("expected condition unknown, got %s", updating.Status)
	}
	if !strings.Contains(updating.Message, "version=v1") {
		t.Fatalf("expected version marker in message, got %q", updating.Message)
	}
}

func TestMarkPackageUnavailable_ExceedTimeout(t *testing.T) {
	conditions := []metav1.Condition{}
	updating := status.GetCondition(nil, &conditions, v1.CondTypeUpdating)
	updating.Status = metav1.ConditionUnknown
	updating.LastTransitionTime = metav1.NewTime(time.Now().Add(-defaultUpgradePackageUnavailableTimeout - time.Second))
	updating.Message = packageUnavailableMessage("v1", "not-found")

	err := markPackageUnavailable(updating, "v1", "still-not-found", 1)
	if !errors.Is(err, consts.ErrPackageUnavailableExceeded) {
		t.Fatalf("expected ErrPackageUnavailableExceeded, got %v", err)
	}
}

func TestMarkPackageUnavailable_ResetWindowOnVersionChange(t *testing.T) {
	conditions := []metav1.Condition{}
	updating := status.GetCondition(nil, &conditions, v1.CondTypeUpdating)
	updating.Status = metav1.ConditionUnknown
	updating.LastTransitionTime = metav1.NewTime(time.Now().Add(-defaultUpgradePackageUnavailableTimeout - time.Second))
	updating.Message = packageUnavailableMessage("old", "not-found")

	err := markPackageUnavailable(updating, "new", "not-found", 1)
	if !errors.Is(err, consts.ErrPackageNotReady) {
		t.Fatalf("expected ErrPackageNotReady after target version change, got %v", err)
	}
	if !strings.Contains(updating.Message, "version=new") {
		t.Fatalf("expected message reset to new version, got %q", updating.Message)
	}
}
