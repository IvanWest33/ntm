package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newMessageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "message",
		Short: "Agent Mail messaging",
	}

	cmd.AddCommand(
		newMessageInboxCmd(),
		newMessageSendCmd(),
		newMessageReadCmd(),
		newMessageAckCmd(),
	)

	return cmd
}

func newMessageInboxCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inbox [session]",
		Short: "View unified inbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, agentName, err := resolveMessageCommandScopeFromArgs(args)
			if err != nil {
				return err
			}
			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			msgs, err := unified.Inbox(context.Background())
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(msgs)
			}

			t := output.NewTable(cmd.OutOrStdout(), "ID", "Channel", "From", "Subject", "Time")
			for _, m := range msgs {
				t.AddRow(m.ID, m.Channel, m.From, m.Subject, m.Timestamp.Format(time.Kitchen))
			}
			t.Render()
			return nil
		},
	}
}

func newMessageSendCmd() *cobra.Command {
	var subject string
	cmd := &cobra.Command{
		Use:   "send <to> <body>",
		Short: "Send message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			to := args[0]
			body := args[1]

			projectDir, agentName, err := resolveMessageCommandScope()
			if err != nil {
				return err
			}
			to = resolveMessageSendRecipient(to, projectDir, agentName)
			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			return unified.Send(context.Background(), to, subject, body)
		},
	}
	cmd.Flags().StringVar(&subject, "subject", "(No Subject)", "Message subject")
	return cmd
}

func newMessageReadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "read <msg-id>",
		Short: "Read a message by ID",
		Long: `Read a message by its unified ID (e.g., "am-123").
This marks the message as read.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			projectDir, agentName, err := resolveMessageCommandScope()
			if err != nil {
				return err
			}
			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			msg, err := unified.Read(context.Background(), id)
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(msg)
			}

			fmt.Printf("ID:      %s\n", msg.ID)
			fmt.Printf("Channel: %s\n", msg.Channel)
			if msg.From != "" {
				fmt.Printf("From:    %s\n", msg.From)
			}
			if msg.Subject != "" {
				fmt.Printf("Subject: %s\n", msg.Subject)
			}
			if !msg.Timestamp.IsZero() {
				fmt.Printf("Time:    %s\n", msg.Timestamp.Format(time.RFC3339))
			}
			if msg.Body != "" {
				fmt.Printf("\n%s\n", msg.Body)
			}
			return nil
		},
	}
}

func newMessageAckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ack <msg-id>",
		Short: "Acknowledge a message by ID",
		Long: `Acknowledge a message by its unified ID (e.g., "am-123").
This marks the message as both read and acknowledged.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			projectDir, agentName, err := resolveMessageCommandScope()
			if err != nil {
				return err
			}

			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			if err := unified.Ack(context.Background(), id); err != nil {
				return err
			}

			fmt.Printf("Message %s acknowledged.\n", id)
			return nil
		},
	}
}

func resolveMessageScope(session string) (string, string, error) {
	return resolveMessageScopeForSession(session, false)
}

func resolveMessageCommandScope() (string, string, error) {
	return resolveMessageScopeForSession(tmux.GetCurrentSession(), true)
}

func resolveMessageCommandScopeFromArgs(args []string) (string, string, error) {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return resolveMessageScope(args[0])
	}
	return resolveMessageCommandScope()
}

func resolveMessageSendRecipient(to, projectDir, agentName string) string {
	to = strings.TrimSpace(to)
	projectDir = strings.TrimSpace(projectDir)
	agentName = strings.TrimSpace(agentName)
	if to == "" || projectDir == "" || agentName == "" {
		return to
	}
	if to == projectDir || to == filepath.Base(projectDir) {
		return agentName
	}
	return to
}

func resolveMessageScopeForSession(session string, inferredCurrentSession bool) (string, string, error) {
	explicitSession := strings.TrimSpace(session) != ""
	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(session, !IsJSONOutput())
		if err != nil {
			return "", "", err
		}
		session = resolved
	}

	projectDir := ""
	var err error
	if session != "" {
		if inferredCurrentSession {
			projectDir = resolveProjectDirForSession(session, false)
			if projectDir == "" {
				projectDir = GetProjectRoot()
			}
			if !sessionLooksScopedToProject(session, projectDir) {
				session = defaultMessageSessionForProject(projectDir)
			}
		} else {
			projectDir, err = resolveExplicitProjectDirForSession(session)
			if err != nil {
				return "", "", err
			}
		}
		projectDir = refineAgentMailProjectKey(session, projectDir)
	} else {
		projectDir = GetProjectRoot()
	}
	if projectDir == "" {
		return "", "", fmt.Errorf("getting project root failed")
	}

	if session == "" {
		session = defaultMessageSessionForProject(projectDir)
	}
	projectDir = refineAgentMailProjectKey(session, projectDir)

	if explicitSession || inferredCurrentSession {
		if agentName := resolveSessionPaneAgentName(session, projectDir); agentName != "" {
			return projectDir, agentName, nil
		}
	}

	agentName := fmt.Sprintf("ntm_%s", session)
	if info, err := agentmail.LoadBestSessionAgent(session, projectDir); err == nil && info != nil && strings.TrimSpace(info.AgentName) != "" {
		agentName = info.AgentName
	}

	return projectDir, agentName, nil
}

func sessionLooksScopedToProject(session, projectDir string) bool {
	session = strings.TrimSpace(session)
	projectDir = strings.TrimSpace(projectDir)
	if session == "" || projectDir == "" {
		return false
	}
	if session == filepath.Base(projectDir) {
		return true
	}

	_, sessionProject, savedProject := projectDirCandidatesForSession(session, false)
	resolved := bestUsableProjectDir(savedProject, sessionProject)
	return resolved != "" && filepath.Clean(resolved) == filepath.Clean(projectDir)
}

func defaultMessageSessionForProject(projectDir string) string {
	if sessionList, err := tmux.ListSessions(); err == nil {
		if inferred, _ := inferSessionFromCWD(sessionList); inferred != "" {
			return inferred
		}
	}
	return filepath.Base(projectDir)
}
