package chat

import (
	"testing"

	"github.com/sudo-adduser-jordan/open-agents/backend/internal/domain"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/ports"
)

func TestSettingsFromConfigOptionsKeepsAgentModelAndEffortAcrossRestart(t *testing.T) {
	t.Parallel()
	settings, changed := settingsFromConfigOptions(domain.ConversationSettings{
		ApprovalMode: domain.PermissionModeBypassPermissions,
	}, []ports.ChatConfigOption{
		{ID: "model", Category: "model", Current: ports.ChatConfigOptionValue{Select: "gpt-5.4"}},
		{ID: "effort", Category: "thought_level", Current: ports.ChatConfigOptionValue{Select: "high"}},
	})
	if !changed {
		t.Fatal("settings should change")
	}
	if settings.Model != "gpt-5.4" || settings.ApprovalMode != domain.PermissionModeBypassPermissions {
		t.Fatalf("settings = %+v, want model while preserving approval", settings)
	}
}

func TestPermissionConfigOptionsLeaveProviderCatalogUntouched(t *testing.T) {
	t.Parallel()
	input := []ports.ChatConfigOption{{
		ID:      "mode",
		Current: ports.ChatConfigOptionValue{Select: "manual"},
		Choices: []ports.ChatConfigOptionChoice{{Value: "manual"}, {Value: "acceptEdits"}, {Value: "bypassPermissions"}},
	}}
	got := permissionConfigOptions(input)
	if len(got) != 1 || len(got[0].Choices) != 3 {
		t.Fatalf("options = %+v, want provider catalog passed through", got)
	}
	for _, choice := range got[0].Choices {
		if choice.PermissionMode != "" {
			t.Fatalf("opencode choice %q was mapped to Open Agents permission mode %q", choice.Value, choice.PermissionMode)
		}
	}
}
