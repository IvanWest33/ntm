// bd-ntm-respawn-replay-launch-cmd-tu44r tests for the per-pane launch-
// command persistence layer that `ntm respawn` consumes.

package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadPaneLaunchCommands_Roundtrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	in := []PaneLaunchCommand{
		{
			PaneID:        "%1",
			PaneIndex:     1,
			PaneTitle:     "notaryware__cod_1",
			AgentType:     "cod",
			Model:         "gpt-5",
			ResolvedModel: "gpt-5.5",
			Persona:       "codex-default",
			Command:       "codex --dangerously-bypass-approvals-and-sandbox -m gpt-5.5 -c reasoning=xhigh -c summary=experimental --search",
		},
		{
			PaneID:    "%2",
			PaneIndex: 2,
			AgentType: "cc",
			Command:   "claude --dangerously-skip-permissions --model claude-opus-4-7",
		},
	}

	if err := SavePaneLaunchCommands("test-session", in); err != nil {
		t.Fatalf("SavePaneLaunchCommands: %v", err)
	}

	got, err := LoadPaneLaunchCommands("test-session")
	if err != nil {
		t.Fatalf("LoadPaneLaunchCommands: %v", err)
	}
	if got == nil {
		t.Fatalf("LoadPaneLaunchCommands returned nil — expected the just-saved record")
	}
	if got.Session != "test-session" {
		t.Errorf("Session mismatch: got %q want %q", got.Session, "test-session")
	}
	if len(got.Commands) != 2 {
		t.Fatalf("Commands length: got %d want 2", len(got.Commands))
	}
	if got.Commands[0].Command != in[0].Command {
		t.Errorf("pane[0].Command mismatch:\n got %q\nwant %q", got.Commands[0].Command, in[0].Command)
	}
	if got.Commands[1].AgentType != "cc" {
		t.Errorf("pane[1].AgentType: got %q want %q", got.Commands[1].AgentType, "cc")
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt should be populated, got zero value")
	}
}

func TestLoadPaneLaunchCommands_MissingFileReturnsNilNotError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Sanity: we have not saved anything for this session yet.
	got, err := LoadPaneLaunchCommands("never-spawned")
	if err != nil {
		t.Fatalf("LoadPaneLaunchCommands: expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Fatalf("LoadPaneLaunchCommands: expected nil for missing file, got %#v", got)
	}
}

func TestLookupCommandByPaneID(t *testing.T) {
	p := &PaneLaunchCommands{
		Commands: []PaneLaunchCommand{
			{PaneID: "%5", Command: "codex --search"},
			{PaneID: "%7", Command: "claude --model x"},
		},
	}

	if got, ok := p.LookupCommandByPaneID("%5"); !ok || got.Command != "codex --search" {
		t.Errorf("Lookup hit: ok=%v cmd=%q want ok=true cmd=\"codex --search\"", ok, got.Command)
	}
	if _, ok := p.LookupCommandByPaneID("%99"); ok {
		t.Errorf("Lookup miss: expected ok=false for unknown pane")
	}

	// Nil receiver must be safe — callers loading a missing panes.json get
	// nil back and should still be able to chain.
	var nilP *PaneLaunchCommands
	if _, ok := nilP.LookupCommandByPaneID("%5"); ok {
		t.Errorf("nil receiver: expected ok=false")
	}
}

func TestSavePaneLaunchCommands_AtomicReplace(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// First save establishes the file.
	if err := SavePaneLaunchCommands("atomic-test", []PaneLaunchCommand{
		{PaneID: "%1", AgentType: "cod", Command: "first"},
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Second save overwrites atomically (no .tmp leak left behind).
	if err := SavePaneLaunchCommands("atomic-test", []PaneLaunchCommand{
		{PaneID: "%1", AgentType: "cod", Command: "second"},
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	dir := filepath.Join(tmp, ".ntm", "sessions", "atomic-test")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read sessions dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("found leftover .tmp file %q — atomic rename should have replaced it", e.Name())
		}
	}

	got, err := LoadPaneLaunchCommands("atomic-test")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || len(got.Commands) != 1 || got.Commands[0].Command != "second" {
		t.Fatalf("expected the second save to win, got %#v", got)
	}
}
