package loader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-kratos/gateway/constants"
	"github.com/lens077/config-center/sdk/configsource"
)

const (
	watchMinBackoff = time.Second
	watchMaxBackoff = 30 * time.Second
)

var ErrUnsupportedWatch = configsource.ErrUnsupportedWatch

type Event struct {
	Value   []byte
	Deleted bool
	Err     error
}

// Source loads gateway-owned files. The main key comes from the local selector;
// related files resolve beside it in the same namespace and environment.
type Source interface {
	Name() string
	MainKey() string
	Load(context.Context, string) ([]byte, error)
	Watch(context.Context, string, func(Event)) error
}

type configCenterSource struct {
	config configsource.Config
}

type fileSource struct {
	mainPath string
	mainKey  string
	root     string
}

// NewSource follows the same bootstrap contract as Cart: normal startup must
// use CONFIG_SOURCE_FILE, while CONFIG_SOURCE=file is an explicit local-only
// mode. There is no Consul KV fallback.
func NewSource() (Source, error) {
	if selector := os.Getenv(constants.EnvConfigSourceFile); selector != "" {
		return NewConfigCenterSource(selector)
	}

	switch sourceType := os.Getenv(constants.EnvConfigSource); sourceType {
	case constants.ConfigSourceFile:
		configFile := os.Getenv(constants.EnvConfigFile)
		if configFile == "" {
			return nil, fmt.Errorf("required env %s is missing when %s=%s",
				constants.EnvConfigFile, constants.EnvConfigSource, constants.ConfigSourceFile)
		}
		return NewLocalSource(configFile), nil
	case constants.ConfigSourceConfigCenter:
		return nil, fmt.Errorf("%s=%s is deprecated; set %s to a local SourceConfig file instead",
			constants.EnvConfigSource, constants.ConfigSourceConfigCenter, constants.EnvConfigSourceFile)
	case "":
		return nil, fmt.Errorf("required env %s is missing", constants.EnvConfigSourceFile)
	default:
		return nil, fmt.Errorf("unknown %s=%q, expect %q or set %s",
			constants.EnvConfigSource, sourceType, constants.ConfigSourceFile, constants.EnvConfigSourceFile)
	}
}

func NewConfigCenterSource(selector string) (Source, error) {
	cfg, err := configsource.LoadSourceConfig(selector)
	if err != nil {
		return nil, err
	}
	if cfg.Type != configsource.TypeConfigCenter {
		return nil, fmt.Errorf("%s must select config_center, got %q", selector, cfg.Type)
	}
	return &configCenterSource{config: cfg}, nil
}

func NewLocalSource(configFile string) Source {
	cleaned := filepath.Clean(configFile)
	return &fileSource{
		mainPath: cleaned,
		mainKey:  filepath.Base(cleaned),
		root:     filepath.Dir(cleaned),
	}
}

func (s *configCenterSource) Name() string { return string(s.config.Type) }
func (s *configCenterSource) MainKey() string {
	return s.config.ConfigCenter.Key
}

func (s *configCenterSource) Load(ctx context.Context, key string) ([]byte, error) {
	cfg, err := s.configForKey(key)
	if err != nil {
		return nil, err
	}
	return configsource.Load(ctx, cfg)
}

func (s *configCenterSource) Watch(ctx context.Context, key string, onEvent func(Event)) error {
	cfg, err := s.configForKey(key)
	if err != nil {
		return err
	}

	backoff := watchMinBackoff
	for {
		gotEvent := false
		err := configsource.Watch(ctx, cfg, func(event configsource.Event) {
			gotEvent = true
			onEvent(Event{Value: event.Value, Deleted: event.Deleted, Err: event.Err})
		})
		if ctx.Err() != nil {
			return nil
		}
		if gotEvent {
			backoff = watchMinBackoff
		}
		if err == nil {
			err = errors.New("watch stream ended without an error")
		}
		onEvent(Event{Err: fmt.Errorf("config-center watch stream ended, retry in %s: %w", backoff, err)})

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff *= 2; backoff > watchMaxBackoff {
			backoff = watchMaxBackoff
		}
	}
}

func (s *configCenterSource) configForKey(key string) (configsource.Config, error) {
	if err := validateKey(key); err != nil {
		return configsource.Config{}, err
	}
	cfg := s.config
	cfg.ConfigCenter.Key = key
	return cfg, nil
}

func (s *fileSource) Name() string    { return constants.ConfigSourceFile }
func (s *fileSource) MainKey() string { return s.mainKey }

func (s *fileSource) Load(_ context.Context, key string) ([]byte, error) {
	filePath, err := s.pathForKey(key)
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read local config %q: %w", filePath, err)
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("local config %q is empty", filePath)
	}
	return contents, nil
}

func (s *fileSource) Watch(context.Context, string, func(Event)) error {
	return ErrUnsupportedWatch
}

func (s *fileSource) pathForKey(key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	if key == s.mainKey {
		return s.mainPath, nil
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// RelatedKey keeps gateway assets next to the main key. Both
// namespace=gateway,key=config.yaml and namespace=ecommerce,key=gateway/config.yaml
// therefore resolve to a coherent key set.
func RelatedKey(mainKey, relative string) string {
	base := path.Dir(mainKey)
	if base == "." {
		return path.Clean(relative)
	}
	return path.Join(base, relative)
}

func validateKey(key string) error {
	if key == "" {
		return errors.New("configuration key is empty")
	}
	cleaned := path.Clean(key)
	if strings.HasPrefix(key, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("configuration key %q escapes its source root", key)
	}
	return nil
}

// ReplaceFile validates content through a temporary file and atomically swaps
// it into place. A bad update never replaces the last known-good file.
func ReplaceFile(contents []byte, target string, validate func(string) error) (err error) {
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(target); statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect existing file %q: %w", target, statErr)
	}
	return replaceFile(contents, target, mode, validate)
}

func replaceFile(contents []byte, target string, mode os.FileMode, validate func(string) error) (err error) {
	if len(contents) == 0 {
		return fmt.Errorf("refuse to replace %q with empty content", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create directory for %q: %w", target, err)
	}

	temp, err := os.CreateTemp(filepath.Dir(target), ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %q: %w", target, err)
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}()

	if err := temp.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := temp.Write(contents); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if validate != nil {
		if err := validate(tempName); err != nil {
			return fmt.Errorf("validate update for %q: %w", target, err)
		}
	}
	if err := os.Rename(tempName, target); err != nil {
		return fmt.Errorf("replace %q: %w", target, err)
	}
	return nil
}

type fileSnapshot struct {
	contents []byte
	mode     os.FileMode
	exists   bool
}

func snapshotFile(target string) (fileSnapshot, error) {
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("inspect current file %q: %w", target, err)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read current file %q: %w", target, err)
	}
	return fileSnapshot{contents: contents, mode: info.Mode().Perm(), exists: true}, nil
}

func restoreFile(target string, snapshot fileSnapshot) error {
	if snapshot.exists {
		return replaceFile(snapshot.contents, target, snapshot.mode, nil)
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove newly created file %q: %w", target, err)
	}
	return nil
}

func SyncFile(ctx context.Context, source Source, key, target string, validate func(string) error) error {
	contents, err := source.Load(ctx, key)
	if err != nil {
		return err
	}
	return ReplaceFile(contents, target, validate)
}

// WatchFile applies valid remote updates and retains the last known-good file
// for delete or malformed events.
func WatchFile(
	ctx context.Context,
	source Source,
	key string,
	target string,
	validate func(string) error,
	onChange func() error,
	onError func(error),
) error {
	return source.Watch(ctx, key, func(event Event) {
		var err error
		switch {
		case event.Err != nil:
			err = event.Err
		case event.Deleted:
			err = fmt.Errorf("config-center key %q was deleted; retaining last known-good file", key)
		default:
			var previous fileSnapshot
			previous, err = snapshotFile(target)
			if err == nil {
				err = ReplaceFile(event.Value, target, validate)
			}
			if err == nil && onChange != nil {
				if applyErr := onChange(); applyErr != nil {
					err = fmt.Errorf("apply update for %q: %w", target, applyErr)
					if restoreErr := restoreFile(target, previous); restoreErr != nil {
						err = errors.Join(err, fmt.Errorf("restore last known-good file %q: %w", target, restoreErr))
					} else if runtimeErr := onChange(); runtimeErr != nil {
						err = errors.Join(err, fmt.Errorf("restore runtime from last known-good file %q: %w", target, runtimeErr))
					}
				}
			}
		}
		if err != nil && onError != nil {
			onError(err)
		}
	})
}
