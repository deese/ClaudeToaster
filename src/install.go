//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	protocolScheme = "claudetoaster"
	appUserModelID = "ClaudeToaster"
	appDisplayName = "Claude Toaster"

	regKeyWrite = 0x20006
	regSZ       = 1
)

var (
	advapi32            = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKeyExW = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegCloseKey     = advapi32.NewProc("RegCloseKey")
)

// runInstall registers the toast AppUserModelID and the claudetoaster://
// protocol handler under HKCU (no admin rights needed).
func runInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := setRegString(`Software\Classes\AppUserModelId\`+appUserModelID, "DisplayName", appDisplayName); err != nil {
		return err
	}

	root := `Software\Classes\` + protocolScheme
	if err := setRegString(root, "", "URL:Claude Toaster"); err != nil {
		return err
	}
	if err := setRegString(root, "URL Protocol", ""); err != nil {
		return err
	}
	if err := setRegString(root+`\shell\open\command`, "", fmt.Sprintf(`"%s" "%%1"`, exe)); err != nil {
		return err
	}
	fmt.Println("registered AppUserModelID and protocol handler for", exe)
	return nil
}

// setRegString writes a REG_SZ value under HKCU, creating the key if needed.
// An empty name sets the key's default value.
func setRegString(subkey, name, value string) error {
	pKey, err := syscall.UTF16PtrFromString(subkey)
	if err != nil {
		return err
	}
	var h syscall.Handle
	if r, _, _ := procRegCreateKeyExW.Call(uintptr(syscall.HKEY_CURRENT_USER), uintptr(unsafe.Pointer(pKey)),
		0, 0, 0, regKeyWrite, 0, uintptr(unsafe.Pointer(&h)), 0); r != 0 {
		return fmt.Errorf("create HKCU\\%s: %w", subkey, syscall.Errno(r))
	}
	defer procRegCloseKey.Call(uintptr(h))

	var pName *uint16
	if name != "" {
		if pName, err = syscall.UTF16PtrFromString(name); err != nil {
			return err
		}
	}
	data, err := syscall.UTF16FromString(value)
	if err != nil {
		return err
	}
	size := uint32(len(data) * 2)
	if r, _, _ := procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(pName)), 0, regSZ,
		uintptr(unsafe.Pointer(&data[0])), uintptr(size)); r != 0 {
		return fmt.Errorf("set HKCU\\%s %q: %w", subkey, name, syscall.Errno(r))
	}
	return nil
}
