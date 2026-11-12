//go:build windows

// UI Automation access to Windows Terminal tabs through raw COM vtables. Slots
// and IIDs were checked against the mingw-w64 uiautomationclient.h header.
package main

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	wtWindowCls     = "CASCADIA_HOSTING_WINDOW_CLASS"
	titleTagBase    = "claudetoaster-"
	tagPollInterval = 100 * time.Millisecond
	tagPollTimeout  = 2 * time.Second

	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	treeScopeDescendants    = 0x4
	uiaTabItemControlType   = 50019
	uiaSelectionItemPattern = 10010
	swRestore               = 9

	vtAutoElementFromHandle = 6
	vtAutoCreateTrueCond    = 21
	vtElemFindAll           = 6
	vtElemGetPattern        = 16
	vtElemControlType       = 21
	vtElemName              = 23
	vtArrayLength           = 3
	vtArrayGetElement       = 4
	vtSelectionItemSelect   = 3
)

var (
	clsidCUIAutomation  = guid{0xff48dba4, 0x60ef, 0x4201, [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}}
	iidIUIAutomation    = guid{0x30cbe57d, 0xd9d0, 0x452a, [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}}
	iidSelectionItemPat = guid{0xa8efa66a, 0x0fda, 0x421a, [8]byte{0x91, 0x94, 0x38, 0x02, 0x1f, 0x35, 0x78, 0xea}}
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	oleaut32 = syscall.NewLazyDLL("oleaut32.dll")

	procEnumWindows           = user32.NewProc("EnumWindows")
	procGetWindowThreadProcID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible       = user32.NewProc("IsWindowVisible")
	procGetClassNameW         = user32.NewProc("GetClassNameW")
	procIsIconic              = user32.NewProc("IsIconic")
	procShowWindow            = user32.NewProc("ShowWindow")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procCoInitializeEx        = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance      = ole32.NewProc("CoCreateInstance")
	procSysFreeString         = oleaut32.NewProc("SysFreeString")
	procSysStringLen          = oleaut32.NewProc("SysStringLen")
)

// focusShell brings forward the Windows Terminal tab that hosts shellPID:
// it tags the tab with a unique title, finds it through UIA in any terminal
// window, selects it, restores its title and raises its window.
func focusShell(shellPID uint32) error {
	return withAutomation(func(auto, cond comObj) error {
		tab, hwnd, restore, err := tagShellTab(auto, cond, shellPID)
		if err != nil {
			return err
		}
		defer tab.release()

		selErr := selectTab(tab)
		restore()
		if selErr != nil {
			return fmt.Errorf("select tab: %w", selErr)
		}
		return raiseWindow(hwnd)
	})
}

// isShellTabActive reports whether the tab that hosts shellPID is the selected
// tab of the foreground Windows Terminal window, i.e. the user is looking at it.
func isShellTabActive(shellPID uint32) (bool, error) {
	active := false
	err := withAutomation(func(auto, cond comObj) error {
		tab, hwnd, restore, err := tagShellTab(auto, cond, shellPID)
		if err != nil {
			return err
		}
		defer tab.release()

		selected, selErr := tabIsSelected(tab)
		restore()
		if selErr != nil {
			return fmt.Errorf("read tab selection: %w", selErr)
		}

		fg, _, _ := procGetForegroundWindow.Call()
		iconic, _, _ := procIsIconic.Call(hwnd)
		active = selected && fg == hwnd && iconic == 0
		return nil
	})
	return active, err
}

// tagShellTab gives the shell's tab a unique title and returns its UIA element
// and window. The caller must release tab and call restore to put the title back.
func tagShellTab(auto, cond comObj, shellPID uint32) (tab comObj, hwnd uintptr, restore func(), err error) {
	before := tabNamesByWindow(auto, cond)

	token := titleTagBase + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := writeTitleViaConsole(shellPID, token); err != nil {
		return comObj{}, 0, nil, fmt.Errorf("tag title: %w", err)
	}

	tab, hwnd, orig, err := awaitTaggedTab(auto, cond, token, before)
	if err != nil {
		return comObj{}, 0, nil, err
	}
	restore = func() {
		if orig == "" {
			return
		}
		if err := writeTitleViaConsole(shellPID, orig); err != nil {
			logf("restore title: %v", err)
		}
	}
	return tab, hwnd, restore, nil
}

// awaitTaggedTab polls until a tab whose title contains token shows up and
// returns it (the caller releases it) with its window and its title from
// before tagging.
func awaitTaggedTab(auto, cond comObj, token string, before map[uintptr][]string) (comObj, uintptr, string, error) {
	deadline := time.Now().Add(tagPollTimeout)
	for {
		time.Sleep(tagPollInterval)
		for _, hwnd := range terminalWindows() {
			tabs, names := listTabs(auto, cond, hwnd)
			idx := indexContaining(names, token)
			if idx < 0 {
				releaseAll(tabs)
				continue
			}

			for i, t := range tabs {
				if i != idx {
					t.release()
				}
			}

			orig := ""
			if prev := before[hwnd]; len(prev) == len(names) && !strings.HasPrefix(prev[idx], titleTagBase) {
				orig = prev[idx]
			}
			return tabs[idx], hwnd, orig, nil
		}
		if time.Now().After(deadline) {
			return comObj{}, 0, "", errors.New("tagged tab not found via UIA")
		}
	}
}

// withAutomation runs fn on a COM-initialised thread with a UIA client and a
// "true" condition.
func withAutomation(fn func(auto, cond comObj) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded); int32(r) < 0 {
		return fmt.Errorf("CoInitializeEx: 0x%08x", uint32(r))
	}

	auto, err := newAutomation()
	if err != nil {
		return err
	}
	defer auto.release()

	var cond unsafe.Pointer
	if err := auto.call(vtAutoCreateTrueCond, uintptr(unsafe.Pointer(&cond))); err != nil {
		return err
	}
	defer comObj{cond}.release()

	return fn(auto, comObj{cond})
}

func newAutomation() (comObj, error) {
	var out unsafe.Pointer
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&out)))
	if int32(r) < 0 {
		return comObj{}, fmt.Errorf("CoCreateInstance(CUIAutomation): 0x%08x", uint32(r))
	}
	return comObj{out}, nil
}

// listTabs returns the TabItem elements of a window with their names. The
// caller must release the elements.
func listTabs(auto, cond comObj, hwnd uintptr) ([]comObj, []string) {
	var root unsafe.Pointer
	if err := auto.call(vtAutoElementFromHandle, hwnd, uintptr(unsafe.Pointer(&root))); err != nil {
		return nil, nil
	}
	defer comObj{root}.release()

	var arr unsafe.Pointer
	if err := (comObj{root}).call(vtElemFindAll, treeScopeDescendants, uintptr(cond.p), uintptr(unsafe.Pointer(&arr))); err != nil {
		return nil, nil
	}
	defer comObj{arr}.release()

	var length int32
	if err := (comObj{arr}).call(vtArrayLength, uintptr(unsafe.Pointer(&length))); err != nil {
		return nil, nil
	}

	var tabs []comObj
	var names []string
	for i := int32(0); i < length; i++ {
		var el unsafe.Pointer
		if err := (comObj{arr}).call(vtArrayGetElement, uintptr(i), uintptr(unsafe.Pointer(&el))); err != nil {
			continue
		}
		elem := comObj{el}

		var ctype int32
		if err := elem.call(vtElemControlType, uintptr(unsafe.Pointer(&ctype))); err != nil || ctype != uiaTabItemControlType {
			elem.release()
			continue
		}
		tabs = append(tabs, elem)
		names = append(names, elementName(elem))
	}
	return tabs, names
}

func tabNamesByWindow(auto, cond comObj) map[uintptr][]string {
	out := map[uintptr][]string{}
	for _, hwnd := range terminalWindows() {
		tabs, names := listTabs(auto, cond, hwnd)
		releaseAll(tabs)
		out[hwnd] = names
	}
	return out
}

func elementName(el comObj) string {
	var bstr *uint16
	if err := el.call(vtElemName, uintptr(unsafe.Pointer(&bstr))); err != nil || bstr == nil {
		return ""
	}
	defer procSysFreeString.Call(uintptr(unsafe.Pointer(bstr)))
	n, _, _ := procSysStringLen.Call(uintptr(unsafe.Pointer(bstr)))
	return syscall.UTF16ToString(unsafe.Slice(bstr, int(n)))
}

// vtSelectionItemIsSelected is get_CurrentIsSelected, checked against uiautomationclient.h.
const vtSelectionItemIsSelected = 6

var procGetForegroundWindow = user32.NewProc("GetForegroundWindow")

func selectTab(tab comObj) error {
	pat, err := selectionItemPattern(tab)
	if err != nil {
		return err
	}
	defer pat.release()
	return pat.call(vtSelectionItemSelect)
}

func tabIsSelected(tab comObj) (bool, error) {
	pat, err := selectionItemPattern(tab)
	if err != nil {
		return false, err
	}
	defer pat.release()

	var selected int32
	if err := pat.call(vtSelectionItemIsSelected, uintptr(unsafe.Pointer(&selected))); err != nil {
		return false, err
	}
	return selected != 0, nil
}

// selectionItemPattern returns the tab's IUIAutomationSelectionItemPattern.
// The caller must release it.
func selectionItemPattern(tab comObj) (comObj, error) {
	var unk unsafe.Pointer
	if err := tab.call(vtElemGetPattern, uiaSelectionItemPattern, uintptr(unsafe.Pointer(&unk))); err != nil {
		return comObj{}, err
	}
	if unk == nil {
		return comObj{}, errors.New("tab does not expose SelectionItemPattern")
	}
	defer comObj{unk}.release()

	var pat unsafe.Pointer
	if err := (comObj{unk}).call(vtQueryInterface, uintptr(unsafe.Pointer(&iidSelectionItemPat)), uintptr(unsafe.Pointer(&pat))); err != nil {
		return comObj{}, err
	}
	return comObj{pat}, nil
}

func raiseWindow(hwnd uintptr) error {
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	if r, _, _ := procSetForegroundWindow.Call(hwnd); r == 0 {
		return errors.New("tab selected, but SetForegroundWindow was refused")
	}
	return nil
}

// terminalWindows returns the visible top-level Windows Terminal windows of
// every process.
func terminalWindows() []uintptr {
	var found []uintptr
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if vis, _, _ := procIsWindowVisible.Call(hwnd); vis == 0 {
			return 1
		}
		var cls [64]uint16
		procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
		if syscall.UTF16ToString(cls[:]) == wtWindowCls {
			found = append(found, hwnd)
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

func releaseAll(objs []comObj) {
	for _, o := range objs {
		o.release()
	}
}

func indexContaining(names []string, sub string) int {
	for i, n := range names {
		if strings.Contains(n, sub) {
			return i
		}
	}
	return -1
}
