package logx

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	base      *log.Logger
	threshold int
	mu        sync.Mutex
}

const (
	levelDebug = iota
	levelInfo
	levelWarn
	levelError
)

func New(output io.Writer, level string) *Logger {
	threshold := levelInfo
	switch strings.ToLower(level) {
	case "debug":
		threshold = levelDebug
	case "warn":
		threshold = levelWarn
	case "error":
		threshold = levelError
	}
	return &Logger{base: log.New(output, "", 0), threshold: threshold}
}

func (l *Logger) Debug(message string, fields ...any) {
	l.write(levelDebug, "debug", message, fields...)
}
func (l *Logger) Info(message string, fields ...any) { l.write(levelInfo, "info", message, fields...) }
func (l *Logger) Warn(message string, fields ...any) { l.write(levelWarn, "warn", message, fields...) }
func (l *Logger) Error(message string, fields ...any) {
	l.write(levelError, "error", message, fields...)
}

func (l *Logger) write(level int, name, message string, fields ...any) {
	if level < l.threshold {
		return
	}
	record := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "level": name, "message": message}
	for index := 0; index < len(fields); index += 2 {
		key := "field"
		if value, ok := fields[index].(string); ok && value != "" {
			key = value
		}
		if index+1 < len(fields) {
			record[key] = logValue(fields[index+1])
		} else {
			record[key] = nil
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		encoded = []byte(`{"level":"error","message":"could not encode log record"}`)
	}
	l.mu.Lock()
	l.base.Print(string(encoded))
	l.mu.Unlock()
}

func logValue(value any) any {
	switch typed := value.(type) {
	case error:
		if typed == nil {
			return nil
		}
		return typed.Error()
	case fmt.Stringer:
		return typed.String()
	default:
		return value
	}
}
