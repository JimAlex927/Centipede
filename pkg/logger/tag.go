package logger

import (
	"fmt"
	"sync"

	"go.uber.org/zap"
)

const ansiReset = "\033[0m"

type Color string

const (
	AutoColor     Color = ""
	Black         Color = "\033[30m"
	Red           Color = "\033[31m"
	Green         Color = "\033[32m"
	Yellow        Color = "\033[33m"
	Blue          Color = "\033[34m"
	Magenta       Color = "\033[35m"
	Cyan          Color = "\033[36m"
	White         Color = "\033[37m"
	BrightBlack   Color = "\033[90m"
	BrightRed     Color = "\033[91m"
	BrightGreen   Color = "\033[92m"
	BrightYellow  Color = "\033[93m"
	BrightBlue    Color = "\033[94m"
	BrightMagenta Color = "\033[95m"
	BrightCyan    Color = "\033[96m"
	BrightWhite   Color = "\033[97m"
)

func Color256(code uint8) Color { return Color(fmt.Sprintf("\033[38;5;%dm", code)) }
func RGB(red, green, blue uint8) Color {
	return Color(fmt.Sprintf("\033[38;2;%d;%d;%dm", red, green, blue))
}

var tagPalette = []string{"\033[96m", "\033[92m", "\033[95m", "\033[93m", "\033[94m", "\033[38;5;208m", "\033[38;5;213m", "\033[38;5;37m", "\033[38;5;141m", "\033[38;5;214m", "\033[38;5;82m", "\033[38;5;39m"}
var (
	tagMu     sync.Mutex
	tagColors = map[string]tagColorAssignment{}
	tagNext   int
)

type tagColorAssignment struct {
	code     string
	explicit bool
}

func colorFor(name string) string {
	tagMu.Lock()
	defer tagMu.Unlock()
	if assignment, ok := tagColors[name]; ok {
		return assignment.code
	}
	code := tagPalette[tagNext%len(tagPalette)]
	tagNext++
	tagColors[name] = tagColorAssignment{code: code}
	return code
}
func assignTagColor(name string, color Color) {
	if color == AutoColor {
		return
	}
	tagMu.Lock()
	defer tagMu.Unlock()
	current, exists := tagColors[name]
	if exists && current.explicit {
		return
	}
	tagColors[name] = tagColorAssignment{code: string(color), explicit: true}
}

type TaggedLogger struct {
	name   string
	fields []zap.Field
}

func Tag(name string, colors ...Color) *TaggedLogger {
	if len(colors) > 0 {
		assignTagColor(name, colors[0])
	}
	return &TaggedLogger{name: name}
}
func (logger *TaggedLogger) Debugf(format string, args ...any) {
	logger.sugar().Debugf(format, args...)
}
func (logger *TaggedLogger) Infof(format string, args ...any) { logger.sugar().Infof(format, args...) }
func (logger *TaggedLogger) Warnf(format string, args ...any) { logger.sugar().Warnf(format, args...) }
func (logger *TaggedLogger) Errorf(format string, args ...any) {
	logger.sugar().Errorf(format, args...)
}
func (logger *TaggedLogger) Fatalf(format string, args ...any) {
	logger.sugar().Fatalf(format, args...)
}
func (logger *TaggedLogger) Debug(message string, fields ...zap.Field) {
	logger.logger().Debug(message, fields...)
}
func (logger *TaggedLogger) Info(message string, fields ...zap.Field) {
	logger.logger().Info(message, fields...)
}
func (logger *TaggedLogger) Warn(message string, fields ...zap.Field) {
	logger.logger().Warn(message, fields...)
}
func (logger *TaggedLogger) Error(message string, fields ...zap.Field) {
	logger.logger().Error(message, fields...)
}
func (logger *TaggedLogger) With(fields ...zap.Field) *TaggedLogger {
	allFields := append(append([]zap.Field{}, logger.fields...), fields...)
	return &TaggedLogger{name: logger.name, fields: allFields}
}
func (logger *TaggedLogger) logger() *zap.Logger {
	fields := append([]zap.Field{zap.String("tag", logger.name)}, logger.fields...)
	return L().With(fields...)
}
func (logger *TaggedLogger) sugar() *zap.SugaredLogger { return logger.logger().Sugar() }
