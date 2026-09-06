package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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
	if err := applyEnvironmentOverrides(&raw); err != nil {
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
	if err := applyEnvironmentOverrides(&raw); err != nil {
		return Config{}, err
	}
	return raw.build(environment)
}

// applyEnvironmentOverrides keeps the profile YAML useful for local and
// Nacos deployments while allowing container platforms to inject connection,
// URL and secret settings without baking them into an image. Empty variables
// are ignored so existing profile values continue to work unchanged.
func applyEnvironmentOverrides(raw *rawConfig) error {
	if value := strings.TrimSpace(os.Getenv("SERVER_ADDRESS")); value != "" {
		raw.Server.Address = value
	}
	if value := strings.TrimSpace(os.Getenv("SERVER_PUBLIC_URL")); value != "" {
		raw.Server.PublicURL = value
	}
	if value := strings.TrimSpace(os.Getenv("CORS_ORIGINS")); value != "" {
		raw.Server.CORSOrigins = splitEnvironmentList(value)
	}
	if value := strings.TrimSpace(os.Getenv("DATABASE_URL")); value != "" {
		raw.Database.URL = value
	}
	if value := strings.TrimSpace(os.Getenv("DATABASE_MAX_CONNECTIONS")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return fmt.Errorf("DATABASE_MAX_CONNECTIONS must be an integer: %w", err)
		}
		raw.Database.MaxConnections = int32(parsed)
	}
	if value := strings.TrimSpace(os.Getenv("DATABASE_MIN_CONNECTIONS")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return fmt.Errorf("DATABASE_MIN_CONNECTIONS must be an integer: %w", err)
		}
		raw.Database.MinConnections = int32(parsed)
	}
	if value := strings.TrimSpace(os.Getenv("JWT_SECRET")); value != "" {
		raw.Auth.JWTSecret = value
	}
	if value := strings.TrimSpace(os.Getenv("AUTH_ACCESS_TOKEN_TTL")); value != "" {
		raw.Auth.AccessTokenTTL = value
	}
	if value := strings.TrimSpace(os.Getenv("AUTH_REFRESH_TOKEN_TTL")); value != "" {
		raw.Auth.RefreshTokenTTL = value
	}
	if value := strings.TrimSpace(os.Getenv("AUTH_REFRESH_COOKIE")); value != "" {
		raw.Auth.RefreshCookie = value
	}
	if value := strings.TrimSpace(os.Getenv("AUTH_COOKIE_SECURE")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("AUTH_COOKIE_SECURE must be a boolean: %w", err)
		}
		raw.Auth.CookieSecure = parsed
	}
	if value := strings.TrimSpace(os.Getenv("LICENSE_SIGNING_SECRET")); value != "" {
		raw.License.SigningSecret = value
	}
	if value := strings.TrimSpace(os.Getenv("STORAGE_DATA_DIR")); value != "" {
		raw.Storage.DataDir = value
	}
	if value := strings.TrimSpace(os.Getenv("STORAGE_MAX_UPLOAD_BYTES")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("STORAGE_MAX_UPLOAD_BYTES must be an integer: %w", err)
		}
		raw.Storage.MaxUploadBytes = parsed
	}
	if value := strings.TrimSpace(os.Getenv("FRONTEND_BASE_URL")); value != "" {
		raw.Migration.FrontendBaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("LEGACY_BASE_URL")); value != "" {
		raw.Migration.LegacyBaseURL = value
	}
	return nil
}

func splitEnvironmentList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
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
