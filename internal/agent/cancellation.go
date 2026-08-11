package agent

import (
	"errors"
	"strings"
)

// ErrUserInterrupt identifies a cancellation requested for the active turn.
// Plain context.WithCancel callers remain supported and retain the historical
// interrupt behavior.
var ErrUserInterrupt = errors.New("user interrupt")

type userInterruptError struct {
	source string
}

func (e userInterruptError) Error() string {
	return "user interrupt (source: " + e.source + ")"
}

func (e userInterruptError) Unwrap() error {
	return ErrUserInterrupt
}

// NewUserInterrupt records the UI or client path that requested cancellation
// while preserving errors.Is(err, ErrUserInterrupt) compatibility.
func NewUserInterrupt(source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return ErrUserInterrupt
	}
	return userInterruptError{source: source}
}

// UserInterruptSource extracts the source attached by NewUserInterrupt.
func UserInterruptSource(err error) string {
	var tagged userInterruptError
	if errors.As(err, &tagged) {
		return tagged.source
	}
	return ""
}

// ErrServiceShutdown identifies cancellation caused by tearing down the
// service or its owning client. It must not be presented as a user interrupt.
var ErrServiceShutdown = errors.New("service shutdown")
