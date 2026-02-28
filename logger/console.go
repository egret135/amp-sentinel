package logger

import (
	"fmt"
	"os"
	"time"
)

// ConsoleLogger writes human-readable colored log lines to stderr.
type ConsoleLogger struct {
	level      Level
	color      bool
	baseFields []Field
}

// NewConsole creates a console logger with the given minimum level.
func NewConsole(level Level, color bool) *ConsoleLogger {
	return &ConsoleLogger{level: level, color: color}
}

func (c *ConsoleLogger) Debug(msg string, fields ...Field) { c.log(LevelDebug, msg, fields) }
func (c *ConsoleLogger) Info(msg string, fields ...Field)  { c.log(LevelInfo, msg, fields) }
func (c *ConsoleLogger) Warn(msg string, fields ...Field)  { c.log(LevelWarn, msg, fields) }
func (c *ConsoleLogger) Error(msg string, fields ...Field) { c.log(LevelError, msg, fields) }

func (c *ConsoleLogger) WithFields(fields ...Field) Logger {
	merged := make([]Field, 0, len(c.baseFields)+len(fields))
	merged = append(merged, c.baseFields...)
	merged = append(merged, fields...)
	return &ConsoleLogger{level: c.level, color: c.color, baseFields: merged}
}

func (c *ConsoleLogger) Close() error { return nil }

func (c *ConsoleLogger) log(level Level, msg string, fields []Field) {
	if level < c.level {
		return
	}

	allFields := make([]Field, 0, len(c.baseFields)+len(fields))
	allFields = append(allFields, c.baseFields...)
	allFields = append(allFields, fields...)

	ts := time.Now().Format("2006-01-02 15:04:05")
	levelStr := c.levelString(level)
	line := fmt.Sprintf("%s %s %s%s\n", ts, levelStr, msg, FormatFields(allFields))
	fmt.Fprint(os.Stderr, line)
}

func (c *ConsoleLogger) levelString(level Level) string {
	if !c.color {
		return fmt.Sprintf("[%-5s]", level.String())
	}
	var code string
	switch level {
	case LevelDebug:
		code = "\033[36m" // cyan
	case LevelInfo:
		code = "\033[32m" // green
	case LevelWarn:
		code = "\033[33m" // yellow
	case LevelError:
		code = "\033[31m" // red
	}
	return fmt.Sprintf("%s[%-5s]\033[0m", code, level.String())
}
