package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

type Provider interface {
	Load(context.Context, BootstrapConfig) ([]byte, error)
}

type WatchProvider interface {
	Watch(context.Context, BootstrapConfig, func([]byte)) (io.Closer, error)
}

type Loader struct {
	providers map[string]Provider
}

func NewLoader() *Loader {
	return &Loader{providers: map[string]Provider{
		SourceFile:  FileProvider{},
		SourceNacos: NacosProvider{},
	}}
}

func Load(ctx context.Context) (Config, error) {
	return NewLoader().Load(ctx)
}

func LoadDatabase(ctx context.Context) (DatabaseConfig, error) {
	if err := loadDotEnv(); err != nil {
		return DatabaseConfig{}, err
	}
	bootstrap, err := loadBootstrapConfig()
	if err != nil {
		return DatabaseConfig{}, err
	}
	content, err := NewLoader().loadContent(ctx, bootstrap)
	if err != nil {
		return DatabaseConfig{}, err
	}
	raw, err := decode(content)
	if err != nil {
		return DatabaseConfig{}, err
	}
	database := DatabaseConfig{URL: raw.Database.URL, MaxConnections: raw.Database.MaxConnections, MinConnections: raw.Database.MinConnections}
	if err := database.Validate(); err != nil {
		return DatabaseConfig{}, err
	}
	return database, nil
}

func (loader *Loader) Load(ctx context.Context) (Config, error) {
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}
	bootstrap, err := loadBootstrapConfig()
	if err != nil {
		return Config{}, err
	}
	content, err := loader.loadContent(ctx, bootstrap)
	if err != nil {
		return Config{}, err
	}
	return buildConfig(content, bootstrap.AppEnv)
}

func buildConfig(content []byte, environment string) (Config, error) {
	raw, err := decode(content)
	if err != nil {
		return Config{}, err
	}
	return raw.build(environment)
}

func (loader *Loader) loadContent(ctx context.Context, bootstrap BootstrapConfig) ([]byte, error) {
	provider, ok := loader.providers[bootstrap.ConfigSource]
	if !ok {
		return nil, fmt.Errorf("unsupported config source %q", bootstrap.ConfigSource)
	}
	content, err := provider.Load(ctx, bootstrap)
	if err != nil {
		return nil, fmt.Errorf("load %s configuration: %w", bootstrap.ConfigSource, err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, errors.New("configuration content is empty")
	}
	return content, nil
}

func decode(content []byte) (rawConfig, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var raw rawConfig
	if err := decoder.Decode(&raw); err != nil {
		return rawConfig{}, fmt.Errorf("decode configuration YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return rawConfig{}, errors.New("configuration must contain exactly one YAML document")
		}
		return rawConfig{}, fmt.Errorf("decode configuration YAML: %w", err)
	}
	return raw, nil
}
