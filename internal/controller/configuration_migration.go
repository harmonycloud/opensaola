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

package controller

import (
	"context"
	"time"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	"github.com/harmonycloud/opensaola/internal/service/middlewareoperator"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Initial informer events include settled and paused owners. Repair their GC
// metadata independently of lifecycle operations such as primary CR recovery.
func migrateConfigurationReferences(ctx context.Context, cli client.Client, recorder record.EventRecorder, owner client.Object) error {
	if !owner.GetDeletionTimestamp().IsZero() {
		return nil
	}
	var observed int64
	var state v1.State
	var paused bool
	switch m := owner.(type) {
	case *v1.Middleware:
		observed, state = m.Status.ObservedGeneration, m.Status.State
		paused = v1.IsMiddlewareReconcileWriteSuspended(m.Annotations, m.Status.Conditions)
	case *v1.MiddlewareOperator:
		if middlewareoperator.IsNoOperatorResource(m) {
			return nil
		}
		observed, state = m.Status.ObservedGeneration, m.Status.State
		paused = v1.IsReconcileSuspended(m.Annotations)
	}
	if observed == 0 || (!paused && (observed != owner.GetGeneration() || state == v1.StateUpdating)) {
		return nil
	}
	if err := middlewareconfiguration.MigrateConfigurationOwnerReferences(ctx, cli, owner); err != nil {
		recorder.Event(owner, "Warning", "ConfigurationMigrationFailed", err.Error())
		return err
	}
	return nil
}

func retryConfigurationMigration(result *ctrl.Result, reconcileErr *error) {
	if *reconcileErr == nil && result.IsZero() {
		result.RequeueAfter = time.Minute
	}
}
