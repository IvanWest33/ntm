package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestRefreshReusedAgentIdentityUnretiresAndRegistersExistingName(t *testing.T) {
	t.Parallel()

	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		params, _ := req.Params.(map[string]interface{})
		toolName, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]interface{})
		calls = append(calls, toolName)

		w.Header().Set("Content-Type", "application/json")
		writeResult := func(result interface{}) {
			resultJSON, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			if err := json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  resultJSON,
			}); err != nil {
				t.Fatalf("encode response: %v", err)
			}
		}

		switch toolName {
		case "unretire_agent":
			if got := args["project_key"]; got != "/abs/project" {
				t.Errorf("unretire project_key = %v, want /abs/project", got)
			}
			if got := args["agent_name"]; got != "AzureTower" {
				t.Errorf("unretire agent_name = %v, want AzureTower", got)
			}
			if got := args["registration_token"]; got != "TOK-OLD" {
				t.Errorf("unretire registration_token = %v, want TOK-OLD", got)
			}
			writeResult(agentmail.AgentLifecycleResult{
				Status:     "active",
				AgentName:  "AzureTower",
				ProjectKey: "/abs/project",
			})
		case "register_agent":
			if got := args["project_key"]; got != "/abs/project" {
				t.Errorf("register project_key = %v, want /abs/project", got)
			}
			if got := args["name"]; got != "AzureTower" {
				t.Errorf("register name = %v, want AzureTower", got)
			}
			if got := args["program"]; got != "codex-cli" {
				t.Errorf("register program = %v, want codex-cli", got)
			}
			if got := args["model"]; got != "gpt-5" {
				t.Errorf("register model = %v, want gpt-5", got)
			}
			if got := args["registration_token"]; got != "TOK-OLD" {
				t.Errorf("register registration_token = %v, want TOK-OLD", got)
			}
			writeResult(agentmail.Agent{
				ID:                42,
				Name:              "AzureTower",
				Program:           "codex-cli",
				Model:             "gpt-5",
				RegistrationToken: "TOK-NEW",
			})
		default:
			json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &agentmail.JSONRPCError{Code: -32601, Message: "unknown tool"},
			})
		}
	}))
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	client.SetRegistrationToken("/abs/project", "AzureTower", "TOK-OLD")

	registered, err := refreshReusedAgentIdentity(client, "/abs/project", "AzureTower", "codex-cli", "gpt-5")
	if err != nil {
		t.Fatalf("refreshReusedAgentIdentity failed: %v", err)
	}
	if registered.Name != "AzureTower" {
		t.Fatalf("registered name = %q, want AzureTower", registered.Name)
	}
	if got := client.RegistrationToken("/abs/project", "AzureTower"); got != "TOK-NEW" {
		t.Errorf("cached token = %q, want TOK-NEW", got)
	}
	if len(calls) != 2 || calls[0] != "unretire_agent" || calls[1] != "register_agent" {
		t.Fatalf("call order = %#v, want unretire_agent then register_agent", calls)
	}
}
