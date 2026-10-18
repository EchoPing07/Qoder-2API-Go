package auth

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeRuntimeInfoScript writes a fake umid helper (a POSIX shell script that
// prints payload) and returns its path. Skipped on Windows: exec there cannot
// run a #! script without an executable extension.
func writeRuntimeInfoScript(t *testing.T, payload string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake umid helper is a POSIX shell script; Windows cannot exec it")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "runtime-info")
	script := "#!/bin/sh\nprintf '%s\\n' '" + payload + "'\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// readRiskIdentityExec parses a valid helper payload.
func TestReadRiskIdentityExecValid(t *testing.T) {
	exe := writeRuntimeInfoScript(t, `{"machineToken":"tok88","machineType":"type18","machineCode":"code18","vmInfo":{}}`)
	id := readRiskIdentityExec(context.Background(), exe)
	if !id.Valid() {
		t.Fatalf("expected valid identity, got %+v", id)
	}
	if id.MachineToken != "tok88" || id.MachineType != "type18" || id.MachineCode != "code18" {
		t.Errorf("unexpected identity: %+v", id)
	}
}

// A partial identity (missing a field) is rejected.
func TestReadRiskIdentityExecPartial(t *testing.T) {
	exe := writeRuntimeInfoScript(t, `{"machineToken":"tok","machineType":"type"}`)
	if id := readRiskIdentityExec(context.Background(), exe); id != nil {
		t.Errorf("partial identity must be rejected, got %+v", id)
	}
}

// A missing executable yields nil.
func TestReadRiskIdentityExecMissing(t *testing.T) {
	if id := readRiskIdentityExec(context.Background(), filepath.Join(t.TempDir(), "nope")); id != nil {
		t.Errorf("missing executable must yield nil, got %+v", id)
	}
}

// QODER_RUNTIME_INFO override is honored by ReadRiskIdentity.
func TestReadRiskIdentityOverride(t *testing.T) {
	exe := writeRuntimeInfoScript(t, `{"machineToken":"T","machineType":"Y","machineCode":"C"}`)
	t.Setenv("QODER_RUNTIME_INFO", exe)
	id := ReadRiskIdentity(context.Background())
	if !id.Valid() || id.MachineToken != "T" {
		t.Fatalf("override not used: %+v", id)
	}
}
