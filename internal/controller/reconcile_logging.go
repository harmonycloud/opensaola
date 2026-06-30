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

package controller

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var reconcileLogSequence atomic.Uint64

func withReconcileLogger(ctx context.Context, controllerName, resourceKind string, req ctrl.Request) context.Context {
	correlationID := reconcileCorrelationID(controllerName, req.NamespacedName)
	reconcileID := newReconcileID(correlationID)
	logger := log.FromContext(ctx).WithValues(
		"controller", controllerName,
		"resourceKind", resourceKind,
		"namespace", req.Namespace,
		"name", req.Name,
		"correlationID", correlationID,
		"reconcileID", reconcileID,
	)
	return log.IntoContext(ctx, logger)
}

func reconcileCorrelationID(controllerName string, name types.NamespacedName) string {
	if name.Namespace == "" {
		return fmt.Sprintf("%s/%s", controllerName, name.Name)
	}
	return fmt.Sprintf("%s/%s/%s", controllerName, name.Namespace, name.Name)
}

func newReconcileID(correlationID string) string {
	return fmt.Sprintf("%s/%d/%d", correlationID, time.Now().UTC().UnixNano(), reconcileLogSequence.Add(1))
}
