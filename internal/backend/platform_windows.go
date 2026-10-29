//go:build windows && amd64

package backend

import "github.com/Gubriiil/PureVector/internal/engine"

const nativeBackend = "interception"

var platformBackends = map[string]func(*engine.Engine) (Backend, error){
	"interception": newInterception,
}
