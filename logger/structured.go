package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// structuredCore holds the shared mutable state for all StructuredLogger
// instances derived via WithFields.
type structuredCore struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
}

// StructuredLogger writes one JSON object per log line.
type StructuredLogger struct {
	level      Level
	baseFields []Field
	core       *structuredCore
}

// NewStructured creates a structured JSON logger writing to the given path.
func NewStructured(path string, level Level) (*StructuredLogger, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open structured log: %w", err)
	}

	c := &structuredCore{
		file: f,
		enc:  json.NewEncoder(f),
	}

	return &StructuredLogger{
		level: level,
		core:  c,
	}, nil
}

func (s *StructuredLogger) Debug(msg string, fields ...Field) { s.log(LevelDebug, msg, fields) }
func (s *StructuredLogger) Info(msg string, fields ...Field)  { s.log(LevelInfo, msg, fields) }
func (s *StructuredLogger) Warn(msg string, fields ...Field)  { s.log(LevelWarn, msg, fields) }
func (s *StructuredLogger) Error(msg string, fields ...Field) { s.log(LevelError, msg, fields) }

func (s *StructuredLogger) WithFields(fields ...Field) Logger {
	merged := make([]Field, 0, len(s.baseFields)+len(fields))
	merged = append(merged, s.baseFields...)
	merged = append(merged, fields...)
	return &StructuredLogger{
		level:      s.level,
		baseFields: merged,
		core:       s.core,
	}
}

func (s *StructuredLogger) Close() error {
	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	if s.core.file != nil {
		err := s.core.file.Close()
		s.core.file = nil
		return err
	}
	return nil
}

func (s *StructuredLogger) log(level Level, msg string, fields []Field) {
	if level < s.level {
		return
	}

	allFields := make([]Field, 0, len(s.baseFields)+len(fields))
	allFields = append(allFields, s.baseFields...)
	allFields = append(allFields, fields...)

	entry := make(map[string]any, 3+len(allFields))
	entry["time"] = time.Now().UTC().Format(time.RFC3339)
	entry["level"] = level.String()
	entry["msg"] = msg
	for _, f := range allFields {
		entry[f.Key] = f.Value
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	s.core.enc.Encode(entry)
}
