# saucegen

saucegen is a CLI tool that generates Kubernetes manifests and configuration schemas directly from Go structs. It is designed to be used as a source-of-truth generator for Flux/GitOps deployment repositories.

## Features

- Secure-by-Default: All struct fields are treated as secrets (ExternalSecret) unless explicitly tagged as public (ConfigMap).
- Kustomize Integration: Generates a complete Kustomize base including `kustomization.yaml`.
- Schema Export: Produces a `schema.yaml` metadata file for external visibility into the app's configuration.
- Go Toolchain Compatible: Works seamlessly with `go:generate`.

## Installation

```bash
go install github.com/pangobit/saucegen@latest
```

## Usage

```bash
saucegen generate k8s [package_path] [flags]
```

### Flags

- `--struct`: Name of the configuration struct (default: "AppConfig")
- `--name`: Base name for generated resources (default: "app")
- `--namespace`: Target Kubernetes namespace (default: "default")
- `--secret-store`: Name of the SecretStore for ExternalSecrets (default: "secretsauce")
- `--values`: Path to a local config.yaml to extract default values for public fields
- `--output-dir`: Directory where manifests will be written (default: "./k8s/base")

## Example

### 1. Annotate your Go struct

Use the `sauce:"public"` tag to indicate fields that are safe to expose in a ConfigMap.

```go
type AppConfig struct {
    DatabaseURL string `mapstructure:"db_url"`                 // Default: Secret
    LogLevel    string `mapstructure:"log_level" sauce:"public"` // Public
}
```

### 2. Add a generate directive

```go
//go:generate go run github.com/pangobit/saucegen generate k8s . --name my-app --values config.yaml
```

### 3. Run generation

```bash
go generate ./...
```

This will produce a `k8s/base/` directory containing the ConfigMap, ExternalSecret, Kustomization, and Schema files.
