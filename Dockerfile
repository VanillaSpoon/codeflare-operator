# Build the manager binary
FROM registry.access.redhat.com/ubi8/go-toolset:1.20.10 as manager-builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the Go sources
COPY main.go main.go
COPY pkg/ pkg/


# Build
user root
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags strictfipsruntime -a -o manager main.go

# Build the webhook binary
FROM registry.access.redhat.com/ubi8/go-toolset:1.20.10 as webhook-builder
WORKDIR /workspace

COPY raycluster_oauth_webhook/ raycluster_oauth_webhook/

# Build
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags strictfipsruntime -a -o webhook raycluster_oauth_webhook/main.go

# Final image for manager
FROM registry.access.redhat.com/ubi8/ubi-minimal:8.8 as manager-image
WORKDIR /
COPY --from=manager-builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]

# Stage 4: Final image for the webhook
FROM registry.access.redhat.com/ubi8/ubi-minimal:8.8 as webhook-image
WORKDIR /
COPY --from=webhook-builder /workspace/webhook .
RUN chmod +x /usr/local/bin/webhook
USER 65532:65532
ENTRYPOINT ["/webhook"]

