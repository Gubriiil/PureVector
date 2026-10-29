//go:build windows && amd64

package backend

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/Gubriiil/PureVector/internal/engine"
)

// Windows backend using the Interception driver
// (https://github.com/oblitum/Interception). Interception is a signed
// keyboard/mouse class filter driver; its user-mode library lets a process
// receive every mouse packet before Windows processes it and send a modified
// packet on. Raw Input – and therefore every game – sees the modified
// packet as if it came from the physical mouse.

var errInterceptionUnavailable = errors.New("Interception-Treiber nicht erreichbar: nicht installiert, Neustart nach der Installation fehlt, " +
	"oder ein anderes Programm verwendet Interception bereits (nur ein Programm gleichzeitig möglich)")

type icDLL struct {
	ctx         uintptr
	create      uintptr
	destroy     uintptr
	setFilter   uintptr
	waitTimeout uintptr
	send        uintptr
	receive     uintptr
	hwid        uintptr
}

var (
	icPredicateOnce sync.Once
	icPredicate     uintptr

	modKernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procQueryPerformanceCounter   = modKernel32.NewProc("QueryPerformanceCounter")
	procQueryPerformanceFrequency = modKernel32.NewProc("QueryPerformanceFrequency")
	procGetCurrentThread          = modKernel32.NewProc("GetCurrentThread")
	procSetThreadPriority         = modKernel32.NewProc("SetThreadPriority")
)

// predicate selects all mouse device slots for interception_set_filter.
func predicate() uintptr {
	icPredicateOnce.Do(func() {
		icPredicate = syscall.NewCallback(func(dev uintptr) uintptr {
			if icIsMouse(int(int32(dev))) {
				return 1
			}
			return 0
		})
	})
	return icPredicate
}

func findInterceptionDLL() (string, error) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "interception.dll"),
			filepath.Join(dir, "library", "x64", "interception.dll"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "interception.dll"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("interception.dll nicht gefunden – bitte die Datei library\\x64\\interception.dll aus dem Interception-Download neben omnivector.exe legen")
}

func loadInterception() (*icDLL, error) {
	path, err := findInterceptionDLL()
	if err != nil {
		return nil, err
	}
	dll, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("interception.dll konnte nicht geladen werden (%s): %w", path, err)
	}
	d := &icDLL{}
	for _, p := range []struct {
		name string
		dst  *uintptr
	}{
		{"interception_create_context", &d.create},
		{"interception_destroy_context", &d.destroy},
		{"interception_set_filter", &d.setFilter},
		{"interception_wait_with_timeout", &d.waitTimeout},
		{"interception_send", &d.send},
		{"interception_receive", &d.receive},
		{"interception_get_hardware_id", &d.hwid},
	} {
		proc, err := dll.FindProc(p.name)
		if err != nil {
			return nil, fmt.Errorf("interception.dll: Funktion %s fehlt: %w", p.name, err)
		}
		*p.dst = proc.Addr()
	}
	ctx, _, _ := syscall.SyscallN(d.create)
	if ctx == 0 {
		return nil, errInterceptionUnavailable
	}
	d.ctx = ctx
	return d, nil
}

func (d *icDLL) startFiltering() {
	syscall.SyscallN(d.setFilter, d.ctx, predicate(), icFilterMouseAll)
}

func (d *icDLL) Wait(timeoutMs uint32) int {
	r, _, _ := syscall.SyscallN(d.waitTimeout, d.ctx, uintptr(timeoutMs))
	return int(int32(r))
}

func (d *icDLL) Receive(dev int, s *icMouseStroke) int {
	r, _, _ := syscall.SyscallN(d.receive, d.ctx, uintptr(dev), uintptr(unsafe.Pointer(s)), 1)
	return int(int32(r))
}

func (d *icDLL) Send(dev int, s *icMouseStroke) int {
	r, _, _ := syscall.SyscallN(d.send, d.ctx, uintptr(dev), uintptr(unsafe.Pointer(s)), 1)
	return int(int32(r))
}

func (d *icDLL) HardwareID(dev int) string {
	var buf [512]uint16
	r, _, _ := syscall.SyscallN(d.hwid, d.ctx, uintptr(dev), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2))
	n := int(uint32(r)) / 2
	if n <= 0 {
		return ""
	}
	if n > len(buf) {
		n = len(buf)
	}
	return utf16MultiSzFirst(buf[:n])
}

func (d *icDLL) Close() {
	if d.ctx == 0 {
		return
	}
	syscall.SyscallN(d.setFilter, d.ctx, predicate(), 0)
	syscall.SyscallN(d.destroy, d.ctx)
	d.ctx = 0
}

// qpcClock returns a monotonic nanosecond clock based on
// QueryPerformanceCounter.
func qpcClock() func() int64 {
	var freq int64
	procQueryPerformanceFrequency.Call(uintptr(unsafe.Pointer(&freq)))
	if freq <= 0 {
		freq = 10_000_000
	}
	return func() int64 {
		var c int64
		procQueryPerformanceCounter.Call(uintptr(unsafe.Pointer(&c)))
		return (c/freq)*1_000_000_000 + (c%freq)*1_000_000_000/freq
	}
}

type interceptionBackend struct{ eng *engine.Engine }

func newInterception(eng *engine.Engine) (Backend, error) {
	return &interceptionBackend{eng: eng}, nil
}

func (b *interceptionBackend) Name() string { return "interception" }

func (b *interceptionBackend) Run(ctx context.Context) error {
	d, err := loadInterception()
	if err != nil {
		b.eng.SetBackendStatus(engine.BackendStatus{Name: "interception", Error: err.Error()})
		return err
	}
	defer d.Close()
	names := hidProductNames()
	loop := &icLoop{drv: d, eng: b.eng, now: qpcClock(), logf: log.Printf,
		names: func(key string) string { return names[key] }}
	// Register connected mice right away so the UI lists them.
	for dev := icMaxKeyboard + 1; dev <= icMaxDevice; dev++ {
		if d.HardwareID(dev) != "" {
			loop.resolve(dev, nil)
		}
	}
	d.startFiltering()
	b.eng.SetBackendStatus(engine.BackendStatus{Name: "interception", Running: true,
		Detail: "Interception-Treiber aktiv – jede Maus wird auf Treiberebene erfasst (Desktop, Raw Input, Spiele)."})

	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		const threadPriorityHighest = 2
		h, _, _ := procGetCurrentThread.Call()
		procSetThreadPriority.Call(h, threadPriorityHighest)
		loop.run(ctx)
	}()
	<-done
	b.eng.SetBackendStatus(engine.BackendStatus{Name: "interception", Running: false, Detail: "beendet"})
	return nil
}

func (b *interceptionBackend) ListDevices() ([]DeviceDesc, error) {
	d, err := loadInterception()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names := hidProductNames()
	var out []DeviceDesc
	for dev := icMaxKeyboard + 1; dev <= icMaxDevice; dev++ {
		hwid := d.HardwareID(dev)
		if hwid == "" {
			continue
		}
		key, name := describeHardwareID(hwid, dev)
		if n := names[key]; n != "" {
			name = n
		}
		out = append(out, DeviceDesc{Key: key, Name: name, Detail: hwid})
	}
	return out, nil
}
