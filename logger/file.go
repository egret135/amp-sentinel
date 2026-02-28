package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileConfig configures the file logger.
type FileConfig struct {
	Dir        string
	Level      Level
	MaxSizeMB  int // max size per file in MB, 0 = unlimited
	MaxAgeDays int // delete files older than N days, 0 = never delete
}

// fileCore holds the shared mutable state for all FileLogger instances
// derived via WithFields. A single mutex protects the file handle and
// rotation counters so concurrent loggers never race.
type fileCore struct {
	mu         sync.Mutex
	dir        string
	maxSizeMB  int
	maxAgeDays int
	file       *os.File
	currentDay string
	written    int64
}

// FileLogger writes log lines to daily-rotated files.
type FileLogger struct {
	level      Level
	baseFields []Field
	core       *fileCore
}

// NewFile creates a file logger that writes to daily-rotated log files.
func NewFile(cfg FileConfig) (*FileLogger, error) {
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	c := &fileCore{
		dir:        cfg.Dir,
		maxSizeMB:  cfg.MaxSizeMB,
		maxAgeDays: cfg.MaxAgeDays,
	}
	if err := c.openForDay(time.Now()); err != nil {
		return nil, err
	}
	return &FileLogger{level: cfg.Level, core: c}, nil
}

func (l *FileLogger) Debug(msg string, fields ...Field) { l.log(LevelDebug, msg, fields) }
func (l *FileLogger) Info(msg string, fields ...Field)  { l.log(LevelInfo, msg, fields) }
func (l *FileLogger) Warn(msg string, fields ...Field)  { l.log(LevelWarn, msg, fields) }
func (l *FileLogger) Error(msg string, fields ...Field) { l.log(LevelError, msg, fields) }

func (l *FileLogger) WithFields(fields ...Field) Logger {
	merged := make([]Field, 0, len(l.baseFields)+len(fields))
	merged = append(merged, l.baseFields...)
	merged = append(merged, fields...)
	return &FileLogger{
		level:      l.level,
		baseFields: merged,
		core:       l.core,
	}
}

func (l *FileLogger) Close() error {
	l.core.mu.Lock()
	defer l.core.mu.Unlock()
	if l.core.file != nil {
		err := l.core.file.Close()
		l.core.file = nil
		return err
	}
	return nil
}

func (l *FileLogger) log(level Level, msg string, fields []Field) {
	if level < l.level {
		return
	}

	allFields := make([]Field, 0, len(l.baseFields)+len(fields))
	allFields = append(allFields, l.baseFields...)
	allFields = append(allFields, fields...)

	now := time.Now()
	ts := now.Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("%s [%-5s] %s%s\n", ts, level.String(), msg, FormatFields(allFields))

	c := l.core
	c.mu.Lock()
	defer c.mu.Unlock()

	today := now.Format("2006-01-02")
	if today != c.currentDay {
		c.rotate(now)
	} else if c.maxSizeMB > 0 && c.written >= int64(c.maxSizeMB)*1024*1024 {
		c.rotateSizeExceeded(now)
	}

	if c.file != nil {
		n, _ := c.file.WriteString(line)
		c.written += int64(n)
	}
}

func (c *fileCore) openForDay(t time.Time) error {
	day := t.Format("2006-01-02")
	name := filepath.Join(c.dir, fmt.Sprintf("sentinel-%s.log", day))

	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}

	c.file = f
	c.currentDay = day
	c.written = info.Size()
	return nil
}

// rotate closes the current file and opens a new one for the given day.
// Must be called with c.mu held.
func (c *fileCore) rotate(t time.Time) {
	if c.file != nil {
		c.file.Close()
	}
	if err := c.openForDay(t); err != nil {
		fmt.Fprintf(os.Stderr, "file logger rotate failed: %v\n", err)
	}
	c.cleanOld()
}

// rotateSizeExceeded renames the current file with a numeric suffix and opens a fresh one.
// Must be called with c.mu held.
func (c *fileCore) rotateSizeExceeded(t time.Time) {
	day := t.Format("2006-01-02")
	if c.file != nil {
		c.file.Close()
	}

	base := filepath.Join(c.dir, fmt.Sprintf("sentinel-%s.log", day))
	for i := 1; ; i++ {
		dest := filepath.Join(c.dir, fmt.Sprintf("sentinel-%s.%d.log", day, i))
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			os.Rename(base, dest)
			break
		}
	}

	if err := c.openForDay(t); err != nil {
		fmt.Fprintf(os.Stderr, "file logger rotate failed: %v\n", err)
	}
}

// cleanOld removes log files older than maxAgeDays.
// Must be called with c.mu held.
func (c *fileCore) cleanOld() {
	if c.maxAgeDays <= 0 {
		return
	}

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -c.maxAgeDays)
	prefix := "sentinel-"
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		dateStr := strings.TrimPrefix(name, prefix)
		if idx := strings.Index(dateStr, "."); idx >= 0 {
			dateStr = dateStr[:idx]
		}
		fileDate, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if fileDate.Before(cutoff) {
			os.Remove(filepath.Join(c.dir, name))
		}
	}
}

// LogFiles returns all log file paths sorted by name.
func (l *FileLogger) LogFiles() []string {
	l.core.mu.Lock()
	defer l.core.mu.Unlock()

	entries, err := os.ReadDir(l.core.dir)
	if err != nil {
		return nil
	}

	var files []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sentinel-") && strings.HasSuffix(e.Name(), ".log") {
			files = append(files, filepath.Join(l.core.dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files
}
