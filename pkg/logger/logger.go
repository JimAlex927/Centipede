// Package logger provides a zap-based global logger with console, JSON file,
// rotation, and tagged logging support.
package logger

import (
	"errors"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	mu            sync.RWMutex
	global        *zap.Logger
	globalCleanup func() error
)

func init() {
	logger, cleanup, _ := newWithCleanup(DefaultConfig())
	global = logger
	globalCleanup = cleanup
}

func Init(options ...Option) (func(), error) {
	config := DefaultConfig()
	for _, option := range options {
		option(&config)
	}
	logger, cleanup, err := newWithCleanup(config)
	if err != nil {
		return func() {}, err
	}
	oldCleanup := setGlobal(logger, cleanup)
	if oldCleanup != nil {
		_ = oldCleanup()
	}
	return func() { _ = cleanup() }, nil
}

func New(config Config) (*zap.Logger, func() error, error) { return newWithCleanup(config) }

func newWithCleanup(config Config) (*zap.Logger, func() error, error) {
	if config.TimeLayout == "" {
		config.TimeLayout = time.DateTime
	}
	cores, closers, err := buildCores(config)
	if err != nil {
		return nil, nil, err
	}
	if len(cores) == 0 {
		return nil, nil, errors.New("logger: no output configured (set Stdout or File.Enable)")
	}
	options := []zap.Option{zap.AddCaller(), zap.AddCallerSkip(1)}
	if config.Development {
		options = append(options, zap.Development(), zap.AddStacktrace(zapcore.WarnLevel))
	} else {
		options = append(options, zap.AddStacktrace(zapcore.ErrorLevel))
	}
	logger := zap.New(zapcore.NewTee(cores...), options...)
	cleanup := func() error {
		var firstErr error
		if err := logger.Sync(); err != nil {
			firstErr = err
		}
		for _, closeFn := range closers {
			if err := closeFn(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	return logger, cleanup, nil
}

func buildCores(config Config) ([]zapcore.Core, []func() error, error) {
	minLevel, err := config.zapLevel()
	if err != nil {
		return nil, nil, err
	}
	var cores []zapcore.Core
	var closers []func() error
	if config.Stdout {
		core := zapcore.NewCore(consoleEncoder(config), zapcore.AddSync(os.Stdout), minLevel)
		cores = append(cores, tagPrefixCore{Core: core, color: config.ConsoleColor})
	}
	if config.File.Enable {
		if err := os.MkdirAll(config.File.Dir, 0o755); err != nil {
			return nil, nil, err
		}
		encoder := jsonEncoder(config)
		if config.File.SeparateLevel {
			type spec struct {
				tag      string
				min, max zapcore.Level
				openTop  bool
			}
			specs := []spec{{"DEBUG", zapcore.DebugLevel, zapcore.InfoLevel, false}, {"INFO", zapcore.InfoLevel, zapcore.WarnLevel, false}, {"WARN", zapcore.WarnLevel, zapcore.ErrorLevel, false}, {"ERROR", zapcore.ErrorLevel, 0, true}}
			for _, item := range specs {
				if minLevel > item.min && !item.openTop {
					continue
				}
				writer, err := newFileWriter(config.File, item.tag)
				if err != nil {
					return nil, nil, err
				}
				closers = append(closers, writer.Close)
				effectiveMin := minLevel
				if item.min > effectiveMin {
					effectiveMin = item.min
				}
				var enabler zapcore.LevelEnabler
				if item.openTop {
					level := effectiveMin
					enabler = zap.LevelEnablerFunc(func(value zapcore.Level) bool { return value >= level })
				} else {
					enabler = levelBand{min: effectiveMin, max: item.max}
				}
				cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(writer), enabler))
			}
		} else {
			writer, err := newFileWriter(config.File, "ALL")
			if err != nil {
				return nil, nil, err
			}
			closers = append(closers, writer.Close)
			cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(writer), minLevel))
		}
	}
	return cores, closers, nil
}

func newFileWriter(config FileConfig, levelTag string) (*writerSyncer, error) {
	writer, err := NewDailyWriter(config.Dir, config.BaseName, levelTag, config.DailyRotate, config.AlsoBySize, config.MaxSizeMB, config.MaxBackups, config.MaxAgeDays, config.Compress)
	if err != nil {
		return nil, err
	}
	return &writerSyncer{writer}, nil
}

type writerSyncer struct{ *DailyWriter }

func (writer *writerSyncer) Sync() error  { return writer.DailyWriter.Sync() }
func (writer *writerSyncer) Close() error { return writer.DailyWriter.Close() }

type levelBand struct{ min, max zapcore.Level }

func (band levelBand) Enabled(level zapcore.Level) bool { return level >= band.min && level < band.max }

type tagPrefixCore struct {
	zapcore.Core
	fields []zapcore.Field
	color  bool
}

func (core tagPrefixCore) With(fields []zapcore.Field) zapcore.Core {
	allFields := append(append([]zapcore.Field{}, core.fields...), fields...)
	return tagPrefixCore{Core: core.Core.With(withoutTagField(fields)), fields: allFields, color: core.color}
}
func (core tagPrefixCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if core.Enabled(entry.Level) {
		return checked.AddCore(entry, core)
	}
	return checked
}
func (core tagPrefixCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	if tag, ok := findTagField(core.fields, fields); ok {
		message := "[" + tag + "] " + entry.Message
		if core.color {
			message = colorFor(tag) + message + ansiReset
		}
		entry.Message = message
	}
	return core.Core.Write(entry, withoutTagField(fields))
}

func findTagField(groups ...[]zapcore.Field) (string, bool) {
	for _, fields := range groups {
		for _, field := range fields {
			if field.Key == "tag" && field.Type == zapcore.StringType && field.String != "" {
				return field.String, true
			}
		}
	}
	return "", false
}
func withoutTagField(fields []zapcore.Field) []zapcore.Field {
	for _, field := range fields {
		if field.Key == "tag" {
			filtered := make([]zapcore.Field, 0, len(fields)-1)
			for _, item := range fields {
				if item.Key != "tag" {
					filtered = append(filtered, item)
				}
			}
			return filtered
		}
	}
	return fields
}

func consoleEncoder(config Config) zapcore.Encoder {
	encoderConfig := zapcore.EncoderConfig{TimeKey: "ts", LevelKey: "level", CallerKey: "caller", MessageKey: "msg", StacktraceKey: "stacktrace", LineEnding: zapcore.DefaultLineEnding, EncodeDuration: zapcore.StringDurationEncoder, EncodeCaller: zapcore.ShortCallerEncoder, EncodeTime: shanghaiTimeEncoder(config.TimeLayout)}
	if config.ConsoleColor {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	}
	return zapcore.NewConsoleEncoder(encoderConfig)
}
func jsonEncoder(config Config) zapcore.Encoder {
	encoderConfig := zapcore.EncoderConfig{TimeKey: "ts", LevelKey: "level", CallerKey: "caller", MessageKey: "msg", StacktraceKey: "stacktrace", LineEnding: zapcore.DefaultLineEnding, EncodeLevel: zapcore.LowercaseLevelEncoder, EncodeDuration: zapcore.StringDurationEncoder, EncodeCaller: zapcore.ShortCallerEncoder, EncodeTime: shanghaiTimeEncoder(time.RFC3339Nano)}
	return zapcore.NewJSONEncoder(encoderConfig)
}
func shanghaiTimeEncoder(layout string) zapcore.TimeEncoder {
	location := time.FixedZone("CST", 8*3600)
	return func(value time.Time, encoder zapcore.PrimitiveArrayEncoder) {
		encoder.AppendString(value.In(location).Format(layout))
	}
}

func setGlobal(logger *zap.Logger, cleanup func() error) func() error {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cleanup == nil {
		cleanup = func() error { return logger.Sync() }
	}
	mu.Lock()
	oldCleanup := globalCleanup
	global = logger
	globalCleanup = cleanup
	mu.Unlock()
	return oldCleanup
}
func L() *zap.Logger                                { mu.RLock(); defer mu.RUnlock(); return global }
func S() *zap.SugaredLogger                         { return L().Sugar() }
func Sync()                                         { _ = L().Sync() }
func With(fields ...zapcore.Field) *zap.Logger      { return L().With(fields...) }
func Named(name string) *zap.Logger                 { return L().Named(name) }
func Debug(message string, fields ...zapcore.Field) { L().Debug(message, fields...) }
func Info(message string, fields ...zapcore.Field)  { L().Info(message, fields...) }
func Warn(message string, fields ...zapcore.Field)  { L().Warn(message, fields...) }
func Error(message string, fields ...zapcore.Field) { L().Error(message, fields...) }
func Fatal(message string, fields ...zapcore.Field) { L().Fatal(message, fields...) }
func Debugf(format string, args ...any)             { S().Debugf(format, args...) }
func Infof(format string, args ...any)              { S().Infof(format, args...) }
func Warnf(format string, args ...any)              { S().Warnf(format, args...) }
func Errorf(format string, args ...any)             { S().Errorf(format, args...) }
func Fatalf(format string, args ...any)             { S().Fatalf(format, args...) }
