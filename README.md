# ClaudeToaster

A [Claude Code](https://claude.com/claude-code) hook for Windows that raises a toast notification when Claude needs your permission, and brings forward the exact Windows Terminal tab that session runs in when you click it.

It is a single Go binary with no third-party dependencies and no PowerShell involved, so it also works on machines where PowerShell runs in Constrained Language Mode.

## Behavior

- The `Notification` hook shows a toast only for `permission_prompt` events (`<project> - needs permission`). `idle_prompt` notifications are ignored on purpose, since they fire often and need no decision.
- The `Stop` hook is supported too (`<project> - finished`, with the start of the last assistant message as body) but is not required; with many open tabs it is usually too noisy.
- No toast is shown when you are already looking at that session: the shell's tab is the selected tab of the foreground Windows Terminal window.
- Clicking the toast selects the originating tab (in any Windows Terminal window, even if it was dragged to another one) and raises its window.
- The hook always exits 0 and never writes to stdout, so it cannot interfere with Claude Code. Failures are appended to `%TEMP%\claudetoaster.log` (created only on the first failure, truncated above 256 KB). Running outside Windows Terminal is not a failure and is not logged.

## Requirements

- Windows 10/11 and Windows Terminal. Sessions in other terminals still get toasts, but clicking them does nothing.
- Go (to build). The module uses only the standard library.
- The executable must be allowed to run by your execution policy (AppLocker, WDAC, antivirus). An unsigned binary in an arbitrary directory may be blocked.

## Build and install

```
cd src
go build -ldflags "-H=windowsgui" -o claudetoaster.exe .
claudetoaster.exe install
```

Prebuilt, unsigned binaries and SHA-256 checksums are attached to each tagged [release](../../releases).

`-H=windowsgui` avoids a console window flashing when a toast click launches the binary.

`install` writes two things under `HKCU` (no admin rights needed): the toast `AppUserModelId` (so toasts appear as "Claude Toaster") and the `claudetoaster://` URL protocol handler pointing at the binary's current path. Run it again if you move the binary. To uninstall, delete `HKCU\Software\Classes\claudetoaster` and `HKCU\Software\Classes\AppUserModelId\ClaudeToaster`.

## Hook configuration

Add the hook to your Claude Code `settings.json`:

```json
{
  "hooks": {
    "Notification": [
      {
        "hooks": [
          { "type": "command", "command": "C:/path/to/claudetoaster.exe" }
        ]
      }
    ]
  }
}
```

Use forward slashes: on Windows, hook commands run through bash, which swallows backslashes. To also get a toast when a response finishes, add the same entry under `Stop`.

## Commands

| Invocation | Purpose |
| --- | --- |
| `claudetoaster` (no arguments) | Hook mode: reads the hook JSON from stdin and shows the toast. |
| `claudetoaster install` | Registers the AppUserModelId and the URL protocol handler. |
| `claudetoaster list` | Prints every Windows Terminal window and its tab titles (useful to check UI Automation can see your tabs). |
| `claudetoaster claudetoaster://...` | Invoked by Windows on toast click; not meant to be run by hand. |

## How it works

Claude Code gives a hook no information about which terminal tab it runs in, so the binary derives it from the process tree.

1. **Hook time.** The binary walks up its parent chain to the direct child of `WindowsTerminal.exe` (the tab's shell) and records that PID and its creation time in the toast's activation URI: `claudetoaster://focus?shell=<pid>&ct=<filetime>`.
2. **Is the tab in use?** It tags the tab by attaching to the shell's console (`AttachConsole`) and writing an OSC 2 title sequence with a unique token. It finds the tab through UI Automation by that title, reads whether it is selected and whether its window is in the foreground, and restores the original title.
3. **Click time.** Windows launches the binary through the protocol handler. It validates the PID and creation time and that the process is still a child of Windows Terminal, repeats the tagging step, selects the tab with `SelectionItemPattern.Select`, restores the title and calls `SetForegroundWindow`.

Tab identity is re-resolved at click time instead of stored because UI Automation `RuntimeId`s are stable when tabs are reordered inside a window but change when a tab is moved to another window.

## Limitations

- Only Windows Terminal tabs are focused. Claude Code sessions running inside WSL are not covered: the hook runs in Linux and its process chain does not reach `WindowsTerminal.exe`.
- The tab title flickers for a fraction of a second whenever a hook checks the tab or a toast is clicked.
- Each permission prompt adds tagging and UI Automation polling (roughly 100 ms to 2 s) on top of normal process start-up.
- If Windows Terminal or the shell was restarted, old toasts no longer match a live shell and the click does nothing (the reason is logged).
- Windows may refuse `SetForegroundWindow`. The tab is still selected and the refusal is logged.
- With Focus Assist / Do Not Disturb active, Windows suppresses the toast.

## Security notes

- The `claudetoaster://` handler is reachable by any local process or web page. It only acts on a PID that still has the recorded creation time and is a direct child of `WindowsTerminal.exe`, and the only action it performs is selecting a tab and raising its window.
- The hook may be given conversation text (`last_assistant_message`, `message`). It is only used to build the local toast and is never sent anywhere.

## Debugging

- Errors: `%TEMP%\claudetoaster.log`.
- Capture the raw hook payloads: set `CLAUDETOASTER_DUMP` to a file path (for example by prefixing the hook command with `CLAUDETOASTER_DUMP=/path/dump.jsonl`). Each invocation appends the stdin JSON, selected environment variables and the parent chain. Variables whose names contain `TOKEN`, `SECRET`, `KEY` or `PASSWORD` are never recorded, but the stdin JSON can contain conversation text, so delete the file when done.

## License

MIT, see [LICENSE](LICENSE).

## Implementation notes

UI Automation, WinRT toasts and the registry are accessed through raw COM/WinRT vtable calls with `syscall` and `unsafe`, which keeps the module free of external dependencies. The vtable slot numbers and IIDs were checked against the mingw-w64 headers `uiautomationclient.h`, `windows.ui.notifications.h` and `windows.data.xml.dom.h`. A thread cannot change COM apartment model, so UI Automation and WinRT are both initialised as STA.

Source layout (under `src/`):

| File | Content |
| --- | --- |
| `main.go` | Dispatch, hook mode, toast text, protocol activation. |
| `uia.go` | UI Automation: tab tagging, lookup, selection, foreground check. |
| `toast.go` | Toast through WinRT. |
| `console.go` | Title writes through the shell's console. |
| `proc.go` | Parent chain, process creation time, shell validation. |
| `install.go` | Registry registration. |
| `dump.go` | Optional payload dump. |
| `com.go` | Raw COM interface helper. |
