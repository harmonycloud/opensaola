package middlewareoperator

import (
	"context"
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/middlewareaction"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconcileSSACompatibility covers Configuration resources. Native MO Deployment
// and RBAC still use their existing non-SSA lifecycle and are not migrated here.
func ReconcileSSACompatibility(ctx context.Context, cli client.Client, owner *v1.MiddlewareOperator) error {
	if current, err := k8s.CheckSSACompatibility(ctx, cli, owner); err != nil || current {
		return err
	}
	rendered := owner.DeepCopy()
	if err := renderOperatorWithBaseline(ctx, cli, rendered); err != nil {
		return err
	}
	if err := middlewareaction.RenderPreActions(ctx, cli, rendered); err != nil {
		return fmt.Errorf("render SSA compatibility intent: %w", err)
	}
	return middlewareconfiguration.ReconcileSSACompatibility(ctx, cli, owner, rendered)
}
