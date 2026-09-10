package middlewareconfiguration

import (
	"context"
	"fmt"
	"github.com/harmonycloud/opensaola/internal/service/consts"
	"github.com/harmonycloud/opensaola/pkg/tools"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReconcileSSACompatibility renders currently referenced resources without running
// Actions or the inventory cleanup path. An empty template is not a deletion.
func ReconcileSSACompatibility(ctx context.Context, cli client.Client, owner client.Object, rendered tools.Quoter) error {
	configurations, err := GetTemplateParsedMiddlewareConfigurations(ctx, cli, consts.HandleActionUpdate, rendered)
	if err != nil {
		return err
	}
	renderedOwner, ok := rendered.(client.Object)
	if !ok {
		return fmt.Errorf("rendered Configuration owner has unsupported type %T", rendered)
	}
	for _, configuration := range configurations {
		if _, err = Handle(ctx, cli, renderedOwner, consts.HandleActionSSACompatibility, configuration); err != nil {
			return fmt.Errorf("Configuration %s SSA compatibility: %w", configuration.Name, err)
		}
	}
	return nil
}
