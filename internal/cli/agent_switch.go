// Package cli — agent-name <-> pane navigation surface (ntm#139).
//
// This file provides two CLI commands:
//
//   - `ntm switch <agent-name>`: resolve an Agent-Mail-registered agent
//     name to its session/window/pane and either `tmux select-pane`
//     (when inside tmux) or print the `tmux attach`/`select-pane`
//     command pair the operator can copy.
//   - `ntm mapping [--session=<s>]`: list the current `{agent-name ->
//     session, window, pane.Index, pane.ID, agent_type}` mapping as
//     either a table or JSON for downstream tooling.
//
// The lookup primitive already existed inside `internal/robot/routing.go`
// (`AgentScorer.GetAgentNameForPane` + `LoadAgentMappingFromRegistry`),
// backed by the persisted `agentmail.SessionAgentRegistry`. This file is
// the CLI surface on top of that primitive.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// agentMappingEntry is the JSON shape for one row in `ntm mapping`.
type agentMappingEntry struct {
	Name         string `json:"name"`
	Session      string `json:"session"`
	PaneIndex    int    `json:"pane_index"`
	PaneID       string `json:"pane_id"`
	PaneTitle    string `json:"pane_title,omitempty"`
	MailActive   *bool  `json:"mail_active,omitempty"`
	MailRepaired *bool  `json:"mail_repaired,omitempty"`
	MailError    string `json:"mail_error,omitempty"`
}

// agentMappingOutput is the JSON envelope.
type agentMappingOutput struct {
	Session        string              `json:"session"`
	Count          int                 `json:"count"`
	MailChecked    bool                `json:"mail_checked,omitempty"`
	MailRepaired   bool                `json:"mail_repaired,omitempty"`
	InactiveAgents []string            `json:"inactive_agents,omitempty"`
	Entries        []agentMappingEntry `json:"entries"`
}

// loadMappingForSession reads the persisted SessionAgentRegistry for
// `sessionName` and resolves each entry to a live tmux pane (by ID
// first, falling back to pane-title match) so callers see only entries
// that actually exist right now.
func loadMappingForSession(sessionName string) ([]agentMappingEntry, error) {
	entries, _, err := loadMappingForSessionWithRegistry(sessionName)
	return entries, err
}

func loadMappingForSessionWithRegistry(sessionName string) ([]agentMappingEntry, *agentmail.SessionAgentRegistry, error) {
	if sessionName == "" {
		return nil, nil, fmt.Errorf("session name is required")
	}
	registry, err := agentmail.LoadBestSessionAgentRegistry(sessionName, "")
	if err != nil {
		return nil, nil, fmt.Errorf("loading agent registry: %w", err)
	}
	if registry == nil {
		return nil, nil, nil
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return nil, nil, fmt.Errorf("listing tmux panes: %w", err)
	}

	// Build pane lookups by ID and by title so we can match registry
	// entries against the live panes.
	byID := make(map[string]tmux.Pane, len(panes))
	byTitle := make(map[string]tmux.Pane, len(panes))
	for _, p := range panes {
		byID[p.ID] = p
		if p.Title != "" {
			byTitle[p.Title] = p
		}
	}

	seen := make(map[string]bool) // dedupe by agent name across both maps
	var entries []agentMappingEntry

	for paneID, name := range registry.PaneIDMap {
		if seen[name] {
			continue
		}
		if p, ok := byID[paneID]; ok {
			seen[name] = true
			entries = append(entries, agentMappingEntry{
				Name:      name,
				Session:   sessionName,
				PaneIndex: p.Index,
				PaneID:    p.ID,
				PaneTitle: p.Title,
			})
		}
	}
	for paneTitle, name := range registry.Agents {
		if seen[name] {
			continue
		}
		if p, ok := byTitle[paneTitle]; ok {
			seen[name] = true
			entries = append(entries, agentMappingEntry{
				Name:      name,
				Session:   sessionName,
				PaneIndex: p.Index,
				PaneID:    p.ID,
				PaneTitle: p.Title,
			})
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, registry, nil
}

func newSwitchAgentCmd() *cobra.Command {
	var sessionFlag string
	cmd := &cobra.Command{
		Use:   "switch <agent-name>",
		Short: "Switch tmux focus to a pane by Agent Mail agent name",
		Long: `Resolves <agent-name> through the per-session Agent Mail registry
to a live tmux pane and either selects it (when ntm is invoked from
inside tmux) or prints the tmux command the operator can run.

Examples:

  ntm switch claude-alpha
  ntm switch codex-bravo --session=my-project

See ntm#139.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentName := strings.TrimSpace(args[0])
			if agentName == "" {
				return fmt.Errorf("agent name cannot be empty")
			}

			sessions := []string{sessionFlag}
			if sessionFlag == "" {
				// No session pinned — search every active tmux session.
				all, err := tmux.ListSessions()
				if err != nil {
					return fmt.Errorf("listing tmux sessions: %w", err)
				}
				sessions = sessions[:0]
				for _, s := range all {
					sessions = append(sessions, s.Name)
				}
			}

			for _, s := range sessions {
				entries, err := loadMappingForSession(s)
				if err != nil {
					// Don't abort the whole search on a per-session error —
					// the registry may legitimately be missing for some
					// sessions. Surface as a warning to stderr.
					fmt.Fprintf(os.Stderr, "warning: %s: %v\n", s, err)
					continue
				}
				for _, e := range entries {
					if e.Name != agentName {
						continue
					}
					// Found it. If we're inside tmux, select the pane.
					// Otherwise emit the copy-pasteable invocation.
					if os.Getenv("TMUX") != "" {
						// When the target is in a different session
						// than the user's attached client, `select-pane`
						// alone updates which pane is "current" in the
						// target session but does NOT move the attached
						// client — the user sees no change. Pair it
						// with `switch-client -t <session>` so the user
						// actually lands on the target. Running
						// `switch-client` against the already-attached
						// session is a tmux no-op, so this is safe even
						// when the target is in the current session.
						if _, err := tmux.DefaultClient.Run("switch-client", "-t", e.Session); err != nil {
							return fmt.Errorf("tmux switch-client %s: %w", e.Session, err)
						}
						if _, err := tmux.DefaultClient.Run("select-pane", "-t", e.PaneID); err != nil {
							return fmt.Errorf("tmux select-pane %s: %w", e.PaneID, err)
						}
						if IsJSONOutput() {
							return encodeAgentSwitchJSON(e, "selected")
						}
						fmt.Printf("Switched to pane %s (%s, pane %d) in session %s\n",
							e.PaneID, e.Name, e.PaneIndex, e.Session)
						return nil
					}
					if IsJSONOutput() {
						return encodeAgentSwitchJSON(e, "instructions")
					}
					fmt.Printf("Agent %s is in session %s at pane %s (index %d).\n",
						e.Name, e.Session, e.PaneID, e.PaneIndex)
					fmt.Printf("Run:\n  tmux attach -t %s\n  tmux select-pane -t %s\n",
						e.Session, e.PaneID)
					return nil
				}
			}
			return fmt.Errorf("no agent named %q found in any session", agentName)
		},
	}
	cmd.Flags().StringVar(&sessionFlag, "session", "", "Restrict lookup to a single session (default: search all)")
	return cmd
}

func encodeAgentSwitchJSON(e agentMappingEntry, action string) error {
	envelope := map[string]any{
		"action":     action,
		"agent_name": e.Name,
		"session":    e.Session,
		"pane_index": e.PaneIndex,
		"pane_id":    e.PaneID,
		"pane_title": e.PaneTitle,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(envelope)
}

func newMappingCmd() *cobra.Command {
	var sessionFlag string
	var checkMail bool
	var repairMail bool
	cmd := &cobra.Command{
		Use:   "mapping",
		Short: "Show the agent-name <-> pane mapping for a session",
		Long: `Lists the current Agent Mail name to tmux pane mapping. Drives
both human-readable output and a stable JSON envelope for tooling.

Examples:

  ntm mapping --session=my-project
  ntm mapping --session=my-project --json

See ntm#139.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionFlag == "" {
				return fmt.Errorf("--session is required")
			}
			entries, registry, err := loadMappingForSessionWithRegistry(sessionFlag)
			if err != nil {
				return err
			}
			mailChecked := checkMail || repairMail
			mailRepaired := false
			var inactive []string
			var checkErr error
			if mailChecked {
				projectKey := ""
				if registry != nil {
					projectKey = strings.TrimSpace(registry.ProjectKey)
				}
				if projectKey == "" {
					projectKey = GetProjectRoot()
				}
				client := newAgentMailClient(projectKey)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if !client.IsAvailable() {
					checkErr = fmt.Errorf("agent mail server not available")
				} else {
					agents, err := client.ListProjectAgents(ctx, projectKey)
					if err != nil {
						checkErr = fmt.Errorf("listing active Agent Mail agents: %w", err)
					} else {
						inactive = annotateAgentMappingMailState(entries, agents)
						if repairMail && len(inactive) > 0 {
							mailRepaired = repairInactiveMappedAgents(ctx, client, projectKey, entries, inactive)
							agents, err = client.ListProjectAgents(ctx, projectKey)
							if err != nil {
								checkErr = fmt.Errorf("listing active Agent Mail agents after repair: %w", err)
							} else {
								inactive = annotateAgentMappingMailState(entries, agents)
							}
						}
						if checkErr == nil && len(inactive) > 0 {
							checkErr = fmt.Errorf("inactive Agent Mail mappings: %s", strings.Join(inactive, ", "))
						}
					}
				}
			}
			out := agentMappingOutput{
				Session:        sessionFlag,
				Count:          len(entries),
				MailChecked:    mailChecked,
				MailRepaired:   mailRepaired,
				InactiveAgents: inactive,
				Entries:        entries,
			}
			if IsJSONOutput() {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(out); err != nil {
					return err
				}
				return checkErr
			}
			if len(entries) == 0 {
				fmt.Printf("(no registered agents in session %q)\n", sessionFlag)
				return checkErr
			}
			if mailChecked {
				fmt.Printf("%-30s  %-6s  %-10s  %s\n", "agent", "pane", "mail", "pane-id")
				for _, e := range entries {
					fmt.Printf("%-30s  %-6d  %-10s  %s\n", e.Name, e.PaneIndex, formatMailMappingState(e), e.PaneID)
				}
			} else {
				fmt.Printf("%-30s  %-6s  %s\n", "agent", "pane", "pane-id")
				for _, e := range entries {
					fmt.Printf("%-30s  %-6d  %s\n", e.Name, e.PaneIndex, e.PaneID)
				}
			}
			return checkErr
		},
	}
	cmd.Flags().StringVar(&sessionFlag, "session", "", "Session name (required)")
	cmd.Flags().BoolVar(&checkMail, "check-mail", false, "Verify mapped live-pane identities are active Agent Mail agents")
	cmd.Flags().BoolVar(&repairMail, "repair-mail", false, "Unretire inactive mapped live-pane Agent Mail identities, then re-check")
	return cmd
}

func annotateAgentMappingMailState(entries []agentMappingEntry, activeAgents []agentmail.Agent) []string {
	active := make(map[string]bool, len(activeAgents))
	for _, agent := range activeAgents {
		if agent.Name != "" {
			active[agent.Name] = true
		}
	}

	inactive := make([]string, 0)
	for i := range entries {
		ok := active[entries[i].Name]
		entries[i].MailActive = boolPtr(ok)
		if !ok {
			inactive = append(inactive, entries[i].Name)
		}
	}
	sort.Strings(inactive)
	return inactive
}

func repairInactiveMappedAgents(ctx context.Context, client *agentmail.Client, projectKey string, entries []agentMappingEntry, inactive []string) bool {
	if client == nil || len(inactive) == 0 {
		return false
	}
	inactiveSet := make(map[string]struct{}, len(inactive))
	for _, name := range inactive {
		inactiveSet[name] = struct{}{}
	}

	repairedAny := false
	for i := range entries {
		if _, ok := inactiveSet[entries[i].Name]; !ok {
			continue
		}
		_, err := client.UnretireAgent(ctx, projectKey, entries[i].Name)
		if err != nil {
			entries[i].MailError = err.Error()
			entries[i].MailRepaired = boolPtr(false)
			continue
		}
		repairedAny = true
		entries[i].MailError = ""
		entries[i].MailRepaired = boolPtr(true)
	}
	return repairedAny
}

func formatMailMappingState(entry agentMappingEntry) string {
	if entry.MailActive == nil {
		return "-"
	}
	if *entry.MailActive {
		if entry.MailRepaired != nil && *entry.MailRepaired {
			return "repaired"
		}
		return "active"
	}
	if entry.MailError != "" {
		return "error"
	}
	return "inactive"
}

func boolPtr(value bool) *bool {
	return &value
}
