package hooks

import (
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// =============================================================================
// PrintPreCommitResult — 0% → 100%
// =============================================================================

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	out, _ := io.ReadAll(r)
	return string(out)
}

func TestPrintPreCommitResult_Passed(t *testing.T) {
	result := &PreCommitResult{
		Passed:       true,
		StagedFiles:  []string{"main.go", "util.go"},
		Duration:     150 * time.Millisecond,
		UBSAvailable: true,
		ScanResult: &scanner.ScanResult{
			Totals: scanner.ScanTotals{Critical: 0, Warning: 0, Info: 1},
		},
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if len(output) == 0 {
		t.Error("expected non-empty output")
	}
	// Should contain staged files count
	if !contains(output, "Staged files: 2") {
		t.Errorf("missing staged files count in output: %s", output)
	}
	// Should contain pass indicator
	if !contains(output, "passed") {
		t.Errorf("missing pass indicator: %s", output)
	}
}

func TestPrintPreCommitResult_Failed(t *testing.T) {
	result := &PreCommitResult{
		Passed:       false,
		StagedFiles:  []string{"bad.go"},
		Duration:     200 * time.Millisecond,
		UBSAvailable: true,
		BlockReason:  "critical findings",
		ScanResult: &scanner.ScanResult{
			Totals: scanner.ScanTotals{Critical: 2, Warning: 1, Info: 0},
			Findings: []scanner.Finding{
				{File: "bad.go", Line: 10, Severity: scanner.SeverityCritical, Message: "SQL injection"},
				{File: "bad.go", Line: 20, Severity: scanner.SeverityWarning, Message: "unused var"},
			},
		},
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if !contains(output, "failed") {
		t.Errorf("missing fail indicator: %s", output)
	}
	if !contains(output, "critical findings") {
		t.Errorf("missing block reason: %s", output)
	}
}

func TestPrintPreCommitResult_UBSNotAvailable(t *testing.T) {
	result := &PreCommitResult{
		Passed:       true,
		StagedFiles:  []string{"main.go"},
		Duration:     50 * time.Millisecond,
		UBSAvailable: false,
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if !contains(output, "UBS not installed") {
		t.Errorf("missing UBS warning: %s", output)
	}
}

func TestPrintPreCommitResult_NoStagedFiles(t *testing.T) {
	result := &PreCommitResult{
		Passed:       true,
		StagedFiles:  nil,
		Duration:     10 * time.Millisecond,
		UBSAvailable: true,
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	if !contains(output, "No staged files") {
		t.Errorf("missing 'no staged files' message: %s", output)
	}
}

func TestPrintPreCommitResult_ManyFindings(t *testing.T) {
	findings := make([]scanner.Finding, 8)
	for i := range findings {
		findings[i] = scanner.Finding{
			File: "file.go", Line: i + 1,
			Severity: scanner.SeverityWarning, Message: "issue",
		}
	}

	result := &PreCommitResult{
		Passed:       false,
		StagedFiles:  []string{"file.go"},
		Duration:     100 * time.Millisecond,
		UBSAvailable: true,
		BlockReason:  "too many warnings",
		ScanResult: &scanner.ScanResult{
			Totals:   scanner.ScanTotals{Warning: 8},
			Findings: findings,
		},
	}

	output := captureStdout(t, func() {
		PrintPreCommitResult(result)
	})

	// Should truncate to 5 and show "... and 3 more"
	if !contains(output, "and 3 more") {
		t.Errorf("missing truncation message: %s", output)
	}
}

// =============================================================================
// ExitCode — 0% → 100%
// =============================================================================

func TestExitCode_Passed(t *testing.T) {
	t.Parallel()
	r := &PreCommitResult{Passed: true}
	if code := r.ExitCode(); code != 0 {
		t.Errorf("ExitCode() = %d, want 0", code)
	}
}

func TestExitCode_Failed(t *testing.T) {
	t.Parallel()
	r := &PreCommitResult{Passed: false}
	if code := r.ExitCode(); code != 1 {
		t.Errorf("ExitCode() = %d, want 1", code)
	}
}

// =============================================================================
// getStagedFiles — 0% → 100%
// =============================================================================

func setupHooksGitRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = tmp
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("%v failed: %v\n%s", args, err, out)
		}
	}
	return tmp
}

func TestGetStagedFiles_NoStaged(t *testing.T) {
	t.Parallel()
	repoDir := setupHooksGitRepo(t)

	files, err := getStagedFiles(repoDir)
	if err != nil {
		t.Fatalf("getStagedFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("got %v, want empty for no staged files", files)
	}
}

func TestGetStagedFiles_WithStaged(t *testing.T) {
	t.Parallel()
	repoDir := setupHooksGitRepo(t)

	// Create a file and stage it
	filePath := repoDir + "/hello.go"
	if err := os.WriteFile(filePath, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", "hello.go")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	files, err := getStagedFiles(repoDir)
	if err != nil {
		t.Fatalf("getStagedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "hello.go" {
		t.Errorf("got %v, want [hello.go]", files)
	}
}

func TestGetStagedFiles_InvalidRepo(t *testing.T) {
	t.Parallel()
	_, err := getStagedFiles("/nonexistent-hooks-test-dir")
	if err == nil {
		t.Error("expected error for invalid repo")
	}
}

func TestNewIssueTotalsSubtractsBaselineWithoutGoingNegative(t *testing.T) {
	t.Parallel()
	got := newIssueTotals(
		scanner.ScanTotals{Critical: 5, Warning: 1, Info: 10, Files: 2},
		scanner.ScanTotals{Critical: 3, Warning: 2, Info: 10, Files: 2},
	)
	want := scanner.ScanTotals{Critical: 2, Warning: 0, Info: 0, Files: 2}
	if got != want {
		t.Fatalf("newIssueTotals() = %+v, want %+v", got, want)
	}
}

func TestSafeRepoRelativePathRejectsEscapes(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../x.go", "/tmp/x.go", ".."} {
		if got, ok := safeRepoRelativePath(path); ok {
			t.Fatalf("safeRepoRelativePath(%q) = %q, true; want rejection", path, got)
		}
	}
	if got, ok := safeRepoRelativePath("internal/hooks/precommit.go"); !ok || got != "internal/hooks/precommit.go" {
		t.Fatalf("safeRepoRelativePath valid path = %q, %v", got, ok)
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
