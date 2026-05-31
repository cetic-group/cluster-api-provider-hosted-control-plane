# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Cluster API control plane provider for hosted control planes (HCP), enabling management of Kubernetes control
plane components as hosted services. The project implements a custom controller that manages the lifecycle of hosted
control planes including API server, controller manager, scheduler, and etcd components.

## Common Development Commands

This project uses [Task](https://taskfile.dev) as the build system. Key commands:

- `task` or `task build` - Build the project with multi-architecture support and image creation
- `task test` - Run all tests with coverage output to `_artifacts/cover.out`
- `task test path=<pkg>` - Run tests for specific package (e.g., `task test path=pkg/hostedcontrolplane`)
- `task lint` - Run golangci-lint with formatting and linting checks
- `task lint fix=true` - Run linting with automatic fixes
- `task format` - Format code using gofumpt and golines
- `task generate` - Generate deepcopy and conversion methods
- `task manifests` - Generate Kubernetes manifests (CRDs, RBAC, webhooks) using controller-gen and kustomize
- `task ci` - Run full CI pipeline (lint + test)
- `task clean` - Clean build and artifact directories
- `task check-diff` - Verify no uncommitted changes after generation
- `task compile` - Compile binaries for specific architectures
- `task tidy` - Run go mod tidy
- `task get-version` - Get the current version
- `task dev` - Local development with remote Kubernetes clusters using telepresence

## Architecture

### Core Components

- **API Types** (`api/v1alpha1/`): Custom resource definitions for HostedControlPlane and HostedControlPlaneTemplate
- **Controller** (`pkg/hostedcontrolplane/controller.go`): Main reconciliation logic for hosted control plane lifecycle
- **Reconcilers** (`pkg/reconcilers/`): Specialized reconcilers for different components:
    - `etcd_cluster/`: ETCD cluster management with backup/restore capabilities
    - `workload/`: Workload cluster components (RBAC, CoreDNS, kube-proxy)
    - `kubeconfig/`: Kubeconfig generation and management for cluster access
    - `certificates/`: Certificate management via cert-manager
    - `tlsroutes/`: Gateway API TLS route configuration
    - `infrastructure_cluster/`: Infrastructure cluster setup
    - `apiserverresources/`: API server service and deployment management
    - `alias/`: Type aliases for workload cluster clients
- **Operator** (`pkg/operator/`): Controller manager setup and configuration
- **Utilities** (`pkg/util/`): Common utilities for errors, logging, tracing

### Key Features

- **Multi-replica Control Plane**: Supports scaling control plane components
- **ETCD Management**: Includes backup/restore functionality with S3 storage
- **Gateway Integration**: Uses Gateway API for traffic routing
- **Certificate Management**: Integrates with cert-manager for TLS
- **Observability**: OpenTelemetry tracing integration
- **Cloud Integration**: S3 support for ETCD backups

## Code Style and Tools

- **Linting**: Uses golangci-lint with extensive rule set (see `.golangci.yaml`)
- **Formatting**: gofumpt + golines (120 char limit)
- **Import Aliases**: Strict import alias rules enforced (see `.golangci.yaml` importas section)
- **Generated Code**: Controller-gen for CRDs, conversion-gen for API conversions

## Testing

- Test files follow `*_test.go` convention
- Use `task test` to run all tests or `task test path=<package>` for specific packages
- Testing frameworks: Uses standard Go testing with gomega for assertions

## Build and Artifacts

- **Build Directory**: `build/` - Contains compiled binaries and generated manifests
- **Artifacts Directory**: `_artifacts/` - Contains test coverage reports and linting output
- **Multi-Architecture**: Supports amd64 and arm64 architectures
- **Container Images**: Automatically built during the build process
- **Manifests**: Generated using controller-gen and assembled with kustomize

## Development Environment

The project includes a `task dev` (telepresence) task for local development with remote Kubernetes clusters, allowing
local debugging while connected to a cluster environment. This enables running the controller locally while it interacts
with a remote Kubernetes cluster.

## Cetic fork notes (branch `cetic/v1.6.0`)

This is the `cetic-group/cluster-api-provider-hosted-control-plane` fork of upstream `teutonet`. Image is published to
the internal registry: `registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.6.0-cetic.N`.

### Certificate durations — DO NOT shorten

`pkg/hostedcontrolplane/controller.go` sets `caCertificatesDuration` / `certificatesDuration`. Upstream defaulted these
to **48h / 24h** (commit f3722502). With cert-manager those values mean the CA **rotates ~24h after cluster creation**,
and since neither etcd nor kube-apiserver hot-reload their trusted CA, pods started before vs after the rotation end up
on incompatible CAs → split-brain (etcd peer `tls: bad certificate`, apiserver `unknown certificate authority`,
`Error creating leases: context deadline exceeded`, CrashLoopBackOff). This bricks **every** cluster (CCKS and dbaas)
~1 day after creation. Both are set to **20 years** here (`20 * 365 * 24 * time.Hour`).

**Gotcha (fixed in cetic.3):** `pkg/reconcilers/certificates/reconciler.go` used `certificateRenewBefore: int32(50)`
applied via `WithRenewBeforePercentage`. With a 20-year duration, `duration * percentage` **overflows int64
nanoseconds** in cert-manager's webhook, which then rejects every CA (`renewBeforePercentage ... must result in a
renewBefore greater than 5m0s`), blocking all certificate reconciliation and new cluster creation. Use an **absolute**
`renewBefore` instead (field is `time.Duration` = `30 * 24 * time.Hour`, applied via `WithRenewBefore`). Max safe
duration with the percentage form would be <~2.9 years; the absolute form lifts that limit.

### Building/deploying without Task/buildah

`task`/`buildah` may be absent on the dev box. Manual build that matches the Taskfile:

```bash
CGO_ENABLED=0 GOARCH=amd64 GOOS=linux go build \
  -ldflags="-X=main.version=v1.6.0-cetic.N -s -w" -trimpath \
  -o build/manager-amd64 ./cmd/hosted-control-plane-controller/main.go
docker build -f Containerfile --build-arg manager=build/manager-amd64 \
  -t registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.6.0-cetic.N .
docker push registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.6.0-cetic.N
kubectl -n capi-hosted-control-plane-system set image \
  deploy/capi-hosted-control-plane-controller-manager manager=...:v1.6.0-cetic.N
```

### Migrating an already-broken cluster

The controller rewrites durations on reconcile, but to converge an existing cluster without a fresh split: patch the 3
CA Certificates with `spec.privateKey.rotationPolicy: Never` + `duration: 175200h` + `renewBefore: 720h` +
`renewBeforePercentage: null`, **verify the CA SubjectKeyIdentifier is unchanged before/after** (key reused = no split),
patch the leaf certs likewise, then rolling-restart etcd one-by-one (check quorum between each) and
`rollout restart` the apiserver/controller-manager/scheduler deployments.
