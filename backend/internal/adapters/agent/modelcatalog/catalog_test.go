package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sudo-adduser-jordan/open-agents/backend/internal/ports"
)

func TestModelCommandUsesProjectWorkingDirectory(t *testing.T) {
	cmd := modelCommand(context.Background(), "agent", []string{"models"}, "/work/project", map[string]string{"OPENCODE_CONFIG": "/project/opencode.json"})
	if cmd.Dir != "/work/project" {
		t.Fatalf("Dir = %q, want /work/project", cmd.Dir)
	}
	if cmd.WaitDelay != commandTerminationWait {
		t.Fatalf("WaitDelay = %s, want %s", cmd.WaitDelay, commandTerminationWait)
	}
	if !environmentContains(cmd.Env, "OPENCODE_CONFIG=/project/opencode.json") {
		t.Fatalf("Env does not contain project override: %#v", cmd.Env)
	}
}

func environmentContains(env []string, wanted string) bool {
	for _, item := range env {
		if item == wanted {
			return true
		}
	}
	return false
}

func TestCommandDiscoveryTimeoutAllowsSlowModelRegistries(t *testing.T) {
	if commandTimeout < 20*time.Second {
		t.Fatalf("commandTimeout = %s, want at least 20s", commandTimeout)
	}
}

func TestModelDiscoveryErrorExplainsTimeout(t *testing.T) {
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	err := modelDiscoveryError(deadlineCtx, "kilocode", errors.New("signal: killed"))
	if !strings.Contains(err.Error(), "kilocode model discovery timed out after 20s") {
		t.Fatalf("error = %q, want clear timeout", err)
	}
}

// An agent CLI reports no rates, so the cost class comes from the id's own
// naming. The direction of the guess matters: calling a paid model free is what
// costs money, so only an explicit free marker counts as free.
func TestParseIDLinesClassifiesCostFromID(t *testing.T) {
	output := []byte(strings.Join([]string{
		"opencode/big-pickle",
		"opencode/ling-3.0-flash-fin-free",
		"opencode/mimo-v2.6-flash-free",
		"opencode/nemotron-3-ultra-free",
		"opencode/space-bunny-free",
		"openrouter/some-model:free",
		"openai/gpt-4o",
	}, "\n"))
	models, err := parseIDLines(output)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ports.AgentModelCost{}
	for _, m := range models {
		got[m.ID] = m.Cost
	}
	for _, want := range []string{
		"opencode/ling-3.0-flash-fin-free",
		"opencode/mimo-v2.6-flash-free",
		"opencode/nemotron-3-ultra-free",
		"opencode/space-bunny-free",
		"openrouter/some-model:free",
	} {
		if got[want] != ports.AgentModelCostFree {
			t.Fatalf("%s cost = %q, want free", want, got[want])
		}
	}
	for _, want := range []string{"opencode/big-pickle", "openai/gpt-4o"} {
		if got[want] != ports.AgentModelCostPaid {
			t.Fatalf("%s cost = %q, want paid", want, got[want])
		}
	}
}

func TestClassifyModelIDIsCaseInsensitive(t *testing.T) {
	if got := classifyModelID("opencode/Model-FREE"); got != ports.AgentModelCostFree {
		t.Fatalf("cost = %q, want free", got)
	}
}

func TestOpenCodeDiscoveryUsesPureMode(t *testing.T) {
	spec := commandSpecs["opencode"]
	if len(spec.args) != 2 || spec.args[0] != "--pure" || spec.args[1] != "models" {
		t.Fatalf("opencode discovery args = %q, want [--pure models]", spec.args)
	}
}

func TestAiderUsesDocumentedDiscoveryCommand(t *testing.T) {
	spec := commandSpecs["aider"]
	want := []string{"--no-check-update", "--no-git", "--no-gitignore", "--no-analytics", "--list-models", "."}
	if strings.Join(spec.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("aider discovery args = %q, want %q", spec.args, want)
	}
}

func TestOMPAndHelpBackedAgentsUseDocumentedDiscoveryCommands(t *testing.T) {
	tests := []struct {
		agent string
		want  []string
	}{
		{agent: "omp", want: []string{"models", "--json"}},
		{agent: "copilot", want: []string{"help", "config"}},
		{agent: "droid", want: []string{"exec", "--help"}},
		{agent: "crush", want: []string{"models"}},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			spec, ok := commandSpecs[tc.agent]
			if !ok {
				t.Fatalf("%s has no discovery command", tc.agent)
			}
			if !reflect.DeepEqual(spec.args, tc.want) {
				t.Fatalf("%s discovery args = %q, want %q", tc.agent, spec.args, tc.want)
			}
			if spec.parser == nil {
				t.Fatalf("%s discovery parser is nil", tc.agent)
			}
		})
	}
}

func TestBaseClassifiesStaticTextAndModeAgents(t *testing.T) {
	tests := []struct {
		agent string
		mode  ports.ModelSelectionMode
		count int
	}{
		{agent: "opencode", mode: ports.ModelSelectionCatalog},
		{agent: "amp", mode: ports.ModelSelectionModeList, count: 4},
		{agent: "muse", mode: ports.ModelSelectionCatalog, count: 3},
		{agent: "aider", mode: ports.ModelSelectionCatalog},
		{agent: "autohand", mode: ports.ModelSelectionCatalog},
		{agent: "kimchi", mode: ports.ModelSelectionCatalog},
		{agent: "prime-agent", mode: ports.ModelSelectionCatalog},
		{agent: "qwen", mode: ports.ModelSelectionCatalog},
		{agent: "copilot", mode: ports.ModelSelectionCatalog},
		{agent: "droid", mode: ports.ModelSelectionCatalog},
		{agent: "continue", mode: ports.ModelSelectionCatalog},
		{agent: "crush", mode: ports.ModelSelectionCatalog},
		{agent: "omp", mode: ports.ModelSelectionCatalog},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			got := Base(tc.agent)
			if got.SelectionMode != tc.mode || len(got.Models) != tc.count {
				t.Fatalf("Base(%q) = %#v", tc.agent, got)
			}
		})
	}
}

func TestMuseReturnsStaticCatalogWithoutStartingAgent(t *testing.T) {
	got, err := (Discoverer{}).Discover(context.Background(), ports.AgentModelDiscoveryRequest{
		AgentID: "muse",
		Binary:  "/missing/muse",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "muse-spark", Label: "Muse Spark", IsDefault: true},
		{ID: "muse-spark-1.1", Label: "Muse Spark 1.1"},
		{ID: "muse-spark-1.2", Label: "Muse Spark 1.2"},
	}
	if got.Source != "official-catalog" || !reflect.DeepEqual(got.Models, want) {
		t.Fatalf("catalog = %#v, want models %#v", got, want)
	}
}

func TestCustomModelEntryPolicy(t *testing.T) {
	tests := []struct {
		agent         string
		wantEntryMode string
		wantSelection ports.ModelSelectionMode
	}{
		{agent: "opencode", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "grok", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "cursor", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "qwen", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "copilot", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kimi", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "muse", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "droid", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "amp", wantEntryMode: "none", wantSelection: ports.ModelSelectionModeList},
		{agent: "agy", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "crush", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "aider", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "goose", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
		{agent: "auggie", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "continue", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "devin", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "omp", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "cline", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kiro", wantEntryMode: "none", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kilocode", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "vibe", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "pi", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "kimchi", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "prime-agent", wantEntryMode: "configured", wantSelection: ports.ModelSelectionCatalog},
		{agent: "autohand", wantEntryMode: "direct", wantSelection: ports.ModelSelectionCatalog},
	}

	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			got := Base(tc.agent)
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if wire["customModelEntry"] != tc.wantEntryMode {
				t.Fatalf("Base(%q) customModelEntry = %#v, want %q", tc.agent, wire["customModelEntry"], tc.wantEntryMode)
			}
			if got.AllowCustom != (tc.wantEntryMode == "direct") {
				t.Fatalf("Base(%q) allowCustom = %v, want %v", tc.agent, got.AllowCustom, tc.wantEntryMode == "direct")
			}
			if got.SelectionMode != tc.wantSelection {
				t.Fatalf("Base(%q) selectionMode = %q, want %q", tc.agent, got.SelectionMode, tc.wantSelection)
			}
		})
	}
}

func TestPrimeAgentDiscoveryUsesDocumentedModelCommand(t *testing.T) {
	spec := commandSpecs["prime-agent"]
	want := []string{"model", "list"}
	if !reflect.DeepEqual(spec.args, want) {
		t.Fatalf("prime-agent discovery args = %q, want %q", spec.args, want)
	}
	if spec.parser == nil {
		t.Fatal("prime-agent parser is nil")
	}
}

func TestParsePrimeAgentModelsBuildsProviderQualifiedIDs(t *testing.T) {
	got, err := parsePiModels([]byte(`provider   model                 context  max-out  thinking  images
openai     gpt-5.6-sol           400K     128K     yes       yes
zai        glm-5.2               1M       128K     yes       yes
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "zai/glm-5.2", Label: "glm-5.2", Provider: "zai"},
		{ID: "openai/gpt-5.6-sol", Label: "gpt-5.6-sol", Provider: "openai"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestBaseDynamicCatalogsContainNoOpenAgentsOwnedModelIDs(t *testing.T) {
	for _, agentID := range []string{"opencode"} {
		t.Run(agentID, func(t *testing.T) {
			got := Base(agentID)
			if got.SelectionMode != ports.ModelSelectionCatalog || !got.AllowCustom || got.Source != "cli" {
				t.Fatalf("Base(%q) = %#v", agentID, got)
			}
			if len(got.Models) != 0 {
				t.Fatalf("Base(%q) models = %#v, want no Open Agents-owned model IDs", agentID, got.Models)
			}
		})
	}
}

func TestClineDiscoveryUsesACPModelOptions(t *testing.T) {
	discoverer := Discoverer{ClineOptions: func(context.Context, ports.AgentModelDiscoveryRequest) ([]ports.ChatConfigOption, error) {
		return []ports.ChatConfigOption{
			{
				ID: "model", Name: "Model", Category: "model", Type: ports.ChatConfigOptionSelect,
				Current: ports.ChatConfigOptionValue{Select: "zai/glm-4.6"},
				Choices: []ports.ChatConfigOptionChoice{
					{Value: "zai/glm-4.6", Name: "GLM 4.6", Group: "zai", GroupName: "Z.ai"},
					{Value: "openai/gpt-5.4", Name: "GPT-5.4", Group: "openai", GroupName: "OpenAI"},
				},
			},
			{ID: "mode", Name: "Mode", Category: "mode", Type: ports.ChatConfigOptionSelect},
		}, nil
	}}
	got, err := discoverer.Discover(context.Background(), ports.AgentModelDiscoveryRequest{AgentID: "cline", Binary: "/bin/cline"})
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "zai/glm-4.6", Label: "GLM 4.6", Provider: "zai", IsDefault: true},
		{ID: "openai/gpt-5.4", Label: "GPT-5.4", Provider: "openai"},
	}
	if !reflect.DeepEqual(got.Models, want) || got.Source != "acp" {
		t.Fatalf("catalog = %#v, want models %#v from ACP", got, want)
	}
}

func TestParseIDLinesAcceptsOnlyWholeModelIDs(t *testing.T) {
	got, err := parseIDLines([]byte("\x1b[32mModels\x1b[0m\nzai/glm-4.6\nopenai/gpt-5.4\nTip: use --model <id>\nopenai/gpt-5.4 duplicate\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "openai/gpt-5.4" || got[1].ID != "zai/glm-4.6" {
		t.Fatalf("models = %#v", got)
	}
}

func TestParseAgyModelsUsesFirstColumnAsModelID(t *testing.T) {
	got, err := parseAgyModels([]byte(`gemini-3.7-flash-high  Gemini 3.7 Flash (High)
kimi-for-coding  Kimi For Coding
gpt-oss-120b-medium  GPT-OSS 120B (Medium)
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "gemini-3.7-flash-high", Label: "Gemini 3.7 Flash (High)"},
		{ID: "gpt-oss-120b-medium", Label: "GPT-OSS 120B (Medium)"},
		{ID: "kimi-for-coding", Label: "Kimi For Coding"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseGrokModelsIgnoresAuthAndDefaultStatus(t *testing.T) {
	got, err := parseGrokModels([]byte(`You are not authenticated.

Default model: grok-4.5

Available models:
  * grok-4.5 (default)
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "grok-4.5" || !got[0].IsDefault {
		t.Fatalf("models = %#v", got)
	}
}

func TestParseCursorModelsStopsBeforeTip(t *testing.T) {
	got, err := parseCursorModels([]byte(`Available models

auto - Auto (default)
gpt-5.6-sol-high - GPT-5.6 Sol 1M High

Tip: use --model <id> to switch.
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "auto" || got[0].Label != "Auto" || !got[0].IsDefault {
		t.Fatalf("models = %#v", got)
	}
	if got[1].ID != "gpt-5.6-sol-high" || got[1].Label != "GPT-5.6 Sol 1M High" {
		t.Fatalf("models = %#v", got)
	}
}

func TestKimchiDiscoveryUsesListModelsFlag(t *testing.T) {
	spec := commandSpecs["kimchi"]
	if len(spec.args) != 1 || spec.args[0] != "--list-models" {
		t.Fatalf("kimchi discovery args = %q, want [--list-models]", spec.args)
	}
	if spec.parser == nil {
		t.Fatalf("kimchi parser is nil")
	}
}

func TestParseKimchiModelsBuildsProviderQualifiedIDs(t *testing.T) {
	got, err := parsePiModels([]byte(`provider              model                 context  max-out  thinking  images
kimchi-dev            deepseek-v4-flash     1.0M     1.0M     yes       no
kimchi-dev            glm-5.2-fp8           1.0M     1.0M     yes       no
kimchi-dev/openai     gpt-5.5               272K     128K     yes       yes
kimchi-dev/zai        glm-5.3               1M       128K     yes       yes
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("models = %#v, want 4", got)
	}
	want := map[string]bool{
		"kimchi-dev/deepseek-v4-flash": true,
		"kimchi-dev/glm-5.2-fp8":       true,
		"kimchi-dev/openai/gpt-5.5":    true,
		"kimchi-dev/zai/glm-5.3":       true,
	}
	for _, m := range got {
		delete(want, m.ID)
		if m.Provider == "" {
			t.Fatalf("model %q has empty Provider", m.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("models = %#v, missing %#v", got, want)
	}
}

func TestParsePiModelsBuildsProviderQualifiedIDs(t *testing.T) {
	got, err := parsePiModels([]byte(`provider   model                       context  max-out  thinking  images
zai        glm-4.6                     1M       128K     yes       yes
openai     gpt-5.5                     272K     128K     yes       yes
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "zai/glm-4.6" || got[1].ID != "openai/gpt-5.5" {
		t.Fatalf("models = %#v", got)
	}
}

func TestParseJSONModelsFindsNestedModels(t *testing.T) {
	got, err := parseJSONModels([]byte(`{"providers":[{"id":"openai","models":[{"modelId":"gpt-5.4","displayName":"GPT-5.4","isDefault":true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("models = %#v", got)
	}
	var found bool
	for _, model := range got {
		if model.ID == "gpt-5.4" && model.Label == "GPT-5.4" && model.IsDefault {
			found = true
		}
	}
	if !found {
		t.Fatalf("models = %#v, want nested gpt-5.4", got)
	}
}

func TestParseOMPModelsUsesSelectorAsLaunchID(t *testing.T) {
	got, err := parseJSONModels([]byte(`{"models":[{"provider":"openai","id":"gpt-5.5","selector":"openai/gpt-5.5","name":"GPT-5.5"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{{ID: "openai/gpt-5.5", Label: "GPT-5.5", Provider: "openai"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseCopilotConfigModels(t *testing.T) {
	got, err := parseCopilotConfigModels([]byte("`model`: AI model to use.\n  - \"glm-5.2\"\n  - \"gpt-5.6-sol\"\n`contextTier`: context tier.\n  - ignored\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{{ID: "glm-5.2", Label: "glm-5.2"}, {ID: "gpt-5.6-sol", Label: "gpt-5.6-sol"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseDroidHelpModels(t *testing.T) {
	got, err := parseDroidHelpModels([]byte("Available Models:\n  auto                    Auto Model\n  grok-4.5                Grok 4.5 (default)\n  gpt-5.6-sol             GPT-5.6 Sol\n\nTool Controls:\n  --list-tools            List tools\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.AgentModelInfo{
		{ID: "grok-4.5", Label: "Grok 4.5", IsDefault: true},
		{ID: "auto", Label: "Auto Model"},
		{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseJSONModelsUsesModelMapKeysAsSelectableIDs(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": {
			"kimi-code/kimi-for-coding": {
				"provider": "managed:kimi-code",
				"model": "kimi-for-coding",
				"displayName": "K2.7 Coding"
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "kimi-code/kimi-for-coding" || got[0].Label != "K2.7 Coding" || got[0].Provider != "managed:kimi-code" {
		t.Fatalf("models = %#v, want provider-qualified Kimi config alias", got)
	}
}

func TestParseJSONModelsWalksGroupedModelsMaps(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": {
			"available": [{"modelId": "gpt-5.4", "displayName": "GPT-5.4"}]
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "gpt-5.4" || got[0].Label != "GPT-5.4" {
		t.Fatalf("models = %#v, want recursively discovered gpt-5.4", got)
	}
}

func TestParseJSONModelsWalksProviderGroupsWithNestedModels(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": {
			"openai": {
				"provider": "openai",
				"models": [{"modelId": "gpt-5.4", "displayName": "GPT-5.4"}]
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "gpt-5.4" || got[0].Label != "GPT-5.4" {
		t.Fatalf("models = %#v, want nested gpt-5.4 without provider-group alias", got)
	}
}

func TestParseJSONModelsSupportsKiroAndDevinFields(t *testing.T) {
	got, err := parseJSONModels([]byte(`{
		"models": [{"model_name": "Auto", "model_id": "auto"}],
		"families": [{
			"slug": "gpt-oss-120b",
			"family_label": "GPT-OSS 120B",
			"variants": [{"model_uid": "gpt-oss-120b-high", "label": "GPT-OSS 120B High"}]
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"auto":              true,
		"gpt-oss-120b":      true,
		"gpt-oss-120b-high": true,
	}
	for _, item := range got {
		delete(want, item.ID)
	}
	if len(want) != 0 {
		t.Fatalf("models = %#v, missing %#v", got, want)
	}
}

func TestCatalogFingerprintKeepsTheExecutableOnlyValueForConfiglessAgents(t *testing.T) {
	dir := t.TempDir()
	// opencode reads no configuration, so its fingerprint must stay byte-identical
	// to the executable fingerprint earlier daemons cached under.
	got := CatalogFingerprint(context.Background(), "opencode", "opencode", dir, nil)
	if want := BinaryVersion(context.Background(), "opencode"); got != want {
		t.Fatalf("fingerprint = %q, want the executable fingerprint %q", got, want)
	}
}
