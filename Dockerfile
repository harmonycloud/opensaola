# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.26.1 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /workspace

# Cache dependencies - this layer is invalidated only when go.mod/go.sum change
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
COPY pkg/ pkg/

# Build with optimizations
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.gitCommit=${GIT_COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o manager ./cmd

# Download kubectl in builder stage to keep runtime image clean
ARG KUBECTL_VERSION=v1.30.0
RUN curl -LO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${TARGETOS}/${TARGETARCH}/kubectl" \
    && curl -LO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${TARGETOS}/${TARGETARCH}/kubectl.sha256" \
    && echo "$(cat kubectl.sha256)  kubectl" | sha256sum -c - \
    && chmod 0555 kubectl \
    && rm kubectl.sha256

# Runtime image - minimal Alpine with only necessary tools
FROM alpine:3.20

LABEL org.opencontainers.image.source="https://github.com/opensaola/opensaola" \
      org.opencontainers.image.title="opensaola" \
      org.opencontainers.image.description="OpenSaola - Kubernetes Middleware Lifecycle Operator" \
      org.opencontainers.image.licenses="Apache-2.0"

# Install runtime dependencies and create non-root user in a single layer
RUN sed -i 's#https\?://dl-cdn.alpinelinux.org/alpine#https://mirrors.tuna.tsinghua.edu.cn/alpine#g' /etc/apk/repositories \
    && apk add --no-cache tzdata curl jq \
    && rm -rf /var/cache/apk/* \
    && addgroup -g 65532 -S nonroot \
    && adduser -u 65532 -S -G nonroot nonroot

WORKDIR /

# Copy binaries from builder
COPY --from=builder --chown=65532:65532 --chmod=0555 /workspace/manager .
COPY --from=builder --chown=65532:65532 --chmod=0555 /workspace/kubectl /usr/bin/kubectl

# Copy only the runtime config file (not dev config)
COPY --chown=65532:65532 pkg/config/config.yaml /pkg/config/config.yaml

USER 65532:65532

ENTRYPOINT ["/manager"]
