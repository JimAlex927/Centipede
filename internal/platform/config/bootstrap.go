package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	SourceFile  = "file"
	SourceNacos = "nacos"
)

var environmentPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type BootstrapConfig struct {
	AppEnv       string
	ConfigSource string
	ConfigDir    string
	Nacos        NacosBootstrapConfig
}

type NacosBootstrapConfig struct {
	ServerAddresses string
	Namespace       string
	Group           string
	DataID          string
	Username        string
	Password        string
	Timeout         time.Duration
	CacheDir        string
	LogDir          string
	LogLevel        string
}

func loadDotEnv() error {
	path := os.Getenv("DOTENV_PATH")
	if path == "" {
		path = ".env"
	}
	if err := godotenv.Load(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load dotenv %s: %w", path, err)
	}
	return nil
}

func loadBootstrapConfig() (BootstrapConfig, error) {
	appEnv := environmentValue("APP_ENV", "dev")
	configSource := strings.ToLower(environmentValue("CONFIG_SOURCE", SourceFile))
	timeout, err := environmentDuration("NACOS_TIMEOUT", 10*time.Second)
	if err != nil {
		return BootstrapConfig{}, err
	}
	bootstrap := BootstrapConfig{
		AppEnv:       appEnv,
		ConfigSource: configSource,
		ConfigDir:    environmentValue("CONFIG_DIR", "config"),
		Nacos: NacosBootstrapConfig{
			ServerAddresses: environmentValue("NACOS_SERVER_ADDRS", "http://127.0.0.1:8848"),
			Namespace:       environmentValue("NACOS_NAMESPACE", ""),
			Group:           environmentValue("NACOS_GROUP", "DEFAULT_GROUP"),
			DataID:          environmentValue("NACOS_DATA_ID", "centipede-"+appEnv+".yaml"),
			Username:        environmentValue("NACOS_USERNAME", "nacos"),
			Password:        environmentValue("NACOS_PASSWORD", "nacos"),
			Timeout:         timeout,
			CacheDir:        environmentValue("NACOS_CACHE_DIR", "data/nacos/cache"),
			LogDir:          environmentValue("NACOS_LOG_DIR", "data/nacos/log"),
			LogLevel:        environmentValue("NACOS_LOG_LEVEL", "warn"),
		},
	}
	if !environmentPattern.MatchString(bootstrap.AppEnv) {
		return BootstrapConfig{}, errors.New("APP_ENV may contain only letters, numbers, underscores, and hyphens")
	}
	if bootstrap.ConfigSource != SourceFile && bootstrap.ConfigSource != SourceNacos {
		return BootstrapConfig{}, errors.New("CONFIG_SOURCE must be file or nacos")
	}
	if strings.TrimSpace(bootstrap.ConfigDir) == "" {
		return BootstrapConfig{}, errors.New("CONFIG_DIR must not be empty")
	}
	if bootstrap.ConfigSource == SourceNacos {
		if bootstrap.Nacos.Timeout <= 0 {
			return BootstrapConfig{}, errors.New("NACOS_TIMEOUT must be greater than zero")
		}
		if strings.TrimSpace(bootstrap.Nacos.ServerAddresses) == "" {
			return BootstrapConfig{}, errors.New("NACOS_SERVER_ADDRS is required when CONFIG_SOURCE=nacos")
		}
		if strings.TrimSpace(bootstrap.Nacos.Group) == "" {
			return BootstrapConfig{}, errors.New("NACOS_GROUP is required when CONFIG_SOURCE=nacos")
		}
		if strings.TrimSpace(bootstrap.Nacos.DataID) == "" {
			return BootstrapConfig{}, errors.New("NACOS_DATA_ID is required when CONFIG_SOURCE=nacos")
		}
	}
	return bootstrap, nil
}

func environmentValue(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func environmentDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	if milliseconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		return time.Duration(milliseconds) * time.Millisecond, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return duration, nil
}
