// Package opencodeacp binds the user's own OpenCode installation to Open Agents's
// reusable ACP Chat transport.
package opencodeacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sudo-adduser-jordan/open-agents/backend/internal/adapters/agent/opencode"
	acpdriver "github.com/sudo-adduser-jordan/open-agents/backend/internal/adapters/chatdriver/acp"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/domain"
	"github.com/sudo-adduser-jordan/open-agents/backend/internal/ports"
)

// New launches `opencode acp` from the exact binary resolved by the existing
// OpenCode agent plugin. Open Agents adds only a per-session inline overlay for its
// standing instructions and an explicit bypass-permissions choice.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessOpenCode,
		Configure:            configure,
		SessionOptions:       sessionOptions,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	if cfg.SystemPrompt == "" && ports.NormalizePermissionMode(cfg.Permissions) != ports.PermissionModeBypassPermissions {
		return []string{"acp"}, nil, nil
	}
	content, err := opencode.PrepareACPConfigContent(
		cfg.Env["OPENCODE_CONFIG_CONTENT"], cfg.SystemPrompt, string(cfg.SessionID), cfg.Permissions, cfg.Kind)
	if err != nil {
		return nil, nil, err
	}
	return []string{"acp"}, map[string]string{"OPENCODE_CONFIG_CONTENT": content}, nil
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if settings.Model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: settings.Model})
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

// OpenCode parses model overrides as provider/model. A provider display name is
// not an alias for its configured default model; keep that default only when
// the override is empty. Leave model availability to the user's OpenCode.
func validateTurnSettings(_ ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Model == "" {
		return nil
	}
	provider, model, found := strings.Cut(settings.Model, "/")
	if !found || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
		return fmt.Errorf("%w: OpenCode model %q must use provider/model format (for example, openai/gpt-5.4); select a full model ID from `opencode models`, or clear the model override to use Agent default", ports.ErrChatConfigOptionInvalid, settings.Model)
	}
	return nil
}
