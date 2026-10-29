//go:build !windows && !linux

package backend

import "github.com/Gubriiil/PureVector/internal/engine"

const nativeBackend = "none"

var platformBackends = map[string]func(*engine.Engine) (Backend, error){}
