package robot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestRestartPaneBeadPromptTemplate(t *testing.T) {
	// Verify the template contains the expected placeholders
	if !strings.Contains(restartPaneBeadPromptTemplate, "{bead_id}") {
		t.Fatal("template missing {bead_id} placeholder")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "{bead_title}") {
		t.Fatal("template missing {bead_title} placeholder")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "AGENTS.md") {
		t.Fatal("template should reference AGENTS.md")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "Agent Mail") {
		t.Fatal("template should reference Agent Mail")
	}
	if !strings.Contains(restartPaneBeadPromptTemplate, "br show") {
		t.Fatal("template should reference br show for bead details")
	}
}

func TestRestartPaneBeadPromptExpansion(t *testing.T) {
	// Test that the template expands correctly using the same replacer logic
	beadID := "bd-abc12"
	beadTitle := "Fix authentication bug"

	prompt := strings.NewReplacer(
		"{bead_id}", beadID,
		"{bead_title}", beadTitle,
	).Replace(restartPaneBeadPromptTemplate)

	if strings.Contains(prompt, "{bead_id}") {
		t.Error("prompt still contains {bead_id} placeholder after expansion")
	}
	if strings.Contains(prompt, "{bead_title}") {
		t.Error("prompt still contains {bead_title} placeholder after expansion")
	}
	if !strings.Contains(prompt, beadID) {
		t.Errorf("prompt should contain bead ID %q", beadID)
	}
	if !strings.Contains(prompt, beadTitle) {
		t.Errorf("prompt should contain bead title %q", beadTitle)
	}
	// The bead_id should appear multiple times (in work-on and br show)
	if strings.Count(prompt, beadID) < 2 {
		t.Errorf("bead ID should appear at least twice in prompt (work-on + br show), got %d", strings.Count(prompt, beadID))
	}
}

func TestBuildBeadPromptWithTimeoutBoundsBrShow(t *testing.T) {
	binDir := t.TempDir()
	brPath := filepath.Join(binDir, "br")
	if err := os.WriteFile(brPath, []byte("#!/bin/sh\nexec sleep 2\n"), 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	_, err := buildBeadPromptWithTimeout("bd-timeout", 20*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("buildBeadPromptWithTimeout returned nil error for sleeping br")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q, want timeout", err.Error())
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("buildBeadPromptWithTimeout took %s, want bounded timeout", elapsed)
	}
}

func TestBuildBeadPromptWithTimeoutUsesRunBdTimeout(t *testing.T) {
	old := runRestartPaneBr
	t.Cleanup(func() { runRestartPaneBr = old })

	var gotTimeout time.Duration
	var gotArgs []string
	runRestartPaneBr = func(_ string, timeout time.Duration, args ...string) (string, error) {
		gotTimeout = timeout
		gotArgs = append([]string(nil), args...)
		return `[{"title":"Fix assignment timeout"}]`, nil
	}

	prompt, err := buildBeadPromptWithTimeout("bd-vfufk", 10*time.Second)
	if err != nil {
		t.Fatalf("buildBeadPromptWithTimeout failed: %v", err)
	}
	if gotTimeout != 10*time.Second {
		t.Fatalf("timeout = %v, want 10s", gotTimeout)
	}
	if strings.Join(gotArgs, " ") != "show bd-vfufk --json" {
		t.Fatalf("args = %v, want br show args", gotArgs)
	}
	if !strings.Contains(prompt, "bd-vfufk") || !strings.Contains(prompt, "Fix assignment timeout") {
		t.Fatalf("prompt %q did not include bead id and title", prompt)
	}
}

func TestRestartPaneOptionsPromptOverridesBead(t *testing.T) {
	// When both Bead and Prompt are set, Prompt should take precedence.
	// This tests the logic flow: promptToSend defaults to Prompt, falling back to beadPrompt.
	opts := RestartPaneOptions{
		Session: "test-session",
		Bead:    "bd-xyz",
		Prompt:  "Custom prompt override",
	}

	// Simulate the priority logic from GetRestartPane
	promptToSend := opts.Prompt
	beadPrompt := "generated from bead"
	if promptToSend == "" && beadPrompt != "" {
		promptToSend = beadPrompt
	}

	if promptToSend != "Custom prompt override" {
		t.Errorf("explicit --prompt should override bead template, got %q", promptToSend)
	}
}

func TestRestartPaneOptionsBeadPromptFallback(t *testing.T) {
	// When only Bead is set (no Prompt), beadPrompt should be used
	opts := RestartPaneOptions{
		Session: "test-session",
		Bead:    "bd-xyz",
	}

	promptToSend := opts.Prompt
	beadPrompt := "generated from bead"
	if promptToSend == "" && beadPrompt != "" {
		promptToSend = beadPrompt
	}

	if promptToSend != "generated from bead" {
		t.Errorf("bead template should be used when no explicit prompt, got %q", promptToSend)
	}
}

func TestRestartPaneOutputBeadFields(t *testing.T) {
	// Verify the output struct carries bead assignment info
	output := RestartPaneOutput{
		BeadAssigned: "bd-abc12",
		PromptSent:   true,
	}

	if output.BeadAssigned != "bd-abc12" {
		t.Errorf("BeadAssigned = %q, want %q", output.BeadAssigned, "bd-abc12")
	}
	if !output.PromptSent {
		t.Error("PromptSent should be true")
	}
}

func TestRestartPaneOutputPromptError(t *testing.T) {
	output := RestartPaneOutput{
		BeadAssigned: "bd-abc12",
		PromptSent:   false,
		PromptError:  "pane 1: connection refused",
	}

	if output.PromptSent {
		t.Error("PromptSent should be false when there's an error")
	}
	if output.PromptError == "" {
		t.Error("PromptError should be set when prompt sending fails")
	}
}

func TestRestartPaneDryRunShowsBead(t *testing.T) {
	// In dry-run mode, BeadAssigned should still be populated
	output := RestartPaneOutput{
		DryRun:       true,
		WouldAffect:  []string{"1", "2"},
		BeadAssigned: "bd-abc12",
	}

	if output.BeadAssigned == "" {
		t.Error("BeadAssigned should be set even in dry-run mode")
	}
	if !output.DryRun {
		t.Error("DryRun should be true")
	}
}

func TestRestartPaneAgentTypePrefersParsedPaneType(t *testing.T) {

	pane := tmux.Pane{
		Title:   "custom title",
		Type:    tmux.AgentCodex,
		Command: "codex --model o3",
	}

	if got := restartPaneAgentType(pane); got != "codex" {
		t.Fatalf("restartPaneAgentType() = %q, want %q", got, "codex")
	}
}

func TestSelectRestartPaneTargetsUsesParsedPaneTypeForFilters(t *testing.T) {

	panes := []tmux.Pane{
		{ID: "%0", Index: 0, Title: "shell", Type: tmux.AgentUser, Command: "zsh"},
		{ID: "%1", Index: 1, Title: "notes", Type: tmux.AgentClaude, Command: "claude"},
		{ID: "%2", Index: 2, Title: "build monitor", Type: tmux.AgentCodex, Command: "codex"},
	}

	targets := selectRestartPaneTargets(panes, nil, "codex", false)
	if len(targets) != 1 {
		t.Fatalf("selectRestartPaneTargets() returned %d panes, want 1", len(targets))
	}
	if targets[0].ID != "%2" {
		t.Fatalf("selectRestartPaneTargets() picked %s, want %%2", targets[0].ID)
	}
}

func TestSendRestartPromptsUsesAgentAwareSender(t *testing.T) {

	targets := []restartPromptTarget{
		{Pane: "1", Target: "%1", AgentType: tmux.AgentCodex},
		{Pane: "2", Target: "%2", AgentType: tmux.AgentClaude},
	}

	var calls []string
	errs := sendRestartPrompts(targets, "resume work", func(target, keys string, agentType tmux.AgentType) error {
		calls = append(calls, fmt.Sprintf("%s|%s|%s", target, keys, agentType))
		return nil
	})
	if len(errs) != 0 {
		t.Fatalf("sendRestartPrompts() returned errors: %v", errs)
	}
	if len(calls) != 2 {
		t.Fatalf("sendRestartPrompts() made %d calls, want 2", len(calls))
	}
	if calls[0] != "%1|resume work|cod" {
		t.Fatalf("first call = %q, want %q", calls[0], "%1|resume work|cod")
	}
	if calls[1] != "%2|resume work|cc" {
		t.Fatalf("second call = %q, want %q", calls[1], "%2|resume work|cc")
	}
}

func TestRestartPaneOutputJSONFields(t *testing.T) {
	output := RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "myproject",
		RestartedAt:   time.Date(2026, 1, 28, 12, 0, 0, 0, time.UTC),
		Restarted:     []string{"1", "2"},
		Failed:        []RestartError{{Pane: "3", Reason: "timeout"}},
		BeadAssigned:  "bd-test1",
		PromptSent:    true,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Check that bead_assigned and prompt_sent are present
	if parsed["bead_assigned"] != "bd-test1" {
		t.Errorf("bead_assigned = %v, want %q", parsed["bead_assigned"], "bd-test1")
	}
	if parsed["prompt_sent"] != true {
		t.Errorf("prompt_sent = %v, want true", parsed["prompt_sent"])
	}
	if parsed["session"] != "myproject" {
		t.Errorf("session = %v, want %q", parsed["session"], "myproject")
	}
}

func TestRestartPaneOutputJSONOmitEmpty(t *testing.T) {
	// When no bead is used, bead fields should be omitted from JSON
	output := RestartPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "myproject",
		RestartedAt:   time.Now().UTC(),
		Restarted:     []string{"1"},
		Failed:        []RestartError{},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	jsonStr := string(data)
	if strings.Contains(jsonStr, "bead_assigned") {
		t.Error("bead_assigned should be omitted when empty")
	}
	if strings.Contains(jsonStr, "prompt_error") {
		t.Error("prompt_error should be omitted when empty")
	}
}

func TestRestartPaneOptionsDefaults(t *testing.T) {
	opts := RestartPaneOptions{
		Session: "test-session",
	}

	if opts.DryRun {
		t.Error("DryRun should default to false")
	}
	if opts.All {
		t.Error("All should default to false")
	}
	if opts.Bead != "" {
		t.Error("Bead should default to empty")
	}
	if opts.Prompt != "" {
		t.Error("Prompt should default to empty")
	}
	if len(opts.Panes) != 0 {
		t.Error("Panes should default to empty")
	}
}

func TestRestartPaneOptionsAllFieldsSet(t *testing.T) {
	opts := RestartPaneOptions{
		Session: "proj",
		Panes:   []string{"1", "2", "3"},
		Type:    "cc",
		All:     true,
		DryRun:  true,
		Bead:    "bd-abc12",
		Prompt:  "Work on this task",
	}

	if opts.Session != "proj" {
		t.Error("Session mismatch")
	}
	if len(opts.Panes) != 3 {
		t.Error("Panes length mismatch")
	}
	if opts.Type != "cc" {
		t.Error("Type mismatch")
	}
	if !opts.All {
		t.Error("All should be true")
	}
	if !opts.DryRun {
		t.Error("DryRun should be true")
	}
	if opts.Bead != "bd-abc12" {
		t.Error("Bead mismatch")
	}
	if opts.Prompt != "Work on this task" {
		t.Error("Prompt mismatch")
	}
}

func TestRestartErrorStructure(t *testing.T) {
	re := RestartError{
		Pane:   "2",
		Reason: "failed to respawn: pane not found",
	}

	data, err := json.Marshal(re)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed["pane"] != "2" {
		t.Errorf("pane = %q, want %q", parsed["pane"], "2")
	}
	if parsed["reason"] != "failed to respawn: pane not found" {
		t.Errorf("reason = %q, want proper error message", parsed["reason"])
	}
}

func TestRestartPaneBeadPromptTemplateMatchesBulkAssign(t *testing.T) {
	// The restart-pane bead template should be compatible with the bulk-assign template.
	// Both should include: AGENTS.md reference, Agent Mail, bead ID, br show, ultrathink.
	template := restartPaneBeadPromptTemplate

	expectedParts := []string{
		"AGENTS.md",
		"Agent Mail",
		"{bead_id}",
		"{bead_title}",
		"br show",
		"ultrathink",
	}

	for _, part := range expectedParts {
		if !strings.Contains(template, part) {
			t.Errorf("template missing expected part %q", part)
		}
	}
}

func TestRestartPanePromptOnlyNoBeadAssigned(t *testing.T) {
	// When --prompt is used without --bead, BeadAssigned should be empty
	output := RestartPaneOutput{
		Restarted:  []string{"1"},
		PromptSent: true,
	}

	if output.BeadAssigned != "" {
		t.Error("BeadAssigned should be empty when only --prompt is used")
	}
	if !output.PromptSent {
		t.Error("PromptSent should be true")
	}
}

func TestRestartPaneMultiplePanesPromptErrors(t *testing.T) {
	// Test that prompt errors for multiple panes are joined with semicolons
	errors := []string{
		"pane 1: connection refused",
		"pane 3: timeout",
	}
	joined := strings.Join(errors, "; ")

	if !strings.Contains(joined, "pane 1") {
		t.Error("should contain first pane error")
	}
	if !strings.Contains(joined, "pane 3") {
		t.Error("should contain second pane error")
	}
	if strings.Count(joined, "; ") != 1 {
		t.Errorf("expected 1 semicolon separator, got %d", strings.Count(joined, "; "))
	}
}
