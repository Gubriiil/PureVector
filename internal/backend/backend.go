// Package backend hooks OmniVector into the operating system's mouse input
// path, below every application, so that any connected mouse is covered:
//
//   - Windows: the Interception kernel filter driver (mouclass level). The
//     transformed motion is seen by the desktop, by Raw Input and therefore
//     by games, exactly like motion coming from the mouse itself.
//   - Linux: every mouse event device is grabbed exclusively (EVIOCGRAB) and
//     re-emitted through a uinput clone with the same name and IDs.
//   - none: no input hook; calibration, profiles and exports still work.
package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/Gubriiil/PureVector/internal/engine"
)

// Backend is an input hook.
type Backend interface {
	// Name identifies the backend ("interception", "evdev", "none").
	Name() string
	// Run hooks input until ctx is cancelled. Normal input must be fully
	// restored when Run returns.
	Run(ctx context.Context) error
	// ListDevices returns the pointing devices the backend can see.
	ListDevices() ([]DeviceDesc, error)
}

// DeviceDesc describes a device for "omnivector devices".
type DeviceDesc struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// New creates the requested backend. "auto" picks the native backend of
// the platform.
func New(name string, eng *engine.Engine) (Backend, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "auto" {
		name = nativeBackend
	}
	if name == "none" {
		return &noneBackend{eng: eng}, nil
	}
	if f, ok := platformBackends[name]; ok {
		return f(eng)
	}
	return nil, fmt.Errorf("Backend %q ist auf diesem System nicht verfügbar (verfügbar: %s, none)", name, nativeBackend)
}

// noneBackend keeps the UI running without touching input.
type noneBackend struct{ eng *engine.Engine }

func (b *noneBackend) Name() string { return "none" }

func (b *noneBackend) Run(ctx context.Context) error {
	b.eng.SetBackendStatus(engine.BackendStatus{Name: "none", Running: false,
		Detail: "Kein Eingabe-Hook aktiv – Kalibrierung, Profile und Raw-Accel-Export funktionieren trotzdem."})
	<-ctx.Done()
	return nil
}

func (b *noneBackend) ListDevices() ([]DeviceDesc, error) { return nil, nil }
