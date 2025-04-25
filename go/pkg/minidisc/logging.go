// Simple logging interface to give library clients control over what to log.
package minidisc

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
