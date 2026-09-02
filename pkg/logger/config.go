package logger

import (
	"fmt"

	"go.uber.org/zap/zapcore"
)

type Config struct {
	Level        string
	ConsoleColor bool
	Stdout       bool
	Development  bool
	TimeLayout   string
	File         FileConfig
}

type FileConfig struct {
	Enable        bool
	Dir           string
	BaseName      string
	SeparateLevel bool
	DailyRotate   bool
	AlsoBySize    bool
	MaxSizeMB     int
	MaxBackups    int
	MaxAgeDays    int
	Compress      bool
}

type Option func(*Config)

func WithLevel(level string) Option        { return func(c *Config) { c.Level = level } }
func WithConsoleColor(value bool) Option   { return func(c *Config) { c.ConsoleColor = value } }
func WithStdout(value bool) Option         { return func(c *Config) { c.Stdout = value } }
func WithDevelopment(value bool) Option    { return func(c *Config) { c.Development = value } }
func WithFileDir(value string) Option      { return func(c *Config) { c.File.Dir = value } }
func WithFileBaseName(value string) Option { return func(c *Config) { c.File.BaseName = value } }
func WithFileEnable(value bool) Option     { return func(c *Config) { c.File.Enable = value } }
func WithSeparateLevel(value bool) Option  { return func(c *Config) { c.File.SeparateLevel = value } }
func WithDailyRotate(value bool) Option    { return func(c *Config) { c.File.DailyRotate = value } }
func WithAlsoBySize(value bool) Option     { return func(c *Config) { c.File.AlsoBySize = value } }
func WithMaxSizeMB(value int) Option       { return func(c *Config) { c.File.MaxSizeMB = value } }
func WithMaxBackups(value int) Option      { return func(c *Config) { c.File.MaxBackups = value } }
func WithMaxAgeDays(value int) Option      { return func(c *Config) { c.File.MaxAgeDays = value } }
func WithCompress(value bool) Option       { return func(c *Config) { c.File.Compress = value } }

func DefaultConfig() Config {
	return Config{
		Level: "info", ConsoleColor: true, Stdout: true, TimeLayout: "2006-01-02 15:04:05.000",
		File: FileConfig{Dir: "./logs", BaseName: "app", DailyRotate: true, AlsoBySize: true, MaxSizeMB: 100, MaxBackups: 10, MaxAgeDays: 30, Compress: true},
	}
}

func (config Config) zapLevel() (zapcore.Level, error) {
	if config.Level == "" {
		return zapcore.InfoLevel, nil
	}
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(config.Level)); err != nil {
		return zapcore.InfoLevel, fmt.Errorf("logger: invalid level %q: %w", config.Level, err)
	}
	return level, nil
}
