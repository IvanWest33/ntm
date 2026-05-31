package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestAnnotateAgentMappingMailStateDetectsInactiveMappedAgents(t *testing.T) {
	entries := []agentMappingEntry{
		{Name: "AzureTower", Session: "notaryware", PaneIndex: 13, PaneID: "%57"},
		{Name: "PearlGlen", Session: "notaryware", PaneIndex: 14, PaneID: "%58"},
	}
	activeAgents := []agentmail.Agent{
		{Name: "PearlGlen"},
	}

	inactive := annotateAgentMappingMailState(entries, activeAgents)

	if len(inactive) != 1 || inactive[0] != "AzureTower" {
		t.Fatalf("inactive = %#v, want [AzureTower]", inactive)
	}
	if entries[0].MailActive == nil || *entries[0].MailActive {
		t.Fatalf("AzureTower MailActive = %v, want false", entries[0].MailActive)
	}
	if entries[1].MailActive == nil || !*entries[1].MailActive {
		t.Fatalf("PearlGlen MailActive = %v, want true", entries[1].MailActive)
	}
	if got := formatMailMappingState(entries[0]); got != "inactive" {
		t.Fatalf("AzureTower state = %q, want inactive", got)
	}
}

func TestRepairInactiveMappedAgentsUnretiresWithCachedToken(t *testing.T) {
	t.Parallel()

	var receivedArgs map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentmail.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		params, _ := req.Params.(map[string]interface{})
		if got := params["name"]; got != "unretire_agent" {
			t.Fatalf("tool = %v, want unretire_agent", got)
		}
		args, _ := params["arguments"].(map[string]interface{})
		receivedArgs = args

		payload, _ := json.Marshal(agentmail.AgentLifecycleResult{
			Status:     "active",
			AgentName:  "AzureTower",
			ProjectKey: "/abs/project",
		})
		_ = json.NewEncoder(w).Encode(agentmail.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  payload,
		})
	}))
	defer server.Close()

	client := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
	client.SetRegistrationToken("/abs/project", "AzureTower", "tok-azure")
	entries := []agentMappingEntry{
		{Name: "AzureTower", Session: "notaryware", PaneIndex: 13, PaneID: "%57"},
	}

	if !repairInactiveMappedAgents(context.Background(), client, "/abs/project", entries, []string{"AzureTower"}) {
		t.Fatal("repairInactiveMappedAgents() = false, want true")
	}
	if entries[0].MailRepaired == nil || !*entries[0].MailRepaired {
		t.Fatalf("MailRepaired = %v, want true", entries[0].MailRepaired)
	}
	if receivedArgs["registration_token"] != "tok-azure" {
		t.Fatalf("registration_token = %v, want tok-azure", receivedArgs["registration_token"])
	}
}
