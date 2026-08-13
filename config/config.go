package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	configv1 "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/gateway/pkg/loader"
	"github.com/go-kratos/kratos/v2/log"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/yaml"
)

var (
	logger = log.NewHelper(log.With(log.DefaultLogger, "module", "config/config"))
	// protojson 配置选项
	_jsonOptions = &protojson.UnmarshalOptions{DiscardUnknown: true}
)

type OnChange func(*configv1.Gateway) error

// ConfigLoader 配置加载接口
type ConfigLoader interface {
	Load(context.Context) (*configv1.Gateway, error)
	Watch(OnChange)
	Close()
}

// FileLoader 文件加载器
type FileLoader struct {
	source             loader.Source
	confKey            string
	configData         []byte
	confSHA256         string            // conf file hash
	priorityDirectory  string            // 优先级更高的配置目录
	priorityConfigHash map[string]string // priorityConfig hash
	watchCancel        context.CancelFunc
	closed             bool
	applyLock          sync.Mutex
	lock               sync.RWMutex
	onChangeHandlers   []OnChange
}

// NewFileLoader 创建文件加载器
func NewFileLoader(confPath string, priorityDirectory string) (*FileLoader, error) {
	return NewSourceLoader(loader.NewLocalSource(confPath), priorityDirectory)
}

// NewSourceLoader creates a loader for the selected Config Center or explicit
// local-file source.
func NewSourceLoader(source loader.Source, priorityDirectory string) (*FileLoader, error) {
	if source == nil {
		return nil, errors.New("configuration source is nil")
	}
	fl := &FileLoader{
		source:            source,
		confKey:           source.MainKey(),
		priorityDirectory: priorityDirectory,
	}
	if err := fl.initialize(); err != nil {
		return nil, err
	}
	return fl, nil
}

// 文件加载器初始化
func (f *FileLoader) initialize() error {
	if f.priorityDirectory != "" {
		if err := os.MkdirAll(f.priorityDirectory, 0755); err != nil {
			return err
		}
	}
	configData, err := f.source.Load(context.Background(), f.confKey)
	if err != nil {
		return err
	}
	if _, err := f.decodeConfig(configData); err != nil {
		return fmt.Errorf("decode initial config from %s: %w", f.source.Name(), err)
	}
	f.configData = append([]byte(nil), configData...)
	f.confSHA256 = sha256sum(configData)
	logger.Infof("the initial config sha256: %s", f.confSHA256)

	pfHash, err := f.priorityConfigSHA256()
	if err != nil {
		return err
	}
	f.priorityConfigHash = pfHash
	logger.Infof("the initial priority config file sha256 map: %+v", f.priorityConfigHash)

	return nil
}

func sha256sum(in []byte) string {
	sum := sha256.Sum256(in)
	return hex.EncodeToString(sum[:])
}

func (f *FileLoader) priorityConfigSHA256() (map[string]string, error) {
	if f.priorityDirectory == "" {
		return map[string]string{}, nil
	}
	entrys, err := os.ReadDir(f.priorityDirectory)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entrys {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		configData, err := os.ReadFile(filepath.Join(f.priorityDirectory, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = sha256sum(configData)
	}
	return out, nil
}

// Load parses the last known-good configuration snapshot.
func (f *FileLoader) Load(_ context.Context) (*configv1.Gateway, error) {
	f.lock.RLock()
	configData := append([]byte(nil), f.configData...)
	f.lock.RUnlock()
	return f.decodeConfig(configData)
}

func (f *FileLoader) decodeConfig(configData []byte) (*configv1.Gateway, error) {
	jsonData, err := yaml.YAMLToJSON(configData)
	if err != nil {
		return nil, err
	}
	out := &configv1.Gateway{}
	if err := _jsonOptions.Unmarshal(jsonData, out); err != nil {
		return nil, err
	}
	if err := f.mergePriorityConfig(out); err != nil {
		logger.Warnf("failed to merge priority config: %+v", err)
	}

	return out, nil
}

type envValue struct {
	value  string
	exists bool
}

func applyEnvs(config *configv1.Gateway) (func() error, error) {
	previous := make(map[string]envValue, len(config.Envs))
	rollback := func() error {
		var rollbackErr error
		for key, old := range previous {
			var err error
			if old.exists {
				err = os.Setenv(key, old.value)
			} else {
				err = os.Unsetenv(key)
			}
			if err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore env %s: %w", key, err))
			}
		}
		return rollbackErr
	}

	keys := make([]string, 0, len(config.Envs))
	for key := range config.Envs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		oldValue, existed := os.LookupEnv(key)
		if err := os.Setenv(key, config.Envs[key]); err != nil {
			return nil, errors.Join(fmt.Errorf("set env %s: %w", key, err), rollback())
		}
		previous[key] = envValue{value: oldValue, exists: existed}
	}
	return rollback, nil
}

// join priorityDir 文件夹下所有配置，然后将所有配置合并到 conf path 输出的结构体中，覆盖源配置
func (f *FileLoader) mergePriorityConfig(dst *configv1.Gateway) error {
	if f.priorityDirectory == "" {
		return nil
	}
	entrys, err := os.ReadDir(f.priorityDirectory)
	if err != nil {
		return err
	}
	replaceOrPrependEndpoint := MakeReplaceOrPrependEndpointFn(dst.Endpoints)
	for _, e := range entrys {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		cfgPath := filepath.Join(f.priorityDirectory, e.Name())
		pCfg, err := f.parsePriorityConfig(cfgPath)
		if err != nil {
			logger.Warnf("failed to parse priority config: %s: %+v, skip merge this file", cfgPath, err)
			continue
		}
		for _, e := range pCfg.Endpoints {
			dst.Endpoints = replaceOrPrependEndpoint(dst.Endpoints, e)
		}
		logger.Infof("succeeded to merge priority config: %s, %d endpoints effected", cfgPath, len(pCfg.Endpoints))
	}
	return nil
}

// 解析配置
func (f *FileLoader) parsePriorityConfig(cfgPath string) (*configv1.PriorityConfig, error) {
	configData, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	jsonData, err := yaml.YAMLToJSON(configData)
	if err != nil {
		return nil, err
	}
	out := &configv1.PriorityConfig{}
	if err := _jsonOptions.Unmarshal(jsonData, out); err != nil {
		return nil, err
	}
	return out, nil
}

// MakeReplaceOrPrependEndpointFn 返回一个函数，用于替换源配置中的 endpoint，如果源配置中不存在，则添加到源配置中
func MakeReplaceOrPrependEndpointFn(origin []*configv1.Endpoint) func([]*configv1.Endpoint, *configv1.Endpoint) []*configv1.Endpoint {
	keyFn := func(e *configv1.Endpoint) string {
		return fmt.Sprintf("%s-%s", e.Method, e.Path)
	}
	index := map[string]int{}
	for i, e := range origin {
		index[keyFn(e)] = i
	}
	return func(dst []*configv1.Endpoint, item *configv1.Endpoint) []*configv1.Endpoint {
		idx, ok := index[keyFn(item)]
		if !ok {
			return append([]*configv1.Endpoint{item}, dst...)
		}
		dst[idx] = item
		return dst
	}
}

// Watch 设置配置文件变更事件处理器
func (f *FileLoader) Watch(fn OnChange) {
	logger.Info("add config file change event handler")
	f.lock.Lock()
	if f.closed {
		f.lock.Unlock()
		logger.Warn("ignore config file change event handler after loader close")
		return
	}
	f.onChangeHandlers = append(f.onChangeHandlers, fn)
	if f.watchCancel != nil {
		f.lock.Unlock()
		return
	}

	watchCtx, cancel := context.WithCancel(context.Background())
	f.watchCancel = cancel
	f.lock.Unlock()

	// Start only after the first handler is visible. Otherwise a remote update
	// can advance the snapshot before the proxy has registered its applier.
	if f.source.Name() == "file" {
		go f.watchLocal(watchCtx)
		return
	}
	go f.watchSource(watchCtx)
	if f.priorityDirectory != "" {
		go f.watchPriority(watchCtx)
	}
}

// 执行配置文件变更事件处理器
func (f *FileLoader) executeLoader(configData []byte, priorityHash map[string]string) error {
	f.applyLock.Lock()
	defer f.applyLock.Unlock()

	logger.Info("execute config loader")
	config, err := f.decodeConfig(configData)
	if err != nil {
		return err
	}

	f.lock.RLock()
	handlers := append([]OnChange(nil), f.onChangeHandlers...)
	f.lock.RUnlock()

	var chainedError error
	rollbackEnvs, err := applyEnvs(config)
	if err != nil {
		return err
	}
	for _, fn := range handlers {
		if err := fn(config); err != nil {
			logger.Errorf("execute config loader error on handler: %+v: %+v", fn, err)
			chainedError = errors.Join(chainedError, err)
		}
	}
	if chainedError != nil {
		return errors.Join(chainedError, rollbackEnvs())
	}

	f.lock.Lock()
	f.configData = append([]byte(nil), configData...)
	f.confSHA256 = sha256sum(configData)
	f.priorityConfigHash = cloneHash(priorityHash)
	f.lock.Unlock()
	return nil
}

func (f *FileLoader) watchLocal(ctx context.Context) {
	logger.Info("start watching local gateway config")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		configData, err := f.source.Load(ctx, f.confKey)
		if err != nil {
			logger.Errorf("watch local config: %v", err)
			continue
		}
		priorityHash, err := f.priorityConfigSHA256()
		if err != nil {
			logger.Errorf("watch priority config: %v", err)
			continue
		}
		if !f.changed(configData, priorityHash) {
			continue
		}
		if err := f.executeLoader(configData, priorityHash); err != nil {
			logger.Errorf("reload local config: %v", err)
		}
	}
}

func (f *FileLoader) watchSource(ctx context.Context) {
	logger.Infof("start watching gateway config from %s", f.source.Name())
	err := f.source.Watch(ctx, f.confKey, func(event loader.Event) {
		switch {
		case event.Err != nil:
			logger.Errorf("gateway config watch: %v", event.Err)
		case event.Deleted:
			logger.Errorf("gateway config key %q was deleted; retaining last known-good config", f.confKey)
		default:
			priorityHash, err := f.priorityConfigSHA256()
			if err != nil {
				logger.Errorf("read priority config during gateway update: %v", err)
				return
			}
			if !f.changed(event.Value, priorityHash) {
				return
			}
			if err := f.executeLoader(event.Value, priorityHash); err != nil {
				logger.Errorf("apply gateway config update: %v", err)
			}
		}
	})
	if err != nil && !errors.Is(err, loader.ErrUnsupportedWatch) && ctx.Err() == nil {
		logger.Errorf("gateway config watch exited: %v", err)
	}
}

func (f *FileLoader) watchPriority(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		priorityHash, err := f.priorityConfigSHA256()
		if err != nil {
			logger.Errorf("watch priority config: %v", err)
			continue
		}
		f.lock.RLock()
		configData := append([]byte(nil), f.configData...)
		priorityChanged := !reflect.DeepEqual(priorityHash, f.priorityConfigHash)
		f.lock.RUnlock()
		if priorityChanged {
			if err := f.executeLoader(configData, priorityHash); err != nil {
				logger.Errorf("apply priority config update: %v", err)
			}
		}
	}
}

func (f *FileLoader) changed(configData []byte, priorityHash map[string]string) bool {
	f.lock.RLock()
	defer f.lock.RUnlock()
	return sha256sum(configData) != f.confSHA256 || !reflect.DeepEqual(priorityHash, f.priorityConfigHash)
}

func cloneHash(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// Close 关闭配置文件加载
func (f *FileLoader) Close() {
	f.lock.Lock()
	f.closed = true
	cancel := f.watchCancel
	f.watchCancel = nil
	f.lock.Unlock()
	if cancel != nil {
		cancel()
	}
}

type InspectFileLoader struct {
	Source             string            `json:"source"`
	ConfigKey          string            `json:"configKey"`
	ConfSHA256         string            `json:"confSha256"`
	PriorityConfigHash map[string]string `json:"priorityConfigHash"`
	OnChangeHandlers   int64             `json:"onChangeHandlers"`
}

// DebugHandler debug service handler
func (f *FileLoader) DebugHandler() http.Handler {
	debugMux := http.NewServeMux()
	debugMux.HandleFunc("/debug/config/inspect", func(rw http.ResponseWriter, r *http.Request) {
		f.lock.RLock()
		out := &InspectFileLoader{
			Source:             f.source.Name(),
			ConfigKey:          f.confKey,
			ConfSHA256:         f.confSHA256,
			PriorityConfigHash: cloneHash(f.priorityConfigHash),
			OnChangeHandlers:   int64(len(f.onChangeHandlers)),
		}
		f.lock.RUnlock()
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(out)
	})
	debugMux.HandleFunc("/debug/config/load", func(rw http.ResponseWriter, r *http.Request) {
		out, err := f.Load(context.Background())
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			_, _ = rw.Write([]byte(err.Error()))
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		b, _ := protojson.Marshal(out)
		_, _ = rw.Write(b)
	})
	debugMux.HandleFunc("/debug/config/version", func(rw http.ResponseWriter, r *http.Request) {
		out, err := f.Load(context.Background())
		if err != nil {
			rw.WriteHeader(http.StatusInternalServerError)
			_, _ = rw.Write([]byte(err.Error()))
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]interface{}{
			"version": out.Version,
		})
	})
	return debugMux
}
