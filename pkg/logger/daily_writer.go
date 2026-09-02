package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

type DailyWriter struct {
	mu                                sync.Mutex
	dir, baseName, levelTag           string
	dailyRotate, alsoBySize           bool
	timeNow                           func() time.Time
	maxSizeMB, maxBackups, maxAgeDays int
	compress                          bool
	curDate                           string
	lj                                *lumberjack.Logger
	file                              *os.File
}

func NewDailyWriter(dir, baseName, levelTag string, dailyRotate, alsoBySize bool, maxSizeMB, maxBackups, maxAgeDays int, compress bool) (*DailyWriter, error) {
	if dir == "" {
		dir = "./logs"
	}
	if baseName == "" {
		baseName = "app"
	}
	if levelTag == "" {
		levelTag = "ALL"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("logger: mkdir %s: %w", dir, err)
	}
	writer := &DailyWriter{dir: dir, baseName: baseName, levelTag: strings.ToUpper(levelTag), dailyRotate: dailyRotate, alsoBySize: alsoBySize, timeNow: time.Now, maxSizeMB: clampMin(maxSizeMB, 1), maxBackups: clampMin(maxBackups, 0), maxAgeDays: clampMin(maxAgeDays, 0), compress: compress}
	if err := writer.ensureOpenLocked(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (writer *DailyWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.dailyRotate {
		if date := writer.timeNow().Format("2006-01-02"); date != writer.curDate {
			_ = writer.closeLocked()
			writer.curDate = date
		}
	}
	if writer.lj == nil && writer.file == nil {
		if err := writer.ensureOpenLocked(); err != nil {
			return 0, err
		}
	}
	if writer.alsoBySize {
		return writer.lj.Write(data)
	}
	return writer.file.Write(data)
}

func (writer *DailyWriter) Sync() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.file != nil {
		return writer.file.Sync()
	}
	return nil
}
func (writer *DailyWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.closeLocked()
}

func (writer *DailyWriter) closeLocked() error {
	if writer.lj != nil {
		err := writer.lj.Close()
		writer.lj = nil
		return err
	}
	if writer.file != nil {
		err := writer.file.Close()
		writer.file = nil
		return err
	}
	return nil
}

func (writer *DailyWriter) ensureOpenLocked() error {
	date := ""
	if writer.dailyRotate {
		date = writer.timeNow().Format("2006-01-02")
		writer.curDate = date
	}
	filename := writer.buildFilename(date)
	if writer.alsoBySize {
		writer.lj = &lumberjack.Logger{Filename: filename, MaxSize: writer.maxSizeMB, MaxBackups: writer.maxBackups, MaxAge: writer.maxAgeDays, Compress: writer.compress, LocalTime: true}
		return nil
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("logger: open %s: %w", filename, err)
	}
	writer.file = file
	return nil
}

func (writer *DailyWriter) buildFilename(date string) string {
	parts := []string{writer.baseName, writer.levelTag}
	if writer.dailyRotate && date != "" {
		parts = append(parts, date)
	}
	return filepath.Join(writer.dir, strings.Join(parts, ".")+".log")
}
func clampMin(value, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}
