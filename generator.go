package main

import (
	"encoding/json"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

// Config holds the configuration for the manifest generation process.
// It defines the input package, the target struct, and where to output the results.
type Config struct {
	// PackagePath is the path to the Go package containing the config struct.
	PackagePath string
	// StructName is the name of the target AppConfig struct within the package.
	StructName string
	// AppName is the base name for the generated Kubernetes resources.
	AppName string
	// Namespace is the Kubernetes namespace where the resources will be deployed.
	Namespace string
	// SecretStore is the name of the Kubernetes SecretStore that will provide the secrets.
	SecretStore string
	// ValuesFile is the path to a local YAML file providing default values for public fields.
	ValuesFile string
	// OutputDir is the directory where the generated manifests and schema will be saved.
	OutputDir string
	// ConfigAsSecret indicates if public fields should be generated as ExternalSecrets.
	ConfigAsSecret bool
	// ConfigStore is the name of the SecretStore for public configuration.
	ConfigStore        string
	SecretKeySeparator string
}

// Generator handles the parsing of Go structs and emission of K8s manifests.
// It uses the Go compiler's type information to derive the infrastructure bundle.
type Generator struct {
	cfg Config
}

// NewGenerator creates a new Generator instance with the provided config.
func NewGenerator(cfg Config) *Generator {
	return &Generator{cfg: cfg}
}

// FieldInfo represents metadata extracted from a single struct field.
// It maps the field's position in the struct tree to its K8s intent.
type FieldInfo struct {
	// Path is the dotted notation path to the field (e.g. "server.port").
	Path string
	// Name is the derived name of the field, usually from mapstructure tags.
	Name string
	// IsSecret indicates if the field should be treated as sensitive.
	// Defaults to true unless tagged with sauce:"public".
	IsSecret bool
	// Value is the resolved default value for public fields.
	Value interface{}
}

// Generate executes the full parsing and emission workflow.
func (g *Generator) Generate() error {
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo,
	}, g.cfg.PackagePath)
	if err != nil {
		return fmt.Errorf("failed to load package: %w", err)
	}

	if len(pkgs) == 0 {
		return fmt.Errorf("no packages found for %s", g.cfg.PackagePath)
	}

	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return fmt.Errorf("package errors: %v", pkg.Errors)
	}

	obj := pkg.Types.Scope().Lookup(g.cfg.StructName)
	if obj == nil {
		return fmt.Errorf("struct %s not found in package %s", g.cfg.StructName, pkg.PkgPath)
	}

	structType, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return fmt.Errorf("%s is not a struct", g.cfg.StructName)
	}

	var values map[string]interface{}
	if g.cfg.ValuesFile != "" {
		data, err := os.ReadFile(g.cfg.ValuesFile)
		if err != nil {
			return fmt.Errorf("failed to read values file %s: %w", g.cfg.ValuesFile, err)
		}
		if err := yaml.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("failed to unmarshal values file %s: %w", g.cfg.ValuesFile, err)
		}
	}

	fields := g.walkStruct(structType, "", values)

	return g.writeManifests(fields)
}

// walkStruct recursively traverses a struct type to extract configuration metadata.
// It follows mapstructure tags for naming and uses the sauce tag to identify public fields.
// The secure-by-default logic is applied here: fields are secrets unless explicitly marked public.
func (g *Generator) walkStruct(t *types.Struct, prefix string, values map[string]interface{}) []FieldInfo {
	var fields []FieldInfo

	for i := 0; i < t.NumFields(); i++ {
		f := t.Field(i)
		tagStr := t.Tag(i)

		msTag := reflect.StructTag(tagStr).Get("mapstructure")
		sauceTag := reflect.StructTag(tagStr).Get("sauce")
		defaultTag := reflect.StructTag(tagStr).Get("default")

		name := f.Name()
		squash := false
		if msTag != "" {
			parts := strings.Split(msTag, ",")
			if parts[0] != "" {
				name = parts[0]
			}
			for _, p := range parts[1:] {
				if p == "squash" {
					squash = true
				}
			}
		}

		isSecret := sauceTag != "public"

		currentPath := name
		if prefix != "" && !squash {
			currentPath = prefix + "." + name
		} else if prefix != "" && squash {
			currentPath = prefix
		}

		underlying := f.Type().Underlying()
		if ptr, ok := underlying.(*types.Pointer); ok {
			underlying = ptr.Elem().Underlying()
		}

		if nestedStruct, ok := underlying.(*types.Struct); ok {
			nestedPrefix := currentPath
			if squash {
				nestedPrefix = prefix
			}
			fields = append(fields, g.walkStruct(nestedStruct, nestedPrefix, values)...)
			continue
		}

		var val interface{}
		if !isSecret {
			val = g.lookupValue(currentPath, values)
			if val == nil && defaultTag != "" {
				val = defaultTag
			}
		}

		fields = append(fields, FieldInfo{
			Path:     currentPath,
			Name:     name,
			IsSecret: isSecret,
			Value:    val,
		})
	}

	return fields
}

// lookupValue retrieves a value from a nested map using a dotted path (e.g., "server.port").
// It returns nil if any part of the path is missing.
func (g *Generator) lookupValue(path string, values map[string]interface{}) interface{} {
	parts := strings.Split(path, ".")
	var current interface{} = values
	for _, part := range parts {
		if m, ok := current.(map[string]interface{}); ok {
			current = m[part]
		} else {
			return nil
		}
	}
	return current
}

// writeManifests orchestrates the creation of the infrastructure bundle.
func (g *Generator) writeManifests(fields []FieldInfo) error {
	if err := os.MkdirAll(g.cfg.OutputDir, 0755); err != nil {
		return err
	}

	resources := []string{}
	bootstrap := make(map[string]interface{})

	var esSecretsData []interface{}
	for _, f := range fields {
		if f.IsSecret {
			key := strings.ToUpper(strings.ReplaceAll(f.Path, ".", "_"))
			remoteKey := strings.ToUpper(strings.ReplaceAll(g.cfg.AppName, "-", "_") + g.cfg.SecretKeySeparator + key)
			esSecretsData = append(esSecretsData, map[string]interface{}{
				"secretKey": key,
				"remoteRef": map[string]string{
					"key": remoteKey,
				},
			})
		}
	}

	if len(esSecretsData) > 0 {
		esSecrets := map[string]interface{}{
			"apiVersion": "external-secrets.io/v1",
			"kind":       "ExternalSecret",
			"metadata": map[string]string{
				"name":      g.cfg.AppName + "-secrets",
				"namespace": g.cfg.Namespace,
			},
			"spec": map[string]interface{}{
				"refreshInterval": "1h",
				"secretStoreRef": map[string]string{
					"name": g.cfg.SecretStore,
					"kind": "SecretStore",
				},
				"target": map[string]string{
					"name":           g.cfg.AppName + "-secrets",
					"creationPolicy": "Owner",
				},
				"data": esSecretsData,
			},
		}

		if err := g.writeYAML(filepath.Join(g.cfg.OutputDir, "external-secret-secrets.yaml"), esSecrets); err != nil {
			return err
		}
		resources = append(resources, "external-secret-secrets.yaml")
	}

	var configKeys []FieldInfo
	for _, f := range fields {
		if !f.IsSecret {
			configKeys = append(configKeys, f)
			if f.Value != nil {
				bootstrap[strings.ToUpper(strings.ReplaceAll(f.Path, ".", "_"))] = f.Value
			}
		}
	}

	if len(configKeys) > 0 {
		if g.cfg.ConfigAsSecret {
			var esConfigData []interface{}
			for _, f := range configKeys {
				key := strings.ToUpper(strings.ReplaceAll(f.Path, ".", "_"))
				esConfigData = append(esConfigData, map[string]interface{}{
					"secretKey": key,
					"remoteRef": map[string]string{
						"key": key,
					},
				})
			}
			esConfig := map[string]interface{}{
				"apiVersion": "external-secrets.io/v1",
				"kind":       "ExternalSecret",
				"metadata": map[string]string{
					"name":      g.cfg.AppName + "-config",
					"namespace": g.cfg.Namespace,
				},
				"spec": map[string]interface{}{
					"refreshInterval": "1h",
					"secretStoreRef": map[string]string{
						"name": g.cfg.ConfigStore,
						"kind": "SecretStore",
					},
					"target": map[string]string{
						"name":           g.cfg.AppName + "-config",
						"creationPolicy": "Owner",
					},
					"data": esConfigData,
				},
			}

			if err := g.writeYAML(filepath.Join(g.cfg.OutputDir, "external-secret-config.yaml"), esConfig); err != nil {
				return err
			}
			resources = append(resources, "external-secret-config.yaml")
		} else {
			cm := map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]string{
					"name":      g.cfg.AppName + "-config",
					"namespace": g.cfg.Namespace,
				},
				"data": make(map[string]string),
			}

			cmData := cm["data"].(map[string]string)
			for _, f := range configKeys {
				key := strings.ToUpper(strings.ReplaceAll(f.Path, ".", "_"))
				if f.Value != nil {
					cmData[key] = fmt.Sprintf("%v", f.Value)
				} else {
					cmData[key] = ""
				}
			}
			if err := g.writeYAML(filepath.Join(g.cfg.OutputDir, "config-map.yaml"), cm); err != nil {
				return err
			}
			resources = append(resources, "config-map.yaml")
		}
	}

	k := map[string]interface{}{
		"resources": resources,
	}
	if err := g.writeYAML(filepath.Join(g.cfg.OutputDir, "kustomization.yaml"), k); err != nil {
		return err
	}

	schema := map[string]interface{}{
		"app":  g.cfg.AppName,
		"keys": []map[string]interface{}{},
	}
	sKeys := schema["keys"].([]map[string]interface{})
	for _, f := range fields {
		key := strings.ToUpper(strings.ReplaceAll(f.Path, ".", "_"))
		sKeys = append(sKeys, map[string]interface{}{
			"path":   f.Path,
			"key":    key,
			"secret": f.IsSecret,
		})
	}
	schema["keys"] = sKeys
	if err := g.writeYAML(filepath.Join(g.cfg.OutputDir, "schema.yaml"), schema); err != nil {
		return err
	}

	if len(bootstrap) > 0 {
		bData, err := json.MarshalIndent(bootstrap, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(g.cfg.OutputDir, "bootstrap.json"), bData, 0644); err != nil {
			return err
		}
	}

	defaults := make(map[string]interface{})
	for _, f := range fields {
		key := strings.ToUpper(strings.ReplaceAll(f.Path, ".", "_"))
		if f.IsSecret {
			defaults[key] = ""
		} else {
			if f.Value != nil {
				defaults[key] = f.Value
			} else {
				defaults[key] = ""
			}
		}
	}
	if err := g.writeYAML(filepath.Join(g.cfg.OutputDir, "defaults.yaml"), defaults); err != nil {
		return err
	}

	return nil
}

// writeYAML serializes a data structure to a file with consistent K8s-style formatting.
// It ensures 2-space indentation and clean output.
func (g *Generator) writeYAML(path string, data interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	return enc.Encode(data)
}
