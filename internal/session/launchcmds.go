// bd-ntm-respawn-replay-launch-cmd-tu44r: persistent per-pane launch
// commands so `ntm respawn` can replay the exact agent CLI invocation
// (model, persona, reasoning effort, system-prompt file, env-var prefix)
// instead of relying on `tmux respawn-pane` without a command argument —
// which reuses the pane's *original* start command, i.e. the default
// shell. The workaround that lived at .flywheel/respawn-cod.sh in the
// notaryware repo (bd-cy5r) becomes redundant once this persistence path
// is shipped end-to-end.

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PaneLaunchCommand records the resolved per-pane agent command so that a
// later `ntm respawn` can replay it verbatim.
type PaneLaunchCommand struct {
	PaneID        string `json:"pane_id"`
	PaneIndex     int    `json:"pane_index"`
	PaneTitle     string `json:"pane_title,omitempty"`
	AgentType     string `json:"agent_type"`
	Model         string `json:"model,omitempty"`
	ResolvedModel string `json:"resolved_model,omitempty"`
	Persona       string `json:"persona,omitempty"`
	Command       string `json:"command"`
}

// PaneLaunchCommands is the serialised shape of the panes.json file.
type PaneLaunchCommands struct {
	Session   string              `json:"session"`
	Commands  []PaneLaunchCommand `json:"commands"`
	UpdatedAt time.Time           `json:"updated_at"`
}

// panesFilePath returns the path to the persisted launch-commands file for
// a session under ~/.ntm/sessions/<session>/panes.json.
func panesFilePath(sessionName string) (string, error) {
	dir, err := SessionDir(sessionName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "panes.json"), nil
}

// SavePaneLaunchCommands persists per-pane launch commands using an atomic
// temp-file rename so a concurrent reader never sees a partial write.
func SavePaneLaunchCommands(sessionName string, cmds []PaneLaunchCommand) error {
	path, err := panesFilePath(sessionName)
	if err != nil {
		return err
	}
	rec := PaneLaunchCommands{
		Session:   sessionName,
		Commands:  cmds,
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pane launch commands: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write pane launch commands: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomically replace pane launch commands: %w", err)
	}
	return nil
}

// LoadPaneLaunchCommands reads the persisted launch commands. Returns
// (nil, nil) when the file does not exist so callers can fall back to the
// legacy tmux respawn-pane semantics without treating "no record yet" as
// an error.
func LoadPaneLaunchCommands(sessionName string) (*PaneLaunchCommands, error) {
	path, err := panesFilePath(sessionName)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read pane launch commands: %w", err)
	}
	var rec PaneLaunchCommands
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse pane launch commands: %w", err)
	}
	return &rec, nil
}

// LookupCommandByPaneID returns the recorded command for a pane id, or the
// empty string when nothing matches. Receivers of nil are safe (callers
// that have not yet loaded commands can chain).
func (p *PaneLaunchCommands) LookupCommandByPaneID(paneID string) (PaneLaunchCommand, bool) {
	if p == nil {
		return PaneLaunchCommand{}, false
	}
	for _, c := range p.Commands {
		if c.PaneID == paneID {
			return c, true
		}
	}
	return PaneLaunchCommand{}, false
}
