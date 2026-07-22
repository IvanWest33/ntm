package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TmuxTestThrottle limits concurrent tmux session spawning in tests.
// This prevents fork bombs when running tests with high parallelism.
//
// The default limit is 8 concurrent tmux-spawning tests, which is safe
// even on systems with lower process limits. Override with NTM_TEST_PARALLEL.
var TmuxTestThrottle = newThrottle(getTmuxTestLimit())

func getTmuxTestLimit() int {
	if env := os.Getenv("NTM_TEST_PARALLEL"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			return n
		}
	}
	// Default to 8, or GOMAXPROCS/8 if that's larger, capped at 16
	limit := runtime.GOMAXPROCS(0) / 8
	if limit < 8 {
		limit = 8
	}
	if limit > 16 {
		limit = 16
	}
	return limit
}

// throttle is a counting semaphore for limiting concurrent operations.
type throttle struct {
	sem chan struct{}
	mu  sync.Mutex
}

func newThrottle(limit int) *throttle {
	return &throttle{
		sem: make(chan struct{}, limit),
	}
}

// Acquire acquires a slot from the throttle, blocking if necessary.
// Returns a release function that must be called when done.
func (th *throttle) Acquire() func() {
	th.sem <- struct{}{}
	return func() {
		<-th.sem
	}
}

// AcquireForTest acquires a slot and registers cleanup to release it.
// This is the recommended way to use the throttle in tests.
func (th *throttle) AcquireForTest(t *testing.T) {
	t.Helper()
	th.sem <- struct{}{}
	t.Cleanup(func() {
		<-th.sem
	})
}

// RequireTmuxThrottled combines RequireTmux with throttle acquisition.
// Use this at the start of any test that spawns tmux sessions.
//
// Example:
//
//	func TestSpawnSession(t *testing.T) {
//	    testutil.RequireTmuxThrottled(t)
//	    // ... test code that spawns tmux sessions
//	}
func RequireTmuxThrottled(t *testing.T) {
	t.Helper()
	RequireTmux(t)
	// Cross-process lock to prevent tmux overload when `go test ./...` runs
	// multiple packages in parallel.
	acquireGlobalTmuxTestLock(t)
	TmuxTestThrottle.AcquireForTest(t)
	isolateTmuxTmpDir(t)
}

// isolateTmuxTmpDir keeps every tmux command in a test that uses the shared
// precheck away from the live user's default socket. Bare tmux commands use a
// private `default` socket beneath this directory; tests that need a named
// socket layer IsolateTmuxSocket on top.
func isolateTmuxTmpDir(t *testing.T) string {
	t.Helper()

	// Go's t.TempDir() embeds the full test name, which can exceed Unix-domain
	// socket limits once tmux adds tmux-UID/<socket-name>. Keep this private
	// root deliberately short while retaining test-scoped cleanup.
	socketRoot, err := os.MkdirTemp("/tmp", "ntm-tmux-")
	if err != nil {
		t.Fatalf("create isolated tmux socket directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketRoot); err != nil {
			t.Errorf("remove isolated tmux socket directory %q: %v", socketRoot, err)
		}
	})
	t.Setenv("TMUX_TMPDIR", socketRoot)
	t.Setenv("TMUX", "")
	t.Setenv("NTM_TMUX_SOCKET_PATH", "")
	t.Setenv("NTM_TEST_TMUX_ISOLATED", "1")
	return socketRoot
}

// IsolateTmuxSocket starts a named tmux server in a test-owned socket
// directory and routes NTM's local tmux client to it. The test remains
// detached (TMUX is empty) while TMUX_PANE can still identify its target pane.
//
// Use this before any test that creates a tmux session requiring a specific
// socket. It deliberately does not call `kill-server`: callers clean up only
// the test sessions they create.
func IsolateTmuxSocket(t *testing.T) string {
	t.Helper()

	socketRoot := isolateTmuxTmpDir(t)
	socketName := fmt.Sprintf("ntm-test-%d", time.Now().UnixNano())
	socketPath := filepath.Join(socketRoot, fmt.Sprintf("tmux-%d", os.Getuid()), socketName)
	t.Setenv("NTM_TMUX_SOCKET_PATH", socketPath)

	cmd := exec.Command(tmux.BinaryPath(), "-L", socketName, "start-server")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start isolated tmux server: %v\n%s", err, output)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("isolated tmux socket %q was not created: %v", socketPath, err)
	}

	return socketPath
}

// IntegrationTestPrecheckThrottled runs integration prechecks with throttling.
// Use this instead of IntegrationTestPrecheck for tests that spawn tmux.
func IntegrationTestPrecheckThrottled(t *testing.T) {
	t.Helper()
	RequireIntegration(t)
	RequireTmuxThrottled(t)
	RequireNTMBinary(t)
}

// E2ETestPrecheckThrottled runs E2E prechecks with throttling.
// Use this instead of E2ETestPrecheck for tests that spawn tmux.
func E2ETestPrecheckThrottled(t *testing.T) {
	t.Helper()
	RequireE2E(t)
	RequireTmuxThrottled(t)
	RequireNTMBinary(t)
}
