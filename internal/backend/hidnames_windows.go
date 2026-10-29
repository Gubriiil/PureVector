//go:build windows && amd64

package backend

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

// hidProductNames asks Windows for the product strings of all mice, keyed
// like describeHardwareID ("VID_1B1C&PID_1BAC" → "Corsair SABRE v3 PRO").
// Best effort: missing names fall back to the vendor table.

var (
	modUser32                     = syscall.NewLazyDLL("user32.dll")
	procGetRawInputDeviceList     = modUser32.NewProc("GetRawInputDeviceList")
	procGetRawInputDeviceInfoW    = modUser32.NewProc("GetRawInputDeviceInfoW")
	modHid                        = syscall.NewLazyDLL("hid.dll")
	procHidDGetProductString      = modHid.NewProc("HidD_GetProductString")
	procHidDGetManufacturerString = modHid.NewProc("HidD_GetManufacturerString")
)

type rawInputDeviceList struct {
	hDevice uintptr
	dwType  uint32
}

const (
	rimTypeMouse   = 0
	ridiDeviceName = 0x20000007
)

func hidProductNames() (out map[string]string) {
	out = map[string]string{}
	defer func() { _ = recover() }() // missing DLL procs must not matter
	size := unsafe.Sizeof(rawInputDeviceList{})
	var n uint32
	if r, _, _ := procGetRawInputDeviceList.Call(0, uintptr(unsafe.Pointer(&n)), size); int32(r) < 0 || n == 0 {
		return out
	}
	list := make([]rawInputDeviceList, n+8)
	n = uint32(len(list))
	r, _, _ := procGetRawInputDeviceList.Call(uintptr(unsafe.Pointer(&list[0])), uintptr(unsafe.Pointer(&n)), size)
	if int32(r) < 0 {
		return out
	}
	for _, it := range list[:int(uint32(r))] {
		if it.dwType != rimTypeMouse {
			continue
		}
		var cch uint32
		procGetRawInputDeviceInfoW.Call(it.hDevice, ridiDeviceName, 0, uintptr(unsafe.Pointer(&cch)))
		if cch == 0 || cch > 4096 {
			continue
		}
		buf := make([]uint16, cch+1)
		if r, _, _ := procGetRawInputDeviceInfoW.Call(it.hDevice, ridiDeviceName, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&cch))); int32(r) <= 0 {
			continue
		}
		path := syscall.UTF16ToString(buf)
		vid, pid, ok := parseVidPid(path)
		if !ok {
			continue
		}
		key := fmt.Sprintf("VID_%04X&PID_%04X", vid, pid)
		if out[key] != "" {
			continue
		}
		if name := hidDeviceName(path); name != "" {
			out[key] = name
		}
	}
	return out
}

func hidDeviceName(path string) string {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	// Access 0: query only. Mice are opened exclusively by Windows, but the
	// HID string requests work on such a handle.
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)
	get := func(proc *syscall.LazyProc) string {
		var b [127]uint16
		if r, _, _ := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&b[0])), unsafe.Sizeof(b)); r == 0 {
			return ""
		}
		return strings.TrimSpace(syscall.UTF16ToString(b[:]))
	}
	product := get(procHidDGetProductString)
	maker := get(procHidDGetManufacturerString)
	switch {
	case product == "":
		return ""
	case maker == "" || strings.Contains(strings.ToLower(product), strings.ToLower(maker)):
		return product
	default:
		return maker + " " + product
	}
}
