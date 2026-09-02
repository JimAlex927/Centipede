// Package banner renders a small, terminal-safe startup banner.
package banner

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultWidth = 64

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

var defaultLogo = []string{
	`   CCC  EEEEE N   N TTTTT III PPPP  EEEEE DDDD  EEEEE`,
	`  C     E     NN  N   T    I  P   P E     D   D E    `,
	` C      EEEE  N N N   T    I  PPPP  EEEE  D   D EEEE `,
	`  C     E     N  NN   T    I  P     E     D   D E    `,
	`   CCC  EEEEE N   N   T   III P     EEEEE DDDD  EEEEE`,
}

type Field struct {
	Label string
	Value string
}

type Config struct {
	Title     string
	Logo      []string
	Subtitle  string
	Version   string
	Env       string
	Port      string
	Accent    string
	Fields    []Field
	Width     int
	Color     bool
	Writer    io.Writer
	StartedAt time.Time
}

type Option func(*Config)

func WithLogo(lines ...string) Option {
	return func(c *Config) { c.Logo = append([]string{}, lines...) }
}
func WithSubtitle(value string) Option     { return func(c *Config) { c.Subtitle = value } }
func WithVersion(value string) Option      { return func(c *Config) { c.Version = value } }
func WithEnv(value string) Option          { return func(c *Config) { c.Env = value } }
func WithPort(port int) Option             { return func(c *Config) { c.Port = strconv.Itoa(port) } }
func WithPortText(value string) Option     { return func(c *Config) { c.Port = value } }
func WithAccent(value string) Option       { return func(c *Config) { c.Accent = value } }
func WithWidth(value int) Option           { return func(c *Config) { c.Width = value } }
func WithColor(value bool) Option          { return func(c *Config) { c.Color = value } }
func WithWriter(writer io.Writer) Option   { return func(c *Config) { c.Writer = writer } }
func WithStartedAt(value time.Time) Option { return func(c *Config) { c.StartedAt = value } }
func WithField(label, value string) Option {
	return func(c *Config) { c.Fields = append(c.Fields, Field{Label: label, Value: value}) }
}
func WithFields(fields ...Field) Option {
	return func(c *Config) { c.Fields = append(c.Fields, fields...) }
}

func Print(title string, opts ...Option) error {
	cfg := buildConfig(title, opts...)
	_, err := io.WriteString(cfg.Writer, render(cfg))
	return err
}

func Render(title string, opts ...Option) string {
	return render(buildConfig(title, opts...))
}

func render(cfg Config) string {
	var builder strings.Builder

	writeLogo(&builder, cfg)
	builder.WriteString("\n")
	writeCentered(&builder, cfg, style(cfg, "\033[97;1m", cfg.Title))
	if cfg.Subtitle != "" {
		writeCentered(&builder, cfg, style(cfg, "\033[90m", cfg.Subtitle))
	}
	status := strings.ToUpper(strings.TrimSpace(cfg.Accent))
	if status == "" {
		status = "READY"
	}
	writeCentered(&builder, cfg, style(cfg, "\033[92;1m", status)+style(cfg, "\033[90m", "  ·  "+cfg.StartedAt.Format("2006-01-02 15:04:05")))
	if facts := factsLine(cfg); facts != "" {
		writeCentered(&builder, cfg, facts)
	}
	return builder.String()
}

func buildConfig(title string, opts ...Option) Config {
	cfg := Config{
		Title:     strings.TrimSpace(title),
		Logo:      append([]string{}, defaultLogo...),
		Accent:    "READY",
		Width:     defaultWidth,
		Color:     false,
		Writer:    os.Stdout,
		StartedAt: time.Now(),
	}
	if cfg.Title == "" {
		cfg.Title = "application"
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if len(cfg.Logo) == 0 {
		cfg.Logo = append([]string{}, defaultLogo...)
	}
	if cfg.Width <= 0 {
		cfg.Width = defaultWidth
	}
	if cfg.Writer == nil {
		cfg.Writer = io.Discard
	}
	return cfg
}

func writeLogo(builder *strings.Builder, cfg Config) {
	blockWidth := 0
	for _, line := range cfg.Logo {
		if width := visibleWidth(line); width > blockWidth {
			blockWidth = width
		}
	}
	left := 0
	if cfg.Width > blockWidth {
		left = (cfg.Width - blockWidth) / 2
	}
	segments := letterSegments(cfg.Logo)
	for _, line := range cfg.Logo {
		builder.WriteString(strings.Repeat(" ", left))
		builder.WriteString(colorizeLogoLine(cfg, line, segments))
		builder.WriteString("\n")
	}
}

var letterPalette = []string{
	"\033[91;1m", "\033[93;1m", "\033[92;1m", "\033[96;1m",
	"\033[94;1m", "\033[95;1m", "\033[97;1m", "\033[31;1m",
}

var defaultLogoSegments = [][2]int{
	{0, 8}, {8, 14}, {14, 21}, {21, 30}, {30, 36}, {36, 43}, {43, 49}, {49, 56},
}

func letterSegments(lines []string) [][2]int {
	if sameLogo(lines, defaultLogo) {
		return append([][2]int(nil), defaultLogoSegments...)
	}
	width := 0
	for _, line := range lines {
		if current := len([]rune(line)); current > width {
			width = current
		}
	}
	if width == 0 {
		return nil
	}
	occupied := make([]bool, width)
	for _, line := range lines {
		for index, char := range []rune(line) {
			if char != ' ' {
				occupied[index] = true
			}
		}
	}
	var segments [][2]int
	start := -1
	for index := 0; index <= width; index++ {
		isOccupied := index < width && occupied[index]
		switch {
		case isOccupied && start == -1:
			start = index
		case !isOccupied && start != -1:
			segments = append(segments, [2]int{start, index})
			start = -1
		}
	}
	return segments
}

func sameLogo(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func colorizeLogoLine(cfg Config, line string, segments [][2]int) string {
	if !cfg.Color || len(segments) == 0 {
		return line
	}
	runes := []rune(line)
	var builder strings.Builder
	segmentIndex := 0
	column := 0
	for column < len(runes) {
		for segmentIndex < len(segments) && column >= segments[segmentIndex][1] {
			segmentIndex++
		}
		if segmentIndex < len(segments) && column >= segments[segmentIndex][0] && column < segments[segmentIndex][1] {
			start, end := segments[segmentIndex][0], segments[segmentIndex][1]
			if start >= len(runes) {
				break
			}
			if end > len(runes) {
				end = len(runes)
			}
			builder.WriteString(letterPalette[segmentIndex%len(letterPalette)])
			builder.WriteString(string(runes[start:end]))
			builder.WriteString("\033[0m")
			column = end
			continue
		}
		builder.WriteRune(runes[column])
		column++
	}
	return builder.String()
}

func factsLine(cfg Config) string {
	fields := make([]Field, 0, len(cfg.Fields)+3)
	if cfg.Version != "" {
		fields = append(fields, Field{Label: "version", Value: cfg.Version})
	}
	if cfg.Env != "" {
		fields = append(fields, Field{Label: "env", Value: cfg.Env})
	}
	if cfg.Port != "" {
		fields = append(fields, Field{Label: "address", Value: cfg.Port})
	}
	fields = append(fields, cfg.Fields...)

	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		label := strings.TrimSpace(field.Label)
		value := strings.TrimSpace(field.Value)
		if label == "" && value == "" {
			continue
		}
		parts = append(parts, style(cfg, "\033[96m", label)+style(cfg, "\033[90m", "=")+value)
	}
	return strings.Join(parts, style(cfg, "\033[90m", "  |  "))
}

func writeCentered(builder *strings.Builder, cfg Config, text string) {
	padding := cfg.Width - visibleWidth(text)
	if padding < 0 {
		padding = 0
	}
	builder.WriteString(strings.Repeat(" ", padding/2))
	builder.WriteString(text)
	builder.WriteString("\n")
}

func style(cfg Config, color, text string) string {
	if !cfg.Color {
		return text
	}
	return color + text + "\033[0m"
}

func visibleWidth(value string) int {
	return len([]rune(ansiPattern.ReplaceAllString(value, "")))
}
