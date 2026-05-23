package cli

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// bd-i2t4l: printLockWarnings must emit non-empty warnings to STDERR
// (not stdout — JSON consumers parse stdout). Forward-compatible: an
// empty / nil slice produces no output.
func TestPrintLockWarnings_EmitsNonEmptyToStderr(t *testing.T) {
	stderr := captureStderr(t, func() {
		printLockWarnings([]string{
			"enforcement_off_for_code_paths: 1 of 1 reserved paths are code-repo paths; advisory only.",
		})
	})

	if !bytes.Contains(stderr, []byte("WARN:")) {
		t.Errorf("stderr missing WARN prefix: %q", stderr)
	}
	if !bytes.Contains(stderr, []byte("enforcement_off_for_code_paths")) {
		t.Errorf("stderr missing warning body: %q", stderr)
	}
}

func TestPrintLockWarnings_NoOpOnEmpty(t *testing.T) {
	stderr := captureStderr(t, func() { printLockWarnings(nil) })
	if len(stderr) != 0 {
		t.Errorf("stderr should be empty for nil warnings, got: %q", stderr)
	}
	stderr = captureStderr(t, func() { printLockWarnings([]string{}) })
	if len(stderr) != 0 {
		t.Errorf("stderr should be empty for empty slice, got: %q", stderr)
	}
}

func TestPrintLockWarnings_SkipsBlankEntries(t *testing.T) {
	// Defensive: a future server release that includes blank strings
	// in the warnings array should not result in noisy "WARN: " lines.
	stderr := captureStderr(t, func() {
		printLockWarnings([]string{"", "  ", "real warning"})
	})
	count := bytes.Count(stderr, []byte("WARN:"))
	if count != 1 {
		t.Errorf("expected exactly 1 WARN line, got %d: %q", count, stderr)
	}
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// what was written. Used by the bd-i2t4l tests above.
func captureStderr(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan []byte, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- buf
	}()

	fn()
	_ = w.Close()
	return <-done
}
