//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	vtQueryInterface = 0
	vtRelease        = 2
)

type guid struct {
	D1 uint32
	D2 uint16
	D3 uint16
	D4 [8]byte
}

// comObj is a raw COM/WinRT interface pointer; calls go through its vtable.
type comObj struct{ p unsafe.Pointer }

func (o comObj) call(slot uintptr, args ...uintptr) error {
	vtbl := *(*unsafe.Pointer)(o.p)
	fn := *(*uintptr)(unsafe.Add(vtbl, slot*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(o.p)}, args...)...)
	if int32(r) < 0 {
		return fmt.Errorf("HRESULT 0x%08x (vtable slot %d)", uint32(r), slot)
	}
	return nil
}

func (o comObj) release() {
	if o.p != nil {
		_ = o.call(vtRelease)
	}
}
