package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Config struct {
	Environment string
	Server      ServerConfig
	Database    DatabaseConfig
	Auth        AuthConfig
	SSO         SSOConfig
}

type ServerConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

type DatabaseConfig struct {
	URL            string
	MaxConnections int32
	MinConnections int32
}

type AuthConfig struct {
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	RefreshCookie   string
	CookieSecure    bool
}

type SSOConfig struct {
	IssuerURL   string
	ClientID    string
	RedirectURL string
	Scopes      []string
}

type rawConfig struct {
	Server struct {
		Address           string `yaml:"address"`
		ReadHeaderTimeout string `yaml:"read_header_timeout"`
		ReadTimeout       string `yaml:"read_timeout"`
		WriteTimeout      string `yaml:"write_timeout"`
		IdleTimeout       string `yaml:"idle_timeout"`
		ShutdownTimeout   string `yaml:"shutdown_timeout"`
	} `yaml:"server"`
	Database struct {
		URL            string `yaml:"url"`
		MaxConnections int32  `yaml:"max_connections"`
		MinConnections int32  `yaml:"min_connections"`
	} `yaml:"database"`
	Auth struct {
		JWTSecret       string `yaml:"jwt_secret"`
		AccessTokenTTL  string `yaml:"access_token_ttl"`
		RefreshTokenTTL string `yaml:"refresh_token_ttl"`
		RefreshCookie   string `yaml:"refresh_cookie"`
		CookieSecure    bool   `yaml:"cookie_secure"`
	} `yaml:"auth"`
	SSO struct {
		IssuerURL   string   `yaml:"issuer_url"`
		ClientID    string   `yaml:"client_id"`
		RedirectURL string   `yaml:"redirect_url"`
		Scopes      []string `yaml:"scopes"`
	} `yaml:"sso"`
}

func (raw rawConfig) build(environment string) (Config, error) {
	readHeaderTimeout, err := parseDuration("server.read_header_timeout", raw.Server.ReadHeaderTimeout)
	if err != nil {
		return Config{}, err
	}
	readTimeout, err := parseDuration("server.read_timeout", raw.Server.ReadTimeout)
	if err != nil {
		return Config{}, err
	}
	writeTimeout, err := parseDuration("server.write_timeout", raw.Server.WriteTimeout)
	if err != nil {
		return Config{}, err
	}
	idleTimeout, err := parseDuration("server.idle_timeout", raw.Server.IdleTimeout)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := parseDuration("server.shutdown_timeout", raw.Server.ShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	accessTokenTTL, err := parseDuration("auth.access_token_ttl", raw.Auth.AccessTokenTTL)
	if err != nil {
		return Config{}, err
	}
	refreshTokenTTL, err := parseDuration("auth.refresh_token_ttl", raw.Auth.RefreshTokenTTL)
	if err != nil {
		return Config{}, err
	}

	result := Config{
		Environment: environment,
		Server: ServerConfig{
			Address:           strings.TrimSpace(raw.Server.Address),
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ShutdownTimeout:   shutdownTimeout,
		},
		Database: DatabaseConfig{
			URL:            strings.TrimSpace(raw.Database.URL),
			MaxConnections: raw.Database.MaxConnections,
			MinConnections: raw.Database.MinConnections,
		},
		Auth: AuthConfig{
			JWTSecret:       raw.Auth.JWTSecret,
			AccessTokenTTL:  accessTokenTTL,
			RefreshTokenTTL: refreshTokenTTL,
			RefreshCookie:   fallback(raw.Auth.RefreshCookie, "centipede_refresh"),
			CookieSecure:    raw.Auth.CookieSecure,
		},
		SSO: SSOConfig{
			IssuerURL:   strings.TrimSpace(raw.SSO.IssuerURL),
			ClientID:    strings.TrimSpace(raw.SSO.ClientID),
			RedirectURL: strings.TrimSpace(raw.SSO.RedirectURL),
			Scopes:      cleanScopes(raw.SSO.Scopes),
		},
	}
	if err := result.Validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (config Config) Validate() error {
	if config.Environment == "" {
		return errors.New("app environment is required")
	}
	if config.Server.Address == "" {
		return errors.New("server.address is required")
	}
	if config.Server.ReadHeaderTimeout <= 0 || config.Server.ReadTimeout <= 0 || config.Server.WriteTimeout <= 0 || config.Server.IdleTimeout <= 0 || config.Server.ShutdownTimeout <= 0 {
		return errors.New("all server timeouts must be greater than zero")
	}
	if err := config.Database.Validate(); err != nil {
		return err
	}
	if config.Auth.AccessTokenTTL <= 0 || config.Auth.RefreshTokenTTL <= 0 {
		return errors.New("auth token TTL values must be greater than zero")
	}
	if strings.TrimSpace(config.Auth.JWTSecret) == "" || config.Auth.JWTSecret == "CHANGE_ME" {
		return errors.New("auth.jwt_secret is required")
	}
	if config.Environment == "production" && (len(config.Auth.JWTSecret) < 32 || config.Auth.JWTSecret == "dev-only-change-me-please" || !config.Auth.CookieSecure) {
		return errors.New("production auth requires a non-default JWT secret of at least 32 characters and cookie_secure=true")
	}
	if !validCookieName(config.Auth.RefreshCookie) {
		return errors.New("auth.refresh_cookie is invalid")
	}
	return nil
}

func (config DatabaseConfig) Validate() error {
	if config.URL == "" || config.URL == "CHANGE_ME" {
		return errors.New("database.url is required")
	}
	if config.MinConnections < 0 || config.MaxConnections <= 0 || config.MinConnections > config.MaxConnections {
		return errors.New("database connections must satisfy 0 <= min_connections <= max_connections")
	}
	return nil
}

func parseDuration(name, value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return strings.TrimSpace(value)
}

func cleanScopes(scopes []string) []string {
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			result = append(result, scope)
		}
	}
	return result
}

func validCookieName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}\t", character) {
			return false
		}
	}
	return true
}
