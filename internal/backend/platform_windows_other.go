//go:build windows && !amd64

package backend

import "github.com/Gubriiil/PureVector/internal/engine"

// The Interception driver ships for x86/x64 only; this build targets
// another architecture.
const nativeBackend = "none"

var platformBackends = map[string]func(*engine.Engine) (Backend, error){}
