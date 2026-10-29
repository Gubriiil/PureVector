//go:build windows

package hotkey

import (
	"context"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procRegisterHotKey     = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = user32.NewProc("UnregisterHotKey")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procPeekMessageW       = user32.NewProc("PeekMessageW")
	procPostThreadMessageW = user32.NewProc("PostThreadMessageW")
	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
	procBeep               = kernel32.NewProc("Beep")
)

const (
	wmHotkey = 0x0312
	wmQuit   = 0x0012
	wmUser   = 0x0400
)

type msg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       [2]int32
	lPrivate uint32
}

// Run registers a global hotkey and calls fn on every press until ctx is
// cancelled. It returns once the hotkey is registered (or failed).
func Run(ctx context.Context, spec string, fn func()) error {
	mods, vk, err := Parse(spec)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var m msg
		// Make sure this thread has a message queue before anyone posts to it.
		procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, wmUser, wmUser, 0)
		tid, _, _ := procGetCurrentThreadID.Call()
		if r, _, e := procRegisterHotKey.Call(0, 1, uintptr(mods|ModNoRepeat), uintptr(vk)); r == 0 {
			errc <- e
			return
		}
		defer procUnregisterHotKey.Call(0, 1)
		errc <- nil
		go func() {
			<-ctx.Done()
			procPostThreadMessageW.Call(tid, wmQuit, 0, 0)
		}()
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			if m.message == wmHotkey {
				fn()
			}
		}
	}()
	return <-errc
}

// Feedback beeps: high tone = on, low tone = off.
func Feedback(on bool) {
	go func() {
		if on {
			procBeep.Call(880, 70)
			procBeep.Call(1320, 70)
		} else {
			procBeep.Call(660, 70)
			procBeep.Call(440, 90)
		}
	}()
}
