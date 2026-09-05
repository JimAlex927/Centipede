package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const validConfigYAML = `
server:
  address: ":9090"
  read_header_timeout: "5s"
  read_timeout: "20s"
  write_timeout: "20s"
  idle_timeout: "60s"
  shutdown_timeout: "10s"
database:
  url: "postgres://user:pass@localhost:5432/app"
  max_connections: 10
  min_connections: 2
auth:
  jwt_secret: "test-secret-that-is-long-enough-for-config"
  access_token_ttl: "15m"
  refresh_token_ttl: "24h"
  refresh_cookie: "centipede_refresh"
  cookie_secure: false
sso:
  issuer_url: "http://localhost:7777"
  client_id: "centipede.web"
  redirect_url: "http://localhost:7788/oauth/callback"
  scopes: ["openid", "profile", "email"]
migration:
  legacy_base_url: "http://localhost:3000"
`

func TestBuildConfigFromYAML(t *testing.T) {
	cfg, err := buildConfig([]byte(validConfigYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "test" || cfg.Server.Address != ":9090" || cfg.Database.MaxConnections != 10 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.Auth.AccessTokenTTL.String() != "15m0s" || len(cfg.SSO.Scopes) != 3 {
		t.Fatalf("unexpected auth/sso config: %#v", cfg)
	}
	if cfg.Migration.LegacyBaseURL != "http://localhost:3000" {
		t.Fatalf("unexpected migration config: %#v", cfg.Migration)
	}
}

func TestMigrationConfigRejectsNonHTTPURL(t *testing.T) {
	if _, err := buildConfig([]byte(validConfigYAML+`
migration:
  legacy_base_url: "not-a-url"
`), "test"); err == nil {
		t.Fatal("expected invalid legacy URL rejection")
	}
}

func TestFileLoaderUsesBootstrapEnvironment(t *testing.T) {
	directory := t.TempDir()
	configDirectory := filepath.Join(directory, "config")
	if err := os.Mkdir(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "application-local.yaml"), []byte(validConfigYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOTENV_PATH", "")
	t.Setenv("APP_ENV", "local")
	t.Setenv("CONFIG_SOURCE", SourceFile)
	t.Setenv("CONFIG_DIR", configDirectory)
	cfg, err := NewLoader().Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "local" || cfg.Server.Address != ":9090" {
		t.Fatalf("unexpected loaded config: %#v", cfg)
	}
}

func TestBootstrapDefaultsAndNacosSettings(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	t.Setenv("CONFIG_SOURCE", SourceNacos)
	t.Setenv("NACOS_SERVER_ADDRS", "http://127.0.0.1:8848")
	t.Setenv("NACOS_DATA_ID", "centipede-dev.yaml")
	bootstrap, err := loadBootstrapConfig()
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.ConfigSource != SourceNacos || bootstrap.Nacos.Group != "DEFAULT_GROUP" || bootstrap.Nacos.Timeout <= 0 {
		t.Fatalf("unexpected bootstrap: %#v", bootstrap)
	}
}

func TestLoadDotEnvDoesNotOverrideOperatingSystem(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	if err := os.WriteFile(path, []byte("APP_ENV=from-file\nCONFIG_SOURCE=nacos\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOTENV_PATH", path)
	t.Setenv("APP_ENV", "from-process")
	if err := loadDotEnv(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("APP_ENV"); got != "from-process" {
		t.Fatalf("APP_ENV = %q, want process environment value", got)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	if _, err := decode([]byte(validConfigYAML + "unknown: true\n")); err == nil {
		t.Fatal("decode accepted an unknown field")
	}
}

func TestProductionConfigRejectsPlaceholders(t *testing.T) {
	if _, err := buildConfig([]byte(`
server: {address: ":7788", read_header_timeout: "5s", read_timeout: "20s", write_timeout: "20s", idle_timeout: "60s", shutdown_timeout: "10s"}
database: {url: "CHANGE_ME", max_connections: 10, min_connections: 2}
auth: {jwt_secret: "CHANGE_ME", access_token_ttl: "15m", refresh_token_ttl: "24h", refresh_cookie: "centipede_refresh", cookie_secure: true}
`), "production"); err == nil {
		t.Fatal("expected production placeholder rejection")
	}
}
