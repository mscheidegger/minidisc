// Simple logging interface to give library clients control over what to log.
package minidisc

import (
	"fmt"
	"log"
)

type Logger interface {
	Debugf(fmt string, args ...any)
	Infof(fmt string, args ...any)
	Warnf(fmt string, args ...any)
	Errorf(fmt string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debugf(fmt string, args ...any) {}
func (noopLogger) Infof(fmt string, args ...any)  {}
func (noopLogger) Warnf(fmt string, args ...any)  {}
func (noopLogger) Errorf(fmt string, args ...any) {}

var logger Logger = noopLogger{}

func SetLogger(l Logger) {
	logger = l
}

type LevelLogger struct {
	Level int
}

func (l LevelLogger) Debugf(format string, args ...any) { l.log(0, format, args...) }
func (l LevelLogger) Infof(format string, args ...any)  { l.log(1, format, args...) }
func (l LevelLogger) Warnf(format string, args ...any)  { l.log(2, format, args...) }
func (l LevelLogger) Errorf(format string, args ...any) { l.log(3, format, args...) }

func (l LevelLogger) log(level int, format string, args ...any) {
	if level < l.Level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	log.Printf("%s: %s", levelStr(level), msg)
}

func levelStr(level int) string {
	switch level {
	case 0:
		return "DEBUG"
	case 1:
		return "INFO"
	case 2:
		return "WARN"
	case 3:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}
