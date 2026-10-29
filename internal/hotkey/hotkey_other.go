//go:build !windows

package hotkey

import "context"

// Run is not supported on this platform; use "omnivector toggle" (e.g.
// bound to a desktop shortcut) instead.
func Run(ctx context.Context, spec string, fn func()) error {
	if _, _, err := Parse(spec); err != nil {
		return err
	}
	return ErrUnsupported
}

// Feedback is a no-op on this platform.
func Feedback(on bool) {}
