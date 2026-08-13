package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	configv1 "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/gateway/pkg/loader"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingSource struct {
	data    []byte
	started chan struct{}
	once    sync.Once
}

func (s *blockingSource) Name() string    { return "config_center" }
func (s *blockingSource) MainKey() string { return "config.yaml" }
func (s *blockingSource) Load(context.Context, string) ([]byte, error) {
	return append([]byte(nil), s.data...), nil
}
func (s *blockingSource) Watch(ctx context.Context, _ string, _ func(loader.Event)) error {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil
}

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

	configLoader, err := NewFileLoader(configPath, "")
	require.NoError(t, err)
	t.Cleanup(configLoader.Close)
	cfg, err := configLoader.Load(context.Background())
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

func TestSourceLoaderStartsWatchAfterHandlerRegistration(t *testing.T) {
	source := &blockingSource{
		data:    []byte("name: test-gateway\nversion: v1\n"),
		started: make(chan struct{}),
	}
	configLoader, err := NewSourceLoader(source, "")
	require.NoError(t, err)
	t.Cleanup(configLoader.Close)

	configLoader.lock.RLock()
	require.Nil(t, configLoader.watchCancel)
	configLoader.lock.RUnlock()

	configLoader.Watch(func(*configv1.Gateway) error { return nil })
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("source watch did not start after handler registration")
	}
}

func TestExecuteLoaderRollsBackEnvironmentAndSnapshotOnApplyFailure(t *testing.T) {
	const envKey = "GATEWAY_CONFIG_TEST_ENV"
	t.Setenv(envKey, "old")
	source := &blockingSource{
		data:    []byte("name: test-gateway\nversion: old\n"),
		started: make(chan struct{}),
	}
	configLoader, err := NewSourceLoader(source, "")
	require.NoError(t, err)
	t.Cleanup(configLoader.Close)
	configLoader.onChangeHandlers = []OnChange{
		func(*configv1.Gateway) error { return assert.AnError },
	}

	update := []byte("name: test-gateway\nversion: new\nenvs:\n  " + envKey + ": new\n")
	err = configLoader.executeLoader(update, map[string]string{})
	require.Error(t, err)
	assert.Equal(t, "old", os.Getenv(envKey))

	current, loadErr := configLoader.Load(context.Background())
	require.NoError(t, loadErr)
	assert.Equal(t, "old", current.GetVersion())
}

func TestApplyEnvsRollsBackPartialUpdate(t *testing.T) {
	const validKey = "A_GATEWAY_CONFIG_TEST_ENV"
	t.Setenv(validKey, "old")

	rollback, err := applyEnvs(&configv1.Gateway{Envs: map[string]string{
		validKey:        "new",
		"Z_INVALID=KEY": "invalid",
	}})

	require.Error(t, err)
	assert.Nil(t, rollback)
	assert.Equal(t, "old", os.Getenv(validKey))
}
