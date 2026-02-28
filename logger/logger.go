package logger

import "fmt"

// Level represents log severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// ParseLevel parses a level string. Defaults to LevelInfo on unknown input.
func ParseLevel(s string) Level {
	switch s {
	case "debug", "DEBUG":
		return LevelDebug
	case "info", "INFO":
		return LevelInfo
	case "warn", "WARN", "warning", "WARNING":
		return LevelWarn
	case "error", "ERROR":
		return LevelError
	default:
		return LevelInfo
	}
}

// Field is a structured key-value pair attached to a log entry.
type Field struct {
	Key   string
	Value any
}

// Convenience constructors for Field.
func String(key, val string) Field   { return Field{Key: key, Value: val} }
func Int(key string, val int) Field  { return Field{Key: key, Value: val} }
func Int64(key string, val int64) Field { return Field{Key: key, Value: val} }
func Bool(key string, val bool) Field { return Field{Key: key, Value: val} }
func Any(key string, val any) Field  { return Field{Key: key, Value: val} }

func Err(err error) Field {
	if err == nil {
		return Field{Key: "error", Value: ""}
	}
	return Field{Key: "error", Value: err.Error()}
}

// Logger is the interface all log backends implement.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	WithFields(fields ...Field) Logger
	Close() error
}

// Multi wraps multiple loggers, dispatching each log call to all of them.
func Multi(loggers ...Logger) Logger {
	return &multiLogger{loggers: loggers}
}

type multiLogger struct {
	loggers []Logger
}

func (m *multiLogger) Debug(msg string, fields ...Field) {
	for _, l := range m.loggers {
		l.Debug(msg, fields...)
	}
}

func (m *multiLogger) Info(msg string, fields ...Field) {
	for _, l := range m.loggers {
		l.Info(msg, fields...)
	}
}

func (m *multiLogger) Warn(msg string, fields ...Field) {
	for _, l := range m.loggers {
		l.Warn(msg, fields...)
	}
}

func (m *multiLogger) Error(msg string, fields ...Field) {
	for _, l := range m.loggers {
		l.Error(msg, fields...)
	}
}

func (m *multiLogger) WithFields(fields ...Field) Logger {
	wrapped := make([]Logger, len(m.loggers))
	for i, l := range m.loggers {
		wrapped[i] = l.WithFields(fields...)
	}
	return &multiLogger{loggers: wrapped}
}

func (m *multiLogger) Close() error {
	var firstErr error
	for _, l := range m.loggers {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Nop returns a logger that discards everything.
func Nop() Logger { return &nopLogger{} }

type nopLogger struct{}

func (n *nopLogger) Debug(string, ...Field) {}
func (n *nopLogger) Info(string, ...Field)  {}
func (n *nopLogger) Warn(string, ...Field)  {}
func (n *nopLogger) Error(string, ...Field) {}
func (n *nopLogger) WithFields(...Field) Logger { return n }
func (n *nopLogger) Close() error               { return nil }

// formatFields renders fields as key=value pairs for human-readable output.
func FormatFields(fields []Field) string {
	if len(fields) == 0 {
		return ""
	}
	s := ""
	for _, f := range fields {
		s += fmt.Sprintf(" %s=%v", f.Key, f.Value)
	}
	return s
}
