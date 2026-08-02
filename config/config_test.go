package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	configv1 "github.com/go-kratos/gateway/api/gateway/config/v1"
)

func TestFileLoaderLoad(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	configData := []byte(`
name: test-gateway
version: v1
endpoints:
  - path: /health
    method: GET
    protocol: HTTP
    backends:
      - target: 127.0.0.1:8080
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

	loader := &FileLoader{confPath: configPath}
	cfg, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "test-gateway" || cfg.Version != "v1" {
		t.Fatalf("unexpected gateway metadata: %+v", cfg)
	}
	if len(cfg.Endpoints) != 1 {
		t.Fatalf("expected one endpoint, got %d", len(cfg.Endpoints))
	}
	endpoint := cfg.Endpoints[0]
	if endpoint.Path != "/health" || endpoint.Method != "GET" || endpoint.Protocol != configv1.Protocol_HTTP {
		t.Fatalf("unexpected endpoint: %+v", endpoint)
	}
	if len(endpoint.Backends) != 1 || endpoint.Backends[0].Target != "127.0.0.1:8080" {
		t.Fatalf("unexpected backends: %+v", endpoint.Backends)
	}
}
