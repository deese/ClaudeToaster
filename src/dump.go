//go:build windows

package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Debug aid: when CLAUDETOASTER_DUMP names a file, every hook invocation
// appends the stdin payload, selected env vars and the parent chain to it.
const dumpPathEnv = "CLAUDETOASTER_DUMP"

// Only these env prefixes are dumped; everything else may hold secrets.
var envPrefixes = []string{"WT_", "CLAUDE", "TERM", "SESSIONNAME", "WSL"}

// Keys containing any of these substrings are never dumped, even if the prefix matches.
var envSecretMarkers = []string{"TOKEN", "SECRET", "KEY", "PASSWORD"}

type dumpRecord struct {
	Time  string            `json:"time"`
	Stdin json.RawMessage   `json:"stdin,omitempty"`
	Raw   string            `json:"stdin_raw,omitempty"`
	Env   map[string]string `json:"env"`
	Chain []procInfo        `json:"chain"`
}

func dumpIfEnabled(stdin []byte) error {
	path := os.Getenv(dumpPathEnv)
	if path == "" {
		return nil
	}

	rec := dumpRecord{
		Time:  time.Now().Format(time.RFC3339Nano),
		Env:   filteredEnv(),
		Chain: parentChain(uint32(os.Getpid())),
	}
	if json.Valid(stdin) {
		rec.Stdin = stdin
	} else {
		rec.Raw = string(stdin)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(rec)
}

func filteredEnv() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if containsAny(upper, envSecretMarkers) {
			continue
		}
		for _, p := range envPrefixes {
			if strings.HasPrefix(upper, p) {
				out[key] = val
				break
			}
		}
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
