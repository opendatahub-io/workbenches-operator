## Stage 1: Fetch upstream component manifests
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest AS manifests
RUN microdnf install -y git && microdnf clean all
WORKDIR /workspace
COPY get_all_manifests.sh .
RUN bash get_all_manifests.sh

## Stage 2: Build the manager binary
FROM registry.access.redhat.com/ubi9/go-toolset:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

USER root
RUN CGO_ENABLED=1 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -tags strictfipsruntime -a -ldflags="-s -w" -o manager cmd/main.go

## Stage 3: Runtime
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=manifests /workspace/opt/manifests /opt/manifests
USER 65532:65532

ENTRYPOINT ["/manager"]
