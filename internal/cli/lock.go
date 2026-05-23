package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func newLockCmd() *cobra.Command {
	var (
		reason string
		ttl    string
		shared bool
	)

	cmd := &cobra.Command{
		Use:   "lock <session> <patterns...>",
		Short: "Reserve files for editing via Agent Mail",
		Long: `Reserve file paths to signal intent before editing, avoiding conflicts with other agents.

File reservations are advisory locks that help coordinate multi-agent work.
Patterns support glob syntax (e.g., "src/**/*.go", "*.json").

Examples:
  ntm lock myproject "src/api/**" --reason "Implementing user endpoints"
  ntm lock myproject "src/api/**" "tests/api/**" --ttl 2h
  ntm lock myproject "docs/**" --shared     # Non-exclusive (read) lock
  ntm lock myproject "config/*.json"        # Default 1 hour TTL`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			patterns := args[1:]
			return runLock(session, patterns, reason, ttl, shared)
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Reason for the lock")
	cmd.Flags().StringVar(&ttl, "ttl", "1h", "Time to live (e.g., 30m, 2h, 24h)")
	cmd.Flags().BoolVar(&shared, "shared", false, "Non-exclusive (read) lock")

	return cmd
}

// LockResult represents the result of a lock operation.
type LockResult struct {
	Success   bool                            `json:"success"`
	Session   string                          `json:"session"`
	Agent     string                          `json:"agent"`
	Granted   []agentmail.FileReservation     `json:"granted,omitempty"`
	Conflicts []agentmail.ReservationConflict `json:"conflicts,omitempty"`
	// Warnings carries advisory notices from the agent-mail server
	// (bd-i2t4l). Mirrored from agentmail.ReservationResult.Warnings
	// so JSON callers see the same field shape. Most commonly
	// "enforcement_off_for_code_paths: ..." when reserving non-archive
	// paths (server-side enforcement only covers mail-archive paths).
	Warnings  []string                        `json:"warnings,omitempty"`
	TTL       string                          `json:"ttl"`
	ExpiresAt *time.Time                      `json:"expires_at,omitempty"`
	Error     string                          `json:"error,omitempty"`
}

func runLock(session string, patterns []string, reason, ttlStr string, shared bool) error {
	ttlDuration, err := util.ParseDuration(ttlStr)
	if err != nil {
		return fmt.Errorf("invalid TTL format '%s': use format like 30m, 1h, 1d", ttlStr)
	}
	ttlSeconds := int(ttlDuration.Seconds())
	if ttlSeconds < 60 {
		return fmt.Errorf("TTL must be at least 1 minute")
	}

	session, projectKey, err := resolveAgentMailScope(session)
	if err != nil {
		return err
	}

	sessionAgent, err := loadResolvedSessionAgent(session, projectKey)
	if err != nil {
		return fmt.Errorf("loading session agent: %w", err)
	}
	if sessionAgent == nil {
		if IsJSONOutput() {
			result := LockResult{Success: false, Session: session, Error: "Session has no Agent Mail identity"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("session '%s' has no Agent Mail identity", session)
	}

	client := newAgentMailClient(projectKey)
	if !client.IsAvailable() {
		if IsJSONOutput() {
			result := LockResult{Success: false, Session: session, Agent: sessionAgent.AgentName, Error: "Agent Mail server unavailable"}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if encErr := enc.Encode(result); encErr != nil {
				return encErr
			}
			return jsonFailureExit()
		}
		return fmt.Errorf("agent mail server unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := agentmail.FileReservationOptions{
		ProjectKey: projectKey,
		AgentName:  sessionAgent.AgentName,
		Paths:      patterns,
		TTLSeconds: ttlSeconds,
		Exclusive:  !shared,
		Reason:     reason,
	}

	reservation, err := client.ReservePaths(ctx, opts)

	result := LockResult{Session: session, Agent: sessionAgent.AgentName, TTL: ttlStr}

	if err != nil {
		if reservation != nil && len(reservation.Conflicts) > 0 {
			result.Success = false
			result.Granted = reservation.Granted
			result.Conflicts = reservation.Conflicts
			result.Warnings = reservation.Warnings
		} else {
			result.Success = false
			result.Error = err.Error()
		}
	} else {
		result.Success = true
		result.Granted = reservation.Granted
		result.Warnings = reservation.Warnings
		if len(reservation.Granted) > 0 {
			t := reservation.Granted[0].ExpiresTS.Time
			result.ExpiresAt = &t
		}
	}

	if IsJSONOutput() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(result); encErr != nil {
			return encErr
		}
		if !result.Success {
			return jsonFailureExit()
		}
		return nil
	}

	return printLockResult(result, shared)
}

func printLockResult(result LockResult, shared bool) error {
	lockType := "exclusive"
	if shared {
		lockType = "shared"
	}

	// bd-i2t4l: surface server-side advisory warnings to stderr above
	// the success line so workers see "enforcement_off_for_code_paths"
	// etc. before they assume the reservation is enforced. Forward-
	// compatible: older servers omit the field and the loop is a no-op.
	printLockWarnings(result.Warnings)

	if result.Success {
		fmt.Printf("Reserved %d path(s) (%s)\n", len(result.Granted), lockType)
		fmt.Printf("  Agent: %s\n", result.Agent)
		if result.ExpiresAt != nil {
			fmt.Printf("  Expires: %s (%s)\n", result.ExpiresAt.Format(time.RFC3339), result.TTL)
		}
		for _, r := range result.Granted {
			fmt.Printf("  [X] %s\n", r.PathPattern)
			if r.Reason != "" {
				fmt.Printf("      %s\n", r.Reason)
			}
		}
		return nil
	}

	if len(result.Conflicts) > 0 {
		fmt.Printf("Conflict detected!\n\n")
		for _, c := range result.Conflicts {
			fmt.Printf("  Pattern: %s\n", c.Path)
			fmt.Printf("  Held by: %s\n", strings.Join(c.Holders, ", "))
		}
		fmt.Println("\nOptions:")
		fmt.Println("  1. Wait for existing locks to expire")
		fmt.Println("  2. Request release from holder")
		fmt.Println("  3. Use --shared for read-only access")
		return fmt.Errorf("reservation conflicts detected")
	}

	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return fmt.Errorf("lock failed")
}

// printLockWarnings emits non-empty server-side advisory warnings to
// stderr so operators see them above the "Reserved N path(s)" success
// line. Forward-compatible with servers that don't yet ship the
// warnings[] field (the input slice is empty, the loop is a no-op).
//
// bd-i2t4l: this surfaces the "enforcement_off_for_code_paths" warning
// the upstream mcp_agent_mail PR (Dicklesworthstone#162) adds to
// file_reservation_paths responses on code-repo paths. Operators no
// longer have to parse the docstring to learn that server-side
// exclusivity is advisory-only for non-archive paths.
func printLockWarnings(warnings []string) {
	for _, w := range warnings {
		if strings.TrimSpace(w) == "" {
			continue
		}
		fmt.Fprintf(os.Stderr, "WARN: %s\n", w)
	}
}
