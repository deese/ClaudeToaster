//go:build windows

// claudetoaster is a Claude Code hook that raises a Windows toast when Claude
// needs attention or finishes, and focuses the right Windows Terminal tab when
// the toast is clicked.
//
//	claudetoaster                  hook mode: hook JSON on stdin -> toast
//	claudetoaster <claudetoaster://...>  toast click: focus the tab
//	claudetoaster install          register the AppUserModelID and URL handler
//	claudetoaster list             print Windows Terminal windows and tab titles
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	logFileName  = "claudetoaster.log"
	focusHost    = "focus"
	maxBodyRunes = 200

	notifyPermissionPrompt = "permission_prompt"
)

type hookInput struct {
	HookEventName        string `json:"hook_event_name"`
	NotificationType     string `json:"notification_type"`
	Message              string `json:"message"`
	CWD                  string `json:"cwd"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

func main() {
	if len(os.Args) < 2 {
		// Hooks must never break Claude: log failures, always exit 0.
		if err := runHook(); err != nil {
			logf("hook: %v", err)
		}
		return
	}

	arg := os.Args[1]
	var err error
	switch {
	case strings.HasPrefix(arg, protocolScheme+"://"):
		err = runActivation(arg)
	case arg == "install":
		ensureConsoleOutput()
		err = runInstall()
	case arg == "list":
		ensureConsoleOutput()
		err = runList()
	default:
		ensureConsoleOutput()
		fmt.Fprintln(os.Stderr, "usage: claudetoaster [install | list | claudetoaster://...]")
		os.Exit(2)
	}
	if err != nil {
		logf("%s: %v", arg, err)
		ensureConsoleOutput()
		fmt.Fprintln(os.Stderr, "claudetoaster:", err)
		os.Exit(1)
	}
}

func runHook() error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if err := dumpIfEnabled(raw); err != nil {
		logf("dump: %v", err)
	}

	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("parse hook input: %w", err)
	}

	// idle_prompt fires often and needs no decision; only permission prompts matter.
	if in.HookEventName == "Notification" && in.NotificationType != notifyPermissionPrompt {
		return nil
	}

	launch, shellPID := focusTarget()
	if shellPID != 0 && tabInUse(shellPID) {
		return nil
	}

	title, body := toastText(in)
	return showToast(title, body, launch)
}

// tabInUse reports whether the user is already looking at the shell's tab.
// Any failure counts as "not in use": a redundant toast beats a lost one.
func tabInUse(shellPID uint32) bool {
	active, err := isShellTabActive(shellPID)
	if err != nil {
		logf("tab activity check: %v", err)
		return false
	}
	return active
}

// focusTarget returns the URI a click will launch and the shell PID it
// targets, or "" and 0 when this process does not run under Windows Terminal
// (a click then does nothing).
func focusTarget() (uri string, shellPID uint32) {
	shellPID, err := findShell()
	if err != nil {
		logf("no focus target: %v", err)
		return "", 0
	}
	created, err := processCreationTime(shellPID)
	if err != nil {
		logf("shell creation time: %v", err)
		return "", 0
	}
	return fmt.Sprintf("%s://%s?shell=%d&ct=%d", protocolScheme, focusHost, shellPID, created), shellPID
}

func toastText(in hookInput) (title, body string) {
	project := filepath.Base(in.CWD)
	if in.CWD == "" {
		project = "Claude Code"
	}

	switch in.HookEventName {
	case "Notification":
		title = project + " - needs permission"
		body = in.Message
		if body == "" {
			body = "Claude Code needs your permission."
		}
	case "Stop":
		title = project + " - finished"
		body = in.LastAssistantMessage
	default:
		title = project
		body = in.HookEventName
	}
	return title, truncateRunes(strings.Join(strings.Fields(body), " "), maxBodyRunes)
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// runActivation handles a toast click: claudetoaster://focus?shell=PID&ct=FILETIME.
func runActivation(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Host != focusHost {
		return fmt.Errorf("unknown action %q", u.Host)
	}

	q := u.Query()
	shellPID, err := strconv.ParseUint(q.Get("shell"), 10, 32)
	if err != nil {
		return fmt.Errorf("invalid shell pid: %w", err)
	}
	created, err := strconv.ParseUint(q.Get("ct"), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid creation time: %w", err)
	}

	// The protocol handler is reachable by any local process, so only act on a
	// process that still is the recorded shell under Windows Terminal.
	if err := validateShell(uint32(shellPID), created); err != nil {
		return err
	}
	return focusShell(uint32(shellPID))
}

func runList() error {
	return withAutomation(func(auto, cond comObj) error {
		windows := terminalWindows()
		fmt.Printf("%d terminal window(s)\n", len(windows))
		for _, hwnd := range windows {
			tabs, names := listTabs(auto, cond, hwnd)
			releaseAll(tabs)
			fmt.Printf("window 0x%x tabs=%d\n", hwnd, len(names))
			for _, n := range names {
				fmt.Printf("  %q\n", n)
			}
		}
		return nil
	})
}

// logf appends a line to the log in the temp dir; the binary has no console
// when started by a hook or a toast click.
func logf(format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
