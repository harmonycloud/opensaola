# Global Image Repository Design

## Goal

Use one global image registry and repository prefix for the OpenSaola manager and the CRD hook's kubectl image. Keep the fixed image names out of user configuration because `opensaola` and `kubectl` are product-owned constants.

## Values Contract

```yaml
global:
  registry: ghcr.io
  repository: harmonycloud

image:
  registry: ""
  repository: ""
  tag: ""
  pullPolicy: IfNotPresent

kubectl:
  image:
    registry: ""
    repository: ""
    tag: v1.30.14
    pullPolicy: IfNotPresent
```

`image.registry` and `image.repository` are optional manager-specific prefix overrides. `kubectl.image.registry` and `kubectl.image.repository` are optional kubectl-specific prefix overrides. They do not include the final image name.

## Resolution Rules

The manager image resolves as:

```text
<image.registry or global.registry>/<image.repository or global.repository>/opensaola:<tag>
```

The CRD hook image resolves as:

```text
<kubectl.image.registry or global.registry>/<kubectl.image.repository or global.repository>/kubectl:<tag>
```

Resolution precedence is component override, then global value. Rendering fails when either the resolved registry or repository prefix is empty. Helpers trim surrounding `/` characters before joining path segments so configuration does not produce duplicate separators.

The final image names `opensaola` and `kubectl` are constants in the Helm helpers and are not configurable values.

## Makefile Contract

The Makefile exposes matching global defaults and optional component prefix overrides. Local Helm deployment passes the global values into the chart instead of expanding two complete repositories independently.

Internal-registry synchronization resolves the same two complete image paths before copying manifests. The documented internal repository setting becomes a prefix such as `middleware`, producing:

```text
<internal-registry>/middleware/opensaola
<internal-registry>/middleware/kubectl
```

## Compatibility

The default rendered image references remain unchanged:

```text
ghcr.io/harmonycloud/opensaola
ghcr.io/harmonycloud/kubectl
```

Existing custom component repository values that include the final image name must remove the trailing `/opensaola` or `/kubectl`, because component repository values now represent prefixes. This behavior is documented in both English and Chinese chart instructions.

Existing tag, pull-policy, global image-pull-secret, multi-architecture synchronization, and component-specific registry override behavior remain unchanged.

## Validation

Automated tests cover:

1. Default rendering produces the two existing GHCR image references.
2. One global registry/repository override affects both images.
3. A component prefix override affects only that image while its fixed name remains appended.
4. Empty resolved registry or repository values fail closed.
5. `make helm-deploy` renders the intended global and component values without requiring Go during Makefile parsing.
6. `make test`, `make helm-check`, schema validation, and `git diff --check` pass.
