package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLookupValue verifies the logic for retrieving values from nested YAML-derived maps.
// It covers top-level keys, nested keys, missing keys, and empty value maps.
func TestLookupValue(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		values   map[string]interface{}
		expected interface{}
	}{
		{
			name: "Top level key",
			path: "debug",
			values: map[string]interface{}{
				"debug": true,
			},
			expected: true,
		},
		{
			name: "Nested key",
			path: "server.port",
			values: map[string]interface{}{
				"server": map[string]interface{}{
					"port": "8080",
				},
			},
			expected: "8080",
		},
		{
			name: "Missing key",
			path: "database.host",
			values: map[string]interface{}{
				"server": map[string]interface{}{
					"port": "8080",
				},
			},
			expected: nil,
		},
		{
			name:     "Empty values",
			path:     "any",
			values:   nil,
			expected: nil,
		},
	}

	g := &Generator{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := g.lookupValue(tt.path, tt.values)
			if got != tt.expected {
				t.Errorf("lookupValue(%s) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

// TestWriteManifests verifies that the Generator correctly creates the infrastructure bundle.
func TestWriteManifests(t *testing.T) {
	tmpDir := t.TempDir()
	g := NewGenerator(Config{
		AppName:            "test-app",
		SecretKeySeparator: "-",
		Namespace:          "test-ns",
		SecretStore:        "test-store",
		OutputDir:          tmpDir,
	})

	fields := []FieldInfo{
		{Path: "server.port", Name: "port", IsSecret: false, Value: "8080"},
		{Path: "database.password", Name: "password", IsSecret: true},
	}

	err := g.writeManifests(fields)
	if err != nil {
		t.Fatalf("writeManifests failed: %v", err)
	}

	cmData, err := os.ReadFile(filepath.Join(tmpDir, "config-map.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cm map[string]interface{}
	if err := yaml.Unmarshal(cmData, &cm); err != nil {
		t.Fatal(err)
	}

	data := cm["data"].(map[string]interface{})
	if data["SERVER_PORT"] != "8080" {
		t.Errorf("expected SERVER_PORT to be 8080, got %v", data["SERVER_PORT"])
	}

	esData, err := os.ReadFile(filepath.Join(tmpDir, "external-secret-secrets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var es map[string]interface{}
	if err := yaml.Unmarshal(esData, &es); err != nil {
		t.Fatal(err)
	}

	spec := es["spec"].(map[string]interface{})
	target := spec["target"].(map[string]interface{})
	if target["name"] != "test-app-secrets" {
		t.Errorf("expected target name test-app-secrets, got %v", target["name"])
	}

	secretData := spec["data"].([]interface{})
	found := false
	for _, d := range secretData {
		m := d.(map[string]interface{})
		if m["secretKey"] == "DATABASE_PASSWORD" {
			found = true
			ref := m["remoteRef"].(map[string]interface{})
			if ref["key"] != "TEST_APP-DATABASE_PASSWORD" {
				t.Errorf("unexpected remoteRef key: %v", ref["key"])
			}
		}
	}
	if !found {
		t.Error("DATABASE_PASSWORD not found in ExternalSecret")
	}
}
