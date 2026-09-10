package middleware

import (
	"context"
	"fmt"

	v1 "github.com/harmonycloud/opensaola/api/v1"
	"github.com/harmonycloud/opensaola/internal/k8s"
	"github.com/harmonycloud/opensaola/internal/service/customresource"
	"github.com/harmonycloud/opensaola/internal/service/middlewareaction"
	"github.com/harmonycloud/opensaola/internal/service/middlewareconfiguration"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconcileSSACompatibility runs even when observedGeneration is current. It must
// remain after pause/resume validation and must not run imperative PreActions.
func ReconcileSSACompatibility(ctx context.Context, cli client.Client, owner *v1.Middleware) error {
	if current, err := k8s.CheckSSACompatibility(ctx, cli, owner); err != nil || current {
		return err
	}

	rendered := owner.DeepCopy()
	if err := renderMiddlewareWithBaseline(ctx, cli, rendered); err != nil {
		return err
	}
	if err := middlewareaction.RenderPreActions(ctx, cli, rendered); err != nil {
		return fmt.Errorf("render SSA compatibility intent: %w", err)
	}
	cr, err := customresource.GetNeedPublishCustomResource(ctx, cli, rendered)
	if err != nil {
		return err
	}
	if err = ApplyReconcileOverrides(rendered, cr); err != nil {
		return err
	}
	if err = ctrl.SetControllerReference(owner, cr, cli.Scheme()); err != nil {
		return err
	}
	if err = middlewareconfiguration.ReconcileSSACompatibility(ctx, cli, owner, rendered); err != nil {
		return err
	}
	return k8s.NewManagedResourceWriter(cli).Reconcile(ctx, owner, cr, "", true)
}
