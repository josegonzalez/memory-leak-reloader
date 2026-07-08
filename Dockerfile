# syntax=docker/dockerfile:1

# BIN_SOURCE selects how the manager binary is obtained:
#   build    - compile from source in this image (default; used by release)
#   prebuilt - copy a binary already compiled on the host (used by CI, which
#              builds each arch natively on its own runner with a cached Go
#              toolchain, avoiding QEMU and a cold in-image recompile)
ARG BIN_SOURCE=build

# Build stage. Pin to the native build platform and cross-compile (CGO is off,
# GOARCH is set below) so the arm64 image is not compiled under slow QEMU
# emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src

# Cache dependencies.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Build the static binary.
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/manager ./cmd

# Prebuilt path: use a manager binary already compiled on the host.
FROM scratch AS prebuilt
COPY manager /out/manager

# Resolve the selected binary source.
FROM ${BIN_SOURCE} AS bin

# Runtime stage: distroless nonroot.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=bin /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
