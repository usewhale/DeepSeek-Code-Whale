package agent

import "errors"

// ErrUserInterrupt identifies a cancellation requested for the active turn.
// Plain context.WithCancel callers remain supported and retain the historical
// interrupt behavior.
var ErrUserInterrupt = errors.New("user interrupt")

// ErrServiceShutdown identifies cancellation caused by tearing down the
// service or its owning client. It must not be presented as a user interrupt.
var ErrServiceShutdown = errors.New("service shutdown")
