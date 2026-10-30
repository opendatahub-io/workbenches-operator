## Build the manager binary
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

## Runtime
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

WORKDIR /
COPY --from=builder /workspace/manager .
COPY opt/manifests /opt/manifests
RUN microdnf update -y ca-certificates && microdnf clean all

USER 65532:65532

ENTRYPOINT ["/manager"]
