package loader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-kratos/gateway/constants"
	configv1 "github.com/lens077/config-center/api/config/v1"
	"github.com/lens077/config-center/api/config/v1/configv1connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type getKeyCall struct {
	request *configv1.GetKeyRequest
	header  http.Header
}

type watchKeysCall struct {
	request *configv1.WatchKeysRequest
	header  http.Header
}

type fakeConfigService struct {
	configv1connect.UnimplementedConfigServiceHandler

	mu       sync.Mutex
	entries  map[string]*configv1.ConfigEntry
	getCalls []getKeyCall
	watches  []watchKeysCall
	watch    func(context.Context, int, *connect.Request[configv1.WatchKeysRequest], *connect.ServerStream[configv1.WatchKeysResponse]) error
}

func (f *fakeConfigService) GetKey(
	_ context.Context,
	req *connect.Request[configv1.GetKeyRequest],
) (*connect.Response[configv1.GetKeyResponse], error) {
	f.mu.Lock()
	f.getCalls = append(f.getCalls, getKeyCall{
		request: &configv1.GetKeyRequest{
			Namespace: req.Msg.GetNamespace(), Environment: req.Msg.GetEnvironment(), Key: req.Msg.GetKey(),
		},
		header: req.Header().Clone(),
	})
	entry := f.entries[configID(req.Msg.GetNamespace(), req.Msg.GetEnvironment(), req.Msg.GetKey())]
	f.mu.Unlock()
	if entry == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("config not found"))
	}
	return connect.NewResponse(&configv1.GetKeyResponse{Entry: entry}), nil
}

func (f *fakeConfigService) WatchKeys(
	ctx context.Context,
	req *connect.Request[configv1.WatchKeysRequest],
	stream *connect.ServerStream[configv1.WatchKeysResponse],
) error {
	f.mu.Lock()
	callIndex := len(f.watches)
	f.watches = append(f.watches, watchKeysCall{
		request: &configv1.WatchKeysRequest{
			Namespace: req.Msg.GetNamespace(), Environment: req.Msg.GetEnvironment(), Keys: append([]string(nil), req.Msg.GetKeys()...),
		},
		header: req.Header().Clone(),
	})
	watch := f.watch
	f.mu.Unlock()
	if watch == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("watch not configured"))
	}
	return watch(ctx, callIndex, req, stream)
}

func (f *fakeConfigService) calls() ([]getKeyCall, []watchKeysCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]getKeyCall(nil), f.getCalls...), append([]watchKeysCall(nil), f.watches...)
}

func configID(namespace, environment, key string) string {
	return namespace + "/" + environment + "/" + key
}

func startFakeConfigService(t *testing.T, service *fakeConfigService) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(configv1connect.NewConfigServiceHandler(service))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func configCenterSelector(t *testing.T, address, namespace, environment, key string) string {
	t.Helper()
	selector := filepath.Join(t.TempDir(), "source.yaml")
	contents := fmt.Sprintf(`type: config_center
config_center:
  address: %s
  namespace: %s
  environment: %s
  key: %s
  service_token: test-service-token
`, address, namespace, environment, key)
	require.NoError(t, os.WriteFile(selector, []byte(contents), 0o600))
	return selector
}

type eventSource struct {
	event Event
}

func (s eventSource) Name() string    { return "config_center" }
func (s eventSource) MainKey() string { return "config.yaml" }
func (s eventSource) Load(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (s eventSource) Watch(_ context.Context, _ string, onEvent func(Event)) error {
	onEvent(s.event)
	return nil
}

func clearSourceEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		constants.EnvConfigSourceFile,
		constants.EnvConfigSource,
		constants.EnvConfigFile,
	} {
		t.Setenv(key, "")
	}
}

func TestNewSourceMissingSelectorFails(t *testing.T) {
	clearSourceEnv(t)

	source, err := NewSource()
	assert.Nil(t, source)
	require.Error(t, err)
	assert.Contains(t, err.Error(), constants.EnvConfigSourceFile)
}

func TestNewSourceRejectsConsul(t *testing.T) {
	clearSourceEnv(t)
	t.Setenv(constants.EnvConfigSource, "consul")

	source, err := NewSource()
	assert.Nil(t, source)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consul")
	assert.Contains(t, err.Error(), constants.EnvConfigSourceFile)
}

func TestNewSourceRejectsNonConfigCenterSelector(t *testing.T) {
	clearSourceEnv(t)
	selector := filepath.Join(t.TempDir(), "source.yaml")
	require.NoError(t, os.WriteFile(selector, []byte("type: file\nfile:\n  path: config.yaml\n"), 0o600))
	t.Setenv(constants.EnvConfigSourceFile, selector)

	source, err := NewSource()
	assert.Nil(t, source)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must select config_center")
}

func TestNewSourceExplicitFile(t *testing.T) {
	clearSourceEnv(t)
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("name: gateway\n"), 0o600))
	t.Setenv(constants.EnvConfigSource, constants.ConfigSourceFile)
	t.Setenv(constants.EnvConfigFile, configFile)

	source, err := NewSource()
	require.NoError(t, err)
	assert.Equal(t, constants.ConfigSourceFile, source.Name())
	assert.Equal(t, "config.yaml", source.MainKey())

	contents, err := source.Load(context.Background(), source.MainKey())
	require.NoError(t, err)
	assert.Equal(t, "name: gateway\n", string(contents))
}

func TestLocalSourceLoadsRelatedFile(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "config.yaml")
	policyFile := filepath.Join(root, "policies", "policies.csv")
	require.NoError(t, os.MkdirAll(filepath.Dir(policyFile), 0o755))
	require.NoError(t, os.WriteFile(configFile, []byte("name: gateway\n"), 0o600))
	require.NoError(t, os.WriteFile(policyFile, []byte("p, admin, /*\n"), 0o600))

	source := NewLocalSource(configFile)
	contents, err := source.Load(context.Background(), RelatedKey(source.MainKey(), "policies/policies.csv"))
	require.NoError(t, err)
	assert.Equal(t, "p, admin, /*\n", string(contents))
}

func TestRelatedKey(t *testing.T) {
	assert.Equal(t, "policies/model.conf", RelatedKey("config.yaml", "policies/model.conf"))
	assert.Equal(t, "gateway/policies/model.conf", RelatedKey("gateway/config.yaml", "policies/model.conf"))
}

func TestConfigCenterSourceLoadsGatewayKeySet(t *testing.T) {
	entries := map[string]*configv1.ConfigEntry{}
	for _, key := range []string{
		"gateway/config.yaml",
		"gateway/secrets/public.pem",
		"gateway/policies/policies.csv",
		"gateway/policies/model.conf",
	} {
		entries[configID("ecommerce", "pre", key)] = &configv1.ConfigEntry{
			Namespace: "ecommerce", Environment: "pre", Key: key, Value: "value-for-" + key,
		}
	}
	service := &fakeConfigService{entries: entries}
	address := startFakeConfigService(t, service)
	source, err := NewConfigCenterSource(configCenterSelector(t, address, "ecommerce", "pre", "gateway/config.yaml"))
	require.NoError(t, err)

	keys := []string{
		source.MainKey(),
		RelatedKey(source.MainKey(), "secrets/public.pem"),
		RelatedKey(source.MainKey(), "policies/policies.csv"),
		RelatedKey(source.MainKey(), "policies/model.conf"),
	}
	for _, key := range keys {
		contents, loadErr := source.Load(context.Background(), key)
		require.NoError(t, loadErr)
		assert.Equal(t, "value-for-"+key, string(contents))
	}

	getCalls, _ := service.calls()
	require.Len(t, getCalls, len(keys))
	for i, call := range getCalls {
		assert.Equal(t, "ecommerce", call.request.GetNamespace())
		assert.Equal(t, "pre", call.request.GetEnvironment())
		assert.Equal(t, keys[i], call.request.GetKey())
		assert.Equal(t, "test-service-token", call.header.Get("x-config-center-service-token"))
	}
}

func TestConfigCenterSourceRejectsMissingOrEmptyValue(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		service := &fakeConfigService{entries: map[string]*configv1.ConfigEntry{}}
		address := startFakeConfigService(t, service)
		source, err := NewConfigCenterSource(configCenterSelector(t, address, "gateway", "prod", "config.yaml"))
		require.NoError(t, err)

		_, err = source.Load(context.Background(), source.MainKey())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gateway/prod/config.yaml")
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("empty", func(t *testing.T) {
		service := &fakeConfigService{entries: map[string]*configv1.ConfigEntry{
			configID("gateway", "prod", "config.yaml"): {Namespace: "gateway", Environment: "prod", Key: "config.yaml"},
		}}
		address := startFakeConfigService(t, service)
		source, err := NewConfigCenterSource(configCenterSelector(t, address, "gateway", "prod", "config.yaml"))
		require.NoError(t, err)

		_, err = source.Load(context.Background(), source.MainKey())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gateway/prod/config.yaml is empty")
	})
}

func TestConfigCenterSourceWatchHandlesEventsAndReconnects(t *testing.T) {
	service := &fakeConfigService{entries: map[string]*configv1.ConfigEntry{}}
	service.watch = func(
		ctx context.Context,
		callIndex int,
		_ *connect.Request[configv1.WatchKeysRequest],
		stream *connect.ServerStream[configv1.WatchKeysResponse],
	) error {
		if callIndex == 0 {
			for _, response := range []*configv1.WatchKeysResponse{
				{Type: configv1.WatchEventType_WATCH_EVENT_TYPE_SNAPSHOT, Entry: &configv1.ConfigEntry{Value: "snapshot"}},
				{Type: configv1.WatchEventType_WATCH_EVENT_TYPE_HEARTBEAT},
				{Type: configv1.WatchEventType_WATCH_EVENT_TYPE_PUT, Entry: &configv1.ConfigEntry{Value: "put"}},
				{Type: configv1.WatchEventType_WATCH_EVENT_TYPE_PUT, Entry: &configv1.ConfigEntry{}},
				{Type: configv1.WatchEventType_WATCH_EVENT_TYPE_DELETE, Entry: &configv1.ConfigEntry{Key: "gateway/config.yaml"}},
			} {
				if err := stream.Send(response); err != nil {
					return err
				}
			}
			return connect.NewError(connect.CodeUnavailable, errors.New("test disconnect"))
		}

		if err := stream.Send(&configv1.WatchKeysResponse{
			Type:  configv1.WatchEventType_WATCH_EVENT_TYPE_SNAPSHOT,
			Entry: &configv1.ConfigEntry{Value: "reconnected"},
		}); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	address := startFakeConfigService(t, service)
	source, err := NewConfigCenterSource(configCenterSelector(t, address, "ecommerce", "pre", "gateway/config.yaml"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() {
		done <- source.Watch(ctx, source.MainKey(), func(event Event) {
			events <- event
			if string(event.Value) == "reconnected" {
				cancel()
			}
		})
	}()

	var got []Event
	for {
		select {
		case event := <-events:
			got = append(got, event)
		case watchErr := <-done:
			require.NoError(t, watchErr)
			goto watchDone
		case <-ctx.Done():
			select {
			case watchErr := <-done:
				require.NoError(t, watchErr)
			case <-time.After(time.Second):
				t.Fatal("watch did not stop after context cancellation")
			}
			goto watchDone
		}
	}

watchDone:
	for {
		select {
		case event := <-events:
			got = append(got, event)
		default:
			goto eventsDrained
		}
	}

eventsDrained:
	require.GreaterOrEqual(t, len(got), 6)
	assert.Equal(t, "snapshot", string(got[0].Value))
	assert.Equal(t, "put", string(got[1].Value))
	assert.Error(t, got[2].Err)
	assert.True(t, got[3].Deleted)
	assert.Error(t, got[4].Err)
	assert.Equal(t, "reconnected", string(got[5].Value))

	_, watchCalls := service.calls()
	require.GreaterOrEqual(t, len(watchCalls), 2)
	for _, call := range watchCalls[:2] {
		assert.Equal(t, "ecommerce", call.request.GetNamespace())
		assert.Equal(t, "pre", call.request.GetEnvironment())
		assert.Equal(t, []string{"gateway/config.yaml"}, call.request.GetKeys())
		assert.Equal(t, "test-service-token", call.header.Get("x-config-center-service-token"))
	}
}

func TestReplaceFileRetainsLastKnownGoodContent(t *testing.T) {
	target := filepath.Join(t.TempDir(), "policy.csv")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o600))

	err := ReplaceFile([]byte("bad"), target, func(string) error {
		return assert.AnError
	})
	require.Error(t, err)

	contents, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, "old", string(contents))
}

func TestWatchFileRestoresFileAndRuntimeAfterApplyFailure(t *testing.T) {
	target := filepath.Join(t.TempDir(), "policy.csv")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o640))
	require.NoError(t, os.Chmod(target, 0o640))

	var applied []string
	var reported error
	err := WatchFile(
		context.Background(),
		eventSource{event: Event{Value: []byte("new")}},
		"policies/policies.csv",
		target,
		nil,
		func() error {
			contents, readErr := os.ReadFile(target)
			require.NoError(t, readErr)
			applied = append(applied, string(contents))
			if string(contents) == "new" {
				return assert.AnError
			}
			return nil
		},
		func(err error) { reported = err },
	)
	require.NoError(t, err)
	require.Error(t, reported)
	assert.Contains(t, reported.Error(), "apply update")
	assert.Equal(t, []string{"new", "old"}, applied)

	contents, readErr := os.ReadFile(target)
	require.NoError(t, readErr)
	assert.Equal(t, "old", string(contents))
	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}
