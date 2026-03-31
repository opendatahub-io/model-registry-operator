# Contributing to Model Registry Operator

## Prerequisites

- Go 1.25+
- Docker or Podman
- Access to a Kubernetes cluster (KIND for local dev, or OpenShift)
- `kubectl` (or `oc` for OpenShift)

## Building

```sh
make build          # Build the manager binary (runs code generation, fmt, vet)
make docker-build   # Build the container image
```

## Testing

```sh
make test                           # Full test suite (generates manifests, runs envtest)
go test ./...                       # Faster iteration (skips code generation)
ginkgo run -v internal/controller   # Run controller tests only
ginkgo run -v api/v1beta1           # Run webhook tests only
ginkgo run -v internal/migration    # Run migration tests only
```

Tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/tools/setup-envtest) (in-process API server). `make test` downloads envtest binaries automatically; `go test` requires them in `bin/k8s/`.

## Linting

```sh
make lint   # Run golangci-lint (downloads binary on first run)
make fmt    # Run go fmt
make vet    # Run go vet
```

### Pre-commit hooks (optional)

```sh
pip install pre-commit
pre-commit install
```

This runs `make fmt`, `make vet`, and `make lint` automatically on commit.

## Code Generation

Run these after modifying API types in `api/`:

```sh
make manifests   # Regenerate CRDs, RBAC, and webhook configs
make generate    # Regenerate DeepCopy and conversion code
```

Always commit generated changes — CI checks for uncommitted diffs.

## Local Development

Run the operator outside the cluster:

```sh
make install   # Install CRDs
make run       # Run the controller locally (webhooks disabled by default)
```

## Dev Cluster Testing

See [AGENTS.md](AGENTS.md#dev-cluster-testing) for instructions on building, pushing, and patching images on an OpenShift dev cluster.

## Submitting Changes

- Keep diffs minimal — only modify files relevant to the task
- Run `make lint` before committing
- Run `go mod tidy` if you changed dependencies
- Follow [Conventional Commits](https://www.conventionalcommits.org/) (e.g. `feat(catalog): ...`, `fix: ...`)
- Use the [pull request template](.github/pull_request_template.md)
- Do not commit secrets or credentials
