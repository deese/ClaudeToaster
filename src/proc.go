//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	wtExe                          = "WindowsTerminal.exe"
	maxChainDepth                  = 16
	processQueryLimitedInformation = 0x1000
)

type procInfo struct {
	PID  uint32 `json:"pid"`
	PPID uint32 `json:"ppid"`
	Exe  string `json:"exe"`
}

// parentChain returns the process, its parent, grandparent, ... up to the root.
func parentChain(pid uint32) []procInfo {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer syscall.CloseHandle(snap)

	procs := map[uint32]procInfo{}
	var entry syscall.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = syscall.Process32First(snap, &entry); err == nil; err = syscall.Process32Next(snap, &entry) {
		procs[entry.ProcessID] = procInfo{
			PID:  entry.ProcessID,
			PPID: entry.ParentProcessID,
			Exe:  syscall.UTF16ToString(entry.ExeFile[:]),
		}
	}

	var chain []procInfo
	for cur := pid; len(chain) < maxChainDepth; {
		p, ok := procs[cur]
		if !ok {
			break
		}
		chain = append(chain, p)
		// Guard against PID reuse loops.
		if p.PPID == 0 || p.PPID == cur {
			break
		}
		cur = p.PPID
	}
	return chain
}

// findShell returns the PID of this process' ancestor that is a direct child
// of WindowsTerminal.exe, i.e. the shell that owns the tab.
func findShell() (uint32, error) {
	chain := parentChain(uint32(os.Getpid()))
	for i, p := range chain {
		if strings.EqualFold(p.Exe, wtExe) && i > 0 {
			return chain[i-1].PID, nil
		}
	}
	return 0, errNotInTerminal
}

// errNotInTerminal is a normal condition (other terminals, IDEs), not a failure.
var errNotInTerminal = errors.New("no WindowsTerminal.exe in parent chain")

// processCreationTime returns the process creation time as a FILETIME value.
func processCreationTime(pid uint32) (uint64, error) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(h)

	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}

// validateShell checks that pid still is the shell recorded in the toast: same
// creation time (guards against PID reuse) and a direct child of Windows Terminal.
func validateShell(pid uint32, creationTime uint64) error {
	got, err := processCreationTime(pid)
	if err != nil {
		return fmt.Errorf("shell %d is gone: %w", pid, err)
	}
	if got != creationTime {
		return fmt.Errorf("shell %d was reused by another process", pid)
	}
	chain := parentChain(pid)
	if len(chain) < 2 || !strings.EqualFold(chain[1].Exe, wtExe) {
		return fmt.Errorf("shell %d is not a child of %s", pid, wtExe)
	}
	return nil
}
