//go:build linux

package backend

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Gubriiil/PureVector/internal/engine"
)

// Linux backend: every mouse event device is grabbed exclusively and its
// events are re-emitted through a uinput clone (same name, bus, vendor and
// product, so desktop settings and hwdb quirks still apply). Motion is
// transformed on the way. If OmniVector exits or crashes, the kernel
// releases the grab and the original device works as before.

// physMarker tags the clones so they are never grabbed themselves.
const physMarker = "omnivector-virtual"

const (
	iocWrite = 1
	iocRead  = 2
)

// ioc encodes an ioctl request (asm-generic layout used by x86, x86-64,
// ARM and ARM64).
func ioc(dir, typ, nr, size uintptr) uintptr { return dir<<30 | size<<16 | typ<<8 | nr }

func eviocgbit(ev, size int) uintptr { return ioc(iocRead, 'E', uintptr(0x20+ev), uintptr(size)) }
func eviocgname(size int) uintptr    { return ioc(iocRead, 'E', 0x06, uintptr(size)) }
func eviocgphys(size int) uintptr    { return ioc(iocRead, 'E', 0x07, uintptr(size)) }
func eviocgprop(size int) uintptr    { return ioc(iocRead, 'E', 0x09, uintptr(size)) }
func eviocgkey(size int) uintptr     { return ioc(iocRead, 'E', 0x18, uintptr(size)) }
func eviocgabs(code int) uintptr     { return ioc(iocRead, 'E', uintptr(0x40+code), 24) }

var (
	eviocgid      = ioc(iocRead, 'E', 0x02, 8)
	eviocgrab     = ioc(iocWrite, 'E', 0x90, 4)
	eviocsclockid = ioc(iocWrite, 'E', 0xa0, 4)
	uiSetEvbit    = ioc(iocWrite, 'U', 100, 4)
	uiSetKeybit   = ioc(iocWrite, 'U', 101, 4)
	uiSetRelbit   = ioc(iocWrite, 'U', 102, 4)
	uiSetAbsbit   = ioc(iocWrite, 'U', 103, 4)
	uiSetMscbit   = ioc(iocWrite, 'U', 104, 4)
	uiSetPhys     = ioc(iocWrite, 'U', 108, unsafe.Sizeof(uintptr(0)))
	uiSetPropbit  = ioc(iocWrite, 'U', 110, 4)
	uiDevSetup    = ioc(iocWrite, 'U', 3, 92)
	uiAbsSetup    = ioc(iocWrite, 'U', 4, 28)
	uiDevCreate   = ioc(0, 'U', 1, 0)
	uiDevDestroy  = ioc(0, 'U', 2, 0)
)

func ioctlPtr(rc syscall.RawConn, req uintptr, p unsafe.Pointer) error {
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(p))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

func ioctlVal(rc syscall.RawConn, req, val uintptr) error {
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, val)
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

type evdevInfo struct {
	path     string
	name     string
	phys     string
	id       [4]uint16 // bustype, vendor, product, version
	evBits   []byte
	keyBits  []byte
	relBits  []byte
	absBits  []byte
	mscBits  []byte
	propBits []byte
	abs      map[int][6]int32
}

func (i *evdevInfo) isMouse() bool {
	return bitSet(i.evBits, evRel) && bitSet(i.relBits, relX) && bitSet(i.relBits, relY) && bitSet(i.keyBits, btnLeft)
}

func (i *evdevInfo) key() string {
	if i.id[1] != 0 || i.id[2] != 0 {
		return fmt.Sprintf("VID_%04X&PID_%04X", i.id[1], i.id[2])
	}
	return "EVDEV:" + i.name
}

func cString(b []byte) string {
	if n := bytes.IndexByte(b, 0); n >= 0 {
		b = b[:n]
	}
	return strings.TrimSpace(string(b))
}

func probeEvdev(path string) (*os.File, *evdevInfo, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	rc, err := f.SyscallConn()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	info := &evdevInfo{path: path, abs: map[int][6]int32{}}
	bits := func(req func(int) uintptr, max int) []byte {
		b := make([]byte, max/8+1)
		if ioctlPtr(rc, req(len(b)), unsafe.Pointer(&b[0])) != nil {
			return make([]byte, len(b))
		}
		return b
	}
	info.evBits = bits(func(n int) uintptr { return eviocgbit(0, n) }, 0x1f)
	info.keyBits = bits(func(n int) uintptr { return eviocgbit(evKey, n) }, keyMax)
	info.relBits = bits(func(n int) uintptr { return eviocgbit(evRel, n) }, relMax)
	info.absBits = bits(func(n int) uintptr { return eviocgbit(evAbs, n) }, absMax)
	info.mscBits = bits(func(n int) uintptr { return eviocgbit(evMsc, n) }, mscMax)
	info.propBits = bits(eviocgprop, propMax)

	name := make([]byte, 256)
	if ioctlPtr(rc, eviocgname(len(name)), unsafe.Pointer(&name[0])) == nil {
		info.name = cString(name)
	}
	phys := make([]byte, 256)
	if ioctlPtr(rc, eviocgphys(len(phys)), unsafe.Pointer(&phys[0])) == nil {
		info.phys = cString(phys)
	}
	id := make([]uint16, 4)
	if ioctlPtr(rc, eviocgid, unsafe.Pointer(&id[0])) == nil {
		copy(info.id[:], id)
	}
	for code := 0; code <= absMax; code++ {
		if bitSet(info.absBits, code) {
			ai := make([]int32, 6)
			if ioctlPtr(rc, eviocgabs(code), unsafe.Pointer(&ai[0])) == nil {
				info.abs[code] = [6]int32{ai[0], ai[1], ai[2], ai[3], ai[4], ai[5]}
			}
		}
	}
	return f, info, nil
}

// createClone builds a uinput device with the same capabilities and IDs.
func createClone(info *evdevInfo) (*os.File, error) {
	u, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		u.Close()
		return nil, err
	}
	rc, err := u.SyscallConn()
	if err != nil {
		return fail(err)
	}
	set := func(req uintptr, v int) error { return ioctlVal(rc, req, uintptr(v)) }
	enableCodes := func(ev int, bitsBuf []byte, max int, req uintptr) error {
		any := false
		for code := 0; code <= max; code++ {
			if bitSet(bitsBuf, code) {
				if !any {
					if err := set(uiSetEvbit, ev); err != nil {
						return err
					}
					any = true
				}
				if err := set(req, code); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := enableCodes(evKey, info.keyBits, keyMax, uiSetKeybit); err != nil {
		return fail(fmt.Errorf("uinput EV_KEY: %w", err))
	}
	if err := enableCodes(evRel, info.relBits, relMax, uiSetRelbit); err != nil {
		return fail(fmt.Errorf("uinput EV_REL: %w", err))
	}
	if err := enableCodes(evMsc, info.mscBits, mscMax, uiSetMscbit); err != nil {
		return fail(fmt.Errorf("uinput EV_MSC: %w", err))
	}
	absCodes := make([]int, 0, len(info.abs))
	for code := range info.abs {
		absCodes = append(absCodes, code)
	}
	sort.Ints(absCodes)
	if len(absCodes) > 0 {
		if err := set(uiSetEvbit, evAbs); err != nil {
			return fail(err)
		}
		for _, code := range absCodes {
			if err := set(uiSetAbsbit, code); err != nil {
				return fail(err)
			}
		}
	}
	for p := 0; p <= propMax; p++ {
		if bitSet(info.propBits, p) {
			_ = set(uiSetPropbit, p)
		}
	}
	phys := append([]byte(physMarker+"/"+info.phys), 0)
	_ = ioctlPtr(rc, uiSetPhys, unsafe.Pointer(&phys[0]))

	setup := make([]byte, 92)
	ne := binary.NativeEndian
	ne.PutUint16(setup[0:], info.id[0])
	ne.PutUint16(setup[2:], info.id[1])
	ne.PutUint16(setup[4:], info.id[2])
	ne.PutUint16(setup[6:], info.id[3])
	name := info.name
	if name == "" {
		name = "OmniVector Mouse"
	}
	if len(name) > 79 {
		name = name[:79]
	}
	copy(setup[8:88], name)
	if err := ioctlPtr(rc, uiDevSetup, unsafe.Pointer(&setup[0])); err != nil {
		return fail(fmt.Errorf("UI_DEV_SETUP: %w", err))
	}
	for _, code := range absCodes {
		ai := info.abs[code]
		b := make([]byte, 28)
		ne.PutUint16(b[0:], uint16(code))
		for i, v := range ai {
			ne.PutUint32(b[4+4*i:], uint32(v))
		}
		if err := ioctlPtr(rc, uiAbsSetup, unsafe.Pointer(&b[0])); err != nil {
			return fail(fmt.Errorf("UI_ABS_SETUP: %w", err))
		}
	}
	if err := ioctlVal(rc, uiDevCreate, 0); err != nil {
		return fail(fmt.Errorf("UI_DEV_CREATE: %w", err))
	}
	return u, nil
}

func destroyClone(u *os.File) {
	if rc, err := u.SyscallConn(); err == nil {
		_ = ioctlVal(rc, uiDevDestroy, 0)
	}
	u.Close()
}

type evdevDevice struct {
	info  *evdevInfo
	src   *os.File
	clone *os.File
	edev  *engine.Device
}

// pressedKeys reads the current key/button state from the device.
func (d *evdevDevice) pressedKeys() map[uint16]bool {
	rc, err := d.src.SyscallConn()
	if err != nil {
		return nil
	}
	b := make([]byte, keyMax/8+1)
	if ioctlPtr(rc, eviocgkey(len(b)), unsafe.Pointer(&b[0])) != nil {
		return nil
	}
	out := map[uint16]bool{}
	for code := 0; code <= keyMax; code++ {
		if bitSet(b, code) {
			out[uint16(code)] = true
		}
	}
	return out
}

type evdevBackend struct {
	eng *engine.Engine

	mu      sync.Mutex
	active  map[string]*evdevDevice
	ignored map[string]uint64 // path → device number of a non-mouse node
	warned  map[string]bool
	wg      sync.WaitGroup
}

func newEvdev(eng *engine.Engine) (Backend, error) {
	return &evdevBackend{eng: eng, active: map[string]*evdevDevice{}, ignored: map[string]uint64{}, warned: map[string]bool{}}, nil
}

func (b *evdevBackend) Name() string { return "evdev" }

const evdevPermissionHint = "Keine Berechtigung. OmniVector braucht Lesezugriff auf /dev/input/event* und Schreibzugriff auf /dev/uinput: " +
	"als root starten oder Benutzer zur Gruppe „input“ hinzufügen und die udev-Regel aus der README installieren."

func (b *evdevBackend) Run(ctx context.Context) error {
	u, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		msg := fmt.Sprintf("/dev/uinput nicht verfügbar: %v", err)
		if errors.Is(err, os.ErrPermission) {
			msg += " – " + evdevPermissionHint
		} else if errors.Is(err, os.ErrNotExist) {
			msg += " – Kernelmodul laden: sudo modprobe uinput"
		}
		b.eng.SetBackendStatus(engine.BackendStatus{Name: "evdev", Error: msg})
		return errors.New(msg)
	}
	u.Close()
	b.eng.SetBackendStatus(engine.BackendStatus{Name: "evdev", Running: true,
		Detail: "evdev/uinput aktiv – jede Maus wird exklusiv übernommen und transformiert weitergegeben."})

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		b.scan(ctx)
		select {
		case <-ctx.Done():
			b.mu.Lock()
			for _, d := range b.active {
				d.src.Close() // unblocks the reader, which cleans up
			}
			b.mu.Unlock()
			b.wg.Wait()
			b.eng.SetBackendStatus(engine.BackendStatus{Name: "evdev", Running: false, Detail: "beendet"})
			return nil
		case <-tick.C:
		}
	}
}

func deviceNumber(path string) uint64 {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return 0
	}
	return uint64(st.Rdev)
}

func (b *evdevBackend) scan(ctx context.Context) {
	paths, _ := filepath.Glob("/dev/input/event*")
	for _, p := range paths {
		if ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		_, busy := b.active[p]
		ign, isIgnored := b.ignored[p]
		b.mu.Unlock()
		rdev := deviceNumber(p)
		if busy || (isIgnored && ign == rdev) {
			continue
		}
		f, info, err := probeEvdev(p)
		if err != nil {
			if !b.warned[p] {
				b.warned[p] = true
				if errors.Is(err, os.ErrPermission) {
					log.Printf("%s: %v – %s", p, err, evdevPermissionHint)
				}
			}
			continue
		}
		if !info.isMouse() || strings.HasPrefix(info.phys, physMarker) {
			f.Close()
			b.mu.Lock()
			b.ignored[p] = rdev
			b.mu.Unlock()
			continue
		}
		if err := b.attach(ctx, f, info); err != nil {
			f.Close()
			if !b.warned[p] {
				b.warned[p] = true
				log.Printf("%s (%s) konnte nicht übernommen werden: %v", p, info.name, err)
			}
		}
	}
}

func (b *evdevBackend) attach(ctx context.Context, src *os.File, info *evdevInfo) error {
	rc, err := src.SyscallConn()
	if err != nil {
		return err
	}
	clock := int32(1) // CLOCK_MONOTONIC timestamps for the tracker
	_ = ioctlPtr(rc, eviocsclockid, unsafe.Pointer(&clock))
	clone, err := createClone(info)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w – %s", err, evdevPermissionHint)
		}
		return err
	}
	if err := ioctlVal(rc, eviocgrab, 1); err != nil {
		destroyClone(clone)
		return fmt.Errorf("EVIOCGRAB: %w (wird das Gerät schon von einem anderen Programm exklusiv genutzt?)", err)
	}
	d := &evdevDevice{info: info, src: src, clone: clone,
		edev: b.eng.Device(info.key(), info.name, "evdev")}
	b.mu.Lock()
	b.active[info.path] = d
	b.mu.Unlock()
	log.Printf("Maus übernommen: %s (%s, %s)", info.name, info.key(), info.path)
	b.wg.Add(1)
	go b.serve(d)
	return nil
}

func (b *evdevBackend) serve(d *evdevDevice) {
	defer b.wg.Done()
	defer func() {
		d.src.Close() // also releases the grab
		destroyClone(d.clone)
		b.eng.Disconnected(d.edev)
		b.mu.Lock()
		delete(b.active, d.info.path)
		b.mu.Unlock()
		log.Printf("Maus freigegeben: %s", d.info.name)
	}()
	transform := func(dx, dy int32, t int64) (ox, oy int32) {
		defer func() {
			if r := recover(); r != nil {
				ox, oy = dx, dy
			}
		}()
		return b.eng.Process(d.edev, dx, dy, t)
	}
	fp := newFrameProcessor(transform, d.pressedKeys)
	buf := make([]byte, evEventSize*64)
	out := make([]byte, 0, evEventSize*64)
	for {
		n, err := d.src.Read(buf)
		if err != nil {
			return
		}
		out = out[:0]
		for off := 0; off+evEventSize <= n; off += evEventSize {
			out = fp.feed(decodeEvent(buf[off:]), out)
		}
		if len(out) > 0 {
			if _, err := d.clone.Write(out); err != nil {
				return
			}
		}
	}
}

func (b *evdevBackend) ListDevices() ([]DeviceDesc, error) {
	paths, _ := filepath.Glob("/dev/input/event*")
	var out []DeviceDesc
	var firstErr error
	for _, p := range paths {
		f, info, err := probeEvdev(p)
		if err != nil {
			if firstErr == nil && errors.Is(err, os.ErrPermission) {
				firstErr = fmt.Errorf("%s: %w – %s", p, err, evdevPermissionHint)
			}
			continue
		}
		f.Close()
		if info.isMouse() && !strings.HasPrefix(info.phys, physMarker) {
			out = append(out, DeviceDesc{Key: info.key(), Name: info.name, Detail: p})
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}
