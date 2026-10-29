//go:build linux

package backend

import "github.com/Gubriiil/PureVector/internal/engine"

const nativeBackend = "evdev"

var platformBackends = map[string]func(*engine.Engine) (Backend, error){
	"evdev": newEvdev,
}
