# Build the manager binary
FROM golang:1.24.1 AS builder

WORKDIR /workspace

# Copy the go source
COPY cmd/ cmd/
COPY api/ api/
COPY internal/controller/ internal/controller/
COPY pkg/ pkg/


# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

RUN #sleep 600


# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go env -w GOPROXY=https://goproxy.cn,direct

RUN go mod download


# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=linux go build -a -o manager ./cmd

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM alpine:3.20

WORKDIR /
COPY --from=builder /workspace/manager .
COPY pkg/config pkg/config

RUN sed -i 's#https\?://dl-cdn.alpinelinux.org/alpine#https://mirrors.tuna.tsinghua.edu.cn/alpine#g' /etc/apk/repositories
RUN apk update
RUN apk add --no-cache tzdata
RUN apk add --no-cache jq
RUN apk add --no-cache
RUN apk add --no-cache curl

RUN echo "系统架构: $(uname -m)"

# 根据 TARGETPLATFORM 和 TARGETARCH 复制相应的 kubectl 二进制文件
#COPY build /home/build
RUN if [ "$(uname -m)" = "x86_64" ]; then \
        curl -LO "https://dl.k8s.io/release/v1.21.0/bin/linux/amd64/kubectl"; \
    elif [ "$(uname -m)" = "aarch64" ]; then \
        curl -LO "https://dl.k8s.io/release/v1.21.0/bin/linux/arm64/kubectl"; \
    else \
        echo "Unsupported architecture: $(uname -m)"; exit 1; \
    fi && \
    mv kubectl /usr/bin/kubectl && \
    chmod +x /usr/bin/kubectl

RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S -G nonroot nonroot
USER 65532:65532

ENTRYPOINT ["/manager"]
