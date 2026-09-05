package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"

	"centipede/pkg/logger"

	"go.uber.org/zap"
)

// Runtime owns the active configuration snapshot. A Nacos update is installed
// only after the complete document has been decoded and validated.
type Runtime struct {
	current atomic.Pointer[Config]

	hooksMu sync.RWMutex
	hooks   []func(Config)

	watcher   io.Closer
	closeOnce sync.Once
}

func LoadRuntime(ctx context.Context) (*Runtime, error) {
	if err := loadDotEnv(); err != nil {
		return nil, err
	}
	bootstrap, err := loadBootstrapConfig()
	if err != nil {
		return nil, err
	}
	loader := NewLoader()
	content, err := loader.loadContent(ctx, bootstrap)
	if err != nil {
		return nil, err
	}
	cfg, err := buildConfig(content, bootstrap.AppEnv)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{}
	runtime.current.Store(&cfg)
	if bootstrap.ConfigSource == SourceNacos {
		provider, ok := loader.providers[SourceNacos].(WatchProvider)
		if !ok {
			return nil, fmt.Errorf("config provider %q does not support watching", SourceNacos)
		}
		watcher, err := provider.Watch(ctx, bootstrap, runtime.applyContent)
		if err != nil {
			return nil, fmt.Errorf("watch %s configuration: %w", SourceNacos, err)
		}
		runtime.watcher = watcher
	}
	return runtime, nil
}

func (runtime *Runtime) Current() Config {
	if runtime == nil {
		return Config{}
	}
	config := runtime.current.Load()
	if config == nil {
		return Config{}
	}
	return *config
}

func (runtime *Runtime) OnChange(fn func(Config)) {
	if runtime == nil || fn == nil {
		return
	}
	runtime.hooksMu.Lock()
	runtime.hooks = append(runtime.hooks, fn)
	runtime.hooksMu.Unlock()
}

func (runtime *Runtime) applyContent(content []byte) {
	previous := runtime.Current()
	next, err := buildConfig(content, previous.Environment)
	if err != nil {
		logger.Error("configuration hot-reload rejected", zap.Error(err))
		return
	}
	if err := validateHotReload(previous, next); err != nil {
		logger.Error("configuration hot-reload rejected", zap.Error(err))
		return
	}
	runtime.current.Store(&next)

	runtime.hooksMu.RLock()
	hooks := append([]func(Config){}, runtime.hooks...)
	runtime.hooksMu.RUnlock()
	for _, hook := range hooks {
		hook(next)
	}
	logger.Info("configuration hot-reloaded successfully")
}

func validateHotReload(previous, next Config) error {
	if previous.Storage != next.Storage || previous.Mail != next.Mail || previous.Migration != next.Migration {
		return errors.New("storage, mail or migration settings changed; restart is required")
	}
	if !reflect.DeepEqual(previous.Server, next.Server) {
		return errors.New("server settings changed; restart is required")
	}
	if previous.Database != next.Database {
		return errors.New("database settings changed; restart is required")
	}
	if previous.Auth.JWTSecret != next.Auth.JWTSecret || previous.Auth.RefreshCookie != next.Auth.RefreshCookie || previous.Auth.CookieSecure != next.Auth.CookieSecure {
		return errors.New("auth key or cookie settings changed; restart is required")
	}
	if !reflect.DeepEqual(previous.SSO, next.SSO) {
		return errors.New("SSO settings changed; restart is required")
	}
	return nil
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	var err error
	runtime.closeOnce.Do(func() {
		if runtime.watcher != nil {
			err = runtime.watcher.Close()
		}
	})
	return err
}
