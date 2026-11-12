//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

const attachParentProcess = 0xFFFFFFFF

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procFreeConsole   = kernel32.NewProc("FreeConsole")
	procAttachConsole = kernel32.NewProc("AttachConsole")
)

// writeTitleViaConsole attaches to the shell's console and writes an OSC 2
// title sequence to it, which Windows Terminal applies to that shell's tab.
func writeTitleViaConsole(shellPID uint32, title string) error {
	procFreeConsole.Call()
	if r, _, e := procAttachConsole.Call(uintptr(shellPID)); r == 0 {
		return fmt.Errorf("AttachConsole(%d): %w", shellPID, e)
	}
	defer procFreeConsole.Call()

	out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.WriteString("\x1b]2;" + title + "\x07")
	return err
}

// ensureConsoleOutput lets the windowsgui binary print when it was started from
// an interactive console (stdout is not redirected).
func ensureConsoleOutput() {
	if _, err := os.Stdout.Stat(); err == nil {
		return
	}
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		os.Stderr = f
	}
}
