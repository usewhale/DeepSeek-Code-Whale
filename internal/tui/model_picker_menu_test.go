package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
)

func TestPickerEventsClearBusyState(t *testing.T) {
	tests := []struct {
		name string
		ev   protocol.Event
		mode mode
	}{
		{
			name: "model picker",
			ev: protocol.Event{
				Kind:            protocol.EventModelSelectionRequested,
				ModelChoices:    []string{"deepseek-v4-pro"},
				EffortChoices:   []string{"normal"},
				ThinkingChoices: []string{"on", "off"},
				CurrentModel:    "deepseek-v4-pro",
				CurrentEffort:   "normal",
				CurrentThinking: "on",
			},
			mode: modeModelPicker,
		},
		{
			name: "permissions menu",
			ev: protocol.Event{
				Kind:       protocol.EventPermissionsSelectionRequested,
				AutoAccept: true,
			},
			mode: modePermissionsMenu,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := model{assembler: tuirender.NewAssembler(), mode: modeChat, busy: true, stopping: true}
			m.busySince = time.Now().Add(-5 * time.Minute)
			next, _ := m.Update(svcMsg(tt.ev))
			m = next.(model)
			if m.busy || m.stopping || !m.busySince.IsZero() {
				t.Fatalf("expected picker event to clear busy state, busy=%v stopping=%v busySince=%v", m.busy, m.stopping, m.busySince)
			}
			if m.mode != tt.mode {
				t.Fatalf("expected mode %v, got %v", tt.mode, m.mode)
			}
		})
	}
}
func TestPermissionsMenuRendersStateAndDispatchesExplicitMode(t *testing.T) {
	// Open with "ask" mode (default)
	m, intents := newModelWithDispatchSpy()
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventPermissionsSelectionRequested, AutoAccept: false, AutoReview: false}))
	m = next.(model)
	if m.mode != modePermissionsMenu {
		t.Fatalf("expected permissions menu mode, got %v", m.mode)
	}
	// Enter on current selection (ask) should not dispatch
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 0 || m.mode != modeChat {
		t.Fatalf("current ask selection should not dispatch, intents=%+v mode=%v", *intents, m.mode)
	}

	// Open with "ask" mode, down to auto-review (index 1), enter → dispatch auto_review
	m, intents = newModelWithDispatchSpy()
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventPermissionsSelectionRequested, AutoAccept: false, AutoReview: false}))
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.mode != modeChat {
		t.Fatalf("expected chat mode after selection, got %v", m.mode)
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSetAutoReview || (*intents)[0].AutoReview != true {
		t.Fatalf("expected auto_review intent, got intents=%+v", *intents)
	}

	// Open with auto-review mode (selected=1), down to auto-accept (index 2), enter → dispatch auto_accept
	m, intents = newModelWithDispatchSpy()
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventPermissionsSelectionRequested, AutoAccept: true, AutoReview: true}))
	m = next.(model)
	m.autoAccept = true
	m.autoReviewEnabled = true
	if m.permissionsMenu.selected != 1 {
		t.Fatalf("expected auto-review selected=1, got %d", m.permissionsMenu.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSetApprovalMode || (*intents)[0].ApprovalMode != "auto_accept" {
		t.Fatalf("expected auto_accept intent, got intents=%+v", *intents)
	}

	// Open with auto-review mode (selected=1), up to ask (index 0), enter → dispatch ask
	m, intents = newModelWithDispatchSpy()
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventPermissionsSelectionRequested, AutoAccept: true, AutoReview: true}))
	m = next.(model)
	m.autoAccept = true
	m.autoReviewEnabled = true
	if m.permissionsMenu.selected != 1 {
		t.Fatalf("expected auto-review selected=1, got %d", m.permissionsMenu.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(model)
	if m.permissionsMenu.selected != 0 {
		t.Fatalf("expected ask selected=0 after up, got %d", m.permissionsMenu.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSetApprovalMode || (*intents)[0].ApprovalMode != "ask" {
		t.Fatalf("expected ask intent, got intents=%+v autoAccept=%v autoReview=%v selected=%d", *intents, m.autoAccept, m.autoReviewEnabled, m.permissionsMenu.selected)
	}

	// Cancel at index 3 should not dispatch
	m, intents = newModelWithDispatchSpy()
	next, _ = m.Update(svcMsg(protocol.Event{Kind: protocol.EventPermissionsSelectionRequested, AutoAccept: false, AutoReview: false}))
	m = next.(model)
	// down x3 to cancel (index 3)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 0 || m.mode != modeChat {
		t.Fatalf("cancel should not dispatch, intents=%+v mode=%v", *intents, m.mode)
	}
}
func TestPermissionsMenuUsesSegmentedPickerStyles(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := newModel(nil, "", "", "")
	m.mode = modePermissionsMenu
	m.autoAccept = true
	m.autoReviewEnabled = false
	m.permissionsMenu.selected = 2
	rendered := m.renderPermissionsMenu()
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected styled permissions menu, got:\n%s", rendered)
	}
	plain := xansi.Strip(rendered)
	for _, want := range []string{"Permissions", "Ask for approval", "Auto-review", "> Auto-accept edits (current)", "cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected plain menu to contain %q, got:\n%s", want, plain)
		}
	}
}
func TestModelPickerUsesSegmentedPickerStyles(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := newModel(nil, "deepseek-chat", "medium", "auto")
	m.mode = modeModelPicker
	m.modelPicker.models = []string{"deepseek-chat", "deepseek-reasoner"}
	m.modelPicker.efforts = []string{"low", "medium", "high"}
	assertStyledPickerContains(t, m.renderModelPicker(), "Select Model and Effort", "Model:", "> deepseek-chat", "  deepseek-reasoner", "(up/down choose, enter next/confirm, esc back)")
}
func TestSessionPickerEnterDispatchesSelectedSession(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind: protocol.EventSessionsListed,
		Choices: []string{
			"recent sessions:",
			"   #   Updated   Branch                    Conversation",
			"   1) 1m ago    main                     first",
			"   2) 2m ago    feature                  second",
		},
	}))
	m = next.(model)
	if m.mode != modeSessionPicker {
		t.Fatalf("expected session picker mode, got %v", m.mode)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if len(*intents) != 1 {
		t.Fatalf("expected one intent, got %+v", *intents)
	}
	got := (*intents)[0]
	if got.Kind != protocol.IntentSelectSession || got.SessionInput != "2" {
		t.Fatalf("unexpected intent: %+v", got)
	}
	if m.mode != modeSessionPicker {
		t.Fatalf("expected session picker mode until async result, got %v", m.mode)
	}
}

func TestSessionPickerUsesSegmentedPickerStyles(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := newModel(nil, "", "", "")
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind: protocol.EventSessionsListed,
		Choices: []string{
			"recent sessions:",
			"   #   Updated   Branch                    Conversation",
			"*  1) 4s ago    -                        current",
			"   2) 1m ago    feature                  second session",
		},
	}))
	m = next.(model)
	assertStyledPickerContains(t, m.renderSessionPicker(), "sessions", "#   Updated   Branch", "> 1)   4s ago", "  2)   1m ago", "feature", "second session")
}
func TestSecondaryPickersUseSegmentedPickerStyles(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := newModel(nil, "", "", "")
	m.files.matches = []fileSuggestion{{Path: "internal/tui/render.go"}, {Path: "internal/tui", IsDir: true}}
	m.files.selected = 1
	assertStyledPickerContains(t, m.renderFileSuggestions(), "Files", "> internal/tui/", "dir", "Tab/Enter insert")

	m = newModel(nil, "", "", "")
	m.palette.actions = []paletteAction{{Label: "Open logs"}, {Label: "Show help"}}
	m.palette.selected = 1
	assertStyledPickerContains(t, m.renderPalette(), "Command Palette", "(enter to run, esc to close)", "> Show help")

	m = newModel(nil, "", "", "")
	m.skills.matches = []skillSuggestion{{Name: "code-review", Description: "Review local changes"}, {Name: "plan", Description: "Make a plan"}}
	m.skills.selected = 0
	assertStyledPickerContains(t, m.renderSkillSuggestions(), "Skills", "> $code-review", "Review local changes", "Tab/Enter insert")

	m = newModel(nil, "", "", "")
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventSkillsSelectionRequested})
	assertStyledPickerContains(t, m.renderSkillsMenu(), "Skills", "Choose an action", "> List skills", "Enable/Disable Skills")

	m = newModel(nil, "", "", "")
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventSkillsManagerUpdated,
		Skills: []protocol.SkillView{
			{Name: "code-review", Description: "Review local changes", Status: string(protocol.SkillAvailabilityReady)},
		},
	})
	assertStyledPickerContains(t, m.renderSkillsManager(), "Enable/Disable Skills", "[x] code-review", "Review local changes")

	m = newModel(nil, "", "", "")
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventPluginsManagerUpdated,
		Open: true,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "memory", Name: "Memory", Description: "Durable memory"},
			Enabled:  true,
		}},
	})
	assertStyledPickerContains(t, m.renderPluginsManager(), "Plugins", "Installed plugins", "[x] memory", "Durable memory")

	m = newModel(nil, "", "", "")
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	assertStyledPickerContains(t, m.renderReviewMenu(), "Review", "Choose what to review", "> Local changes", "Branch")

	m.reviewTargetPicker.branches = []reviewBranchItem{{Name: "main"}, {Name: "feature", Current: true}}
	m.reviewTargetPicker.defaultBranch = "main"
	m.mode = modeReviewBranchPicker
	assertStyledPickerContains(t, m.renderReviewTargetPicker(), "Choose base branch", "Type to search branches", "> feature -> main")

	m = newModel(nil, "", "", "")
	m.planImplementation.index = 1
	assertStyledPickerContains(t, m.renderPlanImplementationPicker(), "Implement this plan?", "> No, stay in Plan mode")

	m = newModel(nil, "", "", "")
	m.userInput.questions = []protocol.UserInputQuestion{{
		Question: "Pick deployment target",
		Options:  []protocol.UserInputOption{{Label: "Staging", Description: "Use staging"}, {Label: "Production", Description: "Use production"}},
	}}
	m.userInput.selectedOption = 1
	assertStyledPickerContains(t, m.renderUserInputPicker(), "Pick deployment target", "> Production", "- Use production")

	m = newModel(nil, "", "", "")
	m.worktreeExit.summary = protocol.WorktreeExitSummary{
		Session: protocol.WorktreeSession{Name: "feat", Branch: "feature/work", Path: "/tmp/work"},
	}
	assertStyledPickerContains(t, m.renderWorktreeExit(), "Exiting worktree session", "worktree: feat", "> Keep worktree", "No worktree changes were detected.")
}
func TestSkillsManagerRendersSearchesAndToggles(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventSkillsManagerUpdated,
		Skills: []protocol.SkillView{
			{Name: "code-review", Description: "Review local changes", Status: string(protocol.SkillAvailabilityReady)},
			{Name: "legacy-review", Reason: "Disabled in config", Status: string(protocol.SkillAvailabilityDisabled)},
		},
	})
	if m.mode != modeSkillsManager {
		t.Fatalf("expected skills manager mode, got %v", m.mode)
	}
	rendered := m.renderSkillsManager()
	for _, want := range []string{"Enable/Disable Skills", "[x] code-review", "[ ] legacy-review", "Space/Enter toggle"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected skills manager render to contain %q, got:\n%s", want, rendered)
		}
	}

	for _, r := range "legacy" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	if len(m.skillsManager.matches) != 1 {
		t.Fatalf("expected one filtered skill, got matches=%v", m.skillsManager.matches)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one toggle intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSetSkillEnabled || got.SkillName != "legacy-review" || !got.SkillEnabled {
		t.Fatalf("unexpected toggle intent: %+v", got)
	}
	idx := m.skillsManager.matches[m.skillsManager.selected]
	if !m.skillsManager.all[idx].Enabled {
		t.Fatalf("expected selected skill to be optimistically enabled: %+v", m.skillsManager.all[idx])
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(model)
	if m.mode != modeChat {
		t.Fatalf("expected ctrl+c to close skills manager, got mode %v", m.mode)
	}
}
func TestSkillsMenuListsAndOpensManager(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventSkillsSelectionRequested})
	if m.mode != modeSkillsMenu {
		t.Fatalf("expected skills menu mode, got %v", m.mode)
	}
	rendered := m.renderSkillsMenu()
	for _, want := range []string{"Skills", "List skills", "Enable/Disable Skills", "press $"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected skills menu render to contain %q, got:\n%s", want, rendered)
		}
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one manager request intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentRequestSkillsManage {
		t.Fatalf("unexpected intent: %+v", got)
	}
}
func TestSkillsMenuListActionOpensDollarPicker(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.skills.all = []skillSuggestion{{Name: "code-review", Description: "Review local changes"}}
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventSkillsSelectionRequested})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.mode != modeChat {
		t.Fatalf("expected chat mode after list action, got %v", m.mode)
	}
	if got := m.input.Value(); got != "$" {
		t.Fatalf("expected input to contain dollar picker trigger, got %q", got)
	}
	if len(m.skills.matches) != 1 || m.skills.matches[0].Name != "code-review" {
		t.Fatalf("expected skill picker matches, got %+v", m.skills.matches)
	}
}
func TestPluginsManagerRendersAndToggles(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventPluginsManagerUpdated,
		Open: true,
		Plugins: []protocol.PluginStatus{
			{
				Manifest: protocol.PluginManifest{ID: "memory", Name: "Memory", Description: "Durable memory"},
				Enabled:  true,
				Tools:    []string{"forget", "recall_memory", "remember"},
				Commands: []protocol.PluginCommand{{Name: "/memory"}},
				Agents:   []string{"memory:curator"},
				Rules:    []string{"memory:style"},
				Services: []protocol.PluginService{{Name: "mcp:memory", Status: "configured"}},
			},
		},
	})
	if m.mode != modePluginsManager {
		t.Fatalf("expected plugins manager mode, got %v", m.mode)
	}
	rendered := m.renderPluginsManager()
	for _, want := range []string{"Plugins", "Installed plugins", "[x] memory", "Run /memory", "Agent tools: forget", "Subagents: memory:curator", "Rules: 1", "Services: mcp:memory", "Enter details", "Space enable/disable"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected plugins manager render to contain %q, got:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{"Type to search plugins", "skills-improver"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("plugins manager should not render search UI %q:\n%s", unwanted, rendered)
		}
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one toggle intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSetPluginEnabled || got.PluginID != "memory" || got.PluginEnabled {
		t.Fatalf("unexpected toggle intent: %+v", got)
	}
	idx := m.pluginsManager.matches[m.pluginsManager.selected]
	if m.pluginsManager.all[idx].Enabled {
		t.Fatalf("expected selected plugin to be optimistically disabled: %+v", m.pluginsManager.all[idx])
	}
	if _, ok := m.slashSpec("/memory"); ok {
		t.Fatalf("disabled plugin command should be removed optimistically")
	}
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventPluginsManagerUpdated,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "memory", Name: "Memory", Description: "Durable memory"},
			Enabled:  false,
			Commands: []protocol.PluginCommand{{Name: "/memory"}},
		}},
	})
	if m.mode != modePluginsManager {
		t.Fatalf("plugin refresh while open should keep manager open, got mode %v", m.mode)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	m = next.(model)
	if len(*intents) != 2 {
		t.Fatalf("expected rune-space to toggle too, got intents %+v", *intents)
	}
	if got := (*intents)[1]; got.Kind != protocol.IntentSetPluginEnabled || got.PluginID != "memory" || !got.PluginEnabled {
		t.Fatalf("unexpected rune-space toggle intent: %+v", got)
	}
	if _, ok := m.slashSpec("/memory"); !ok {
		t.Fatalf("enabled plugin command should be restored optimistically")
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.mode != modeChat {
		t.Fatalf("expected esc to close plugins manager, got mode %v", m.mode)
	}
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventPluginsManagerUpdated,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "memory", Name: "Memory", Description: "Durable memory"},
			Enabled:  false,
			Commands: []protocol.PluginCommand{{Name: "/memory"}},
		}},
	})
	if m.mode != modeChat {
		t.Fatalf("background plugin refresh should not reopen manager, got mode %v", m.mode)
	}
}

func TestConfigManagerSearchesTogglesAndApplies(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventConfigManagerUpdated,
		Open: true,
		Config: &protocol.ConfigManagerState{Items: []protocol.ConfigSettingView{
			{ID: "workflows.enabled", Label: "Dynamic workflows", Description: "Enable workflow runtime", Type: "bool", Value: "true", Scope: "project local", Source: "default"},
			{ID: "workflows.keyword_trigger_enabled", Label: "Workflow keyword trigger", Description: "Let catalog hints encourage workflow use", Type: "bool", Value: "true", Scope: "project local", Source: "default"},
		}},
	})
	if m.mode != modeConfigManager {
		t.Fatalf("expected config manager mode, got %v", m.mode)
	}
	rendered := xansi.Strip(m.renderConfigManager())
	for _, want := range []string{"Config", "Search settings", "[x] Dynamic workflows", "workflows.keyword_trigger_enabled"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected config render to contain %q, got:\n%s", want, rendered)
		}
	}

	for _, r := range "catalog" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	if m.configManager.query != "catalog" {
		t.Fatalf("expected query with plain a to be searchable, got %q", m.configManager.query)
	}
	if len(m.configManager.matches) != 1 {
		t.Fatalf("expected one filtered config setting, got matches=%v", m.configManager.matches)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(model)
	if got := m.configManager.pending["workflows.keyword_trigger_enabled"]; got != "false" {
		t.Fatalf("expected pending false value, got %q", got)
	}
	if !strings.Contains(xansi.Strip(m.renderConfigManager()), "1 pending") {
		t.Fatalf("expected pending marker in render:\n%s", m.renderConfigManager())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("plain a should extend search, not apply pending config changes: %+v", *intents)
	}
	if m.configManager.query != "cataloga" {
		t.Fatalf("expected plain a to extend query, got %q", m.configManager.query)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one config apply intent, got %+v", *intents)
	}
	got := (*intents)[0]
	if got.Kind != protocol.IntentApplyConfigSettings || len(got.ConfigUpdates) != 1 ||
		got.ConfigUpdates[0].ID != "workflows.keyword_trigger_enabled" || got.ConfigUpdates[0].Value != "false" {
		t.Fatalf("unexpected config apply intent: %+v", got)
	}
}

func TestConfigManagerEscDiscardsPendingChanges(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventConfigManagerUpdated,
		Open: true,
		Config: &protocol.ConfigManagerState{Items: []protocol.ConfigSettingView{
			{ID: "workflows.enabled", Label: "Dynamic workflows", Type: "bool", Value: "true"},
		}},
	})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(model)
	if len(m.configManager.pending) != 1 {
		t.Fatalf("expected pending change")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.mode != modeChat {
		t.Fatalf("expected config manager to close, mode=%v", m.mode)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no apply intent on esc, got %+v", *intents)
	}
}

func TestConfigManagerShowsApplyResultInline(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventConfigManagerUpdated,
		Open: true,
		Config: &protocol.ConfigManagerState{Items: []protocol.ConfigSettingView{
			{ID: "workflows.enabled", Label: "Dynamic workflows", Type: "bool", Value: "false", Scope: "project local", Source: "project local"},
		}},
	})
	before := len(m.transcript)
	m, _ = updateTestModel(t, m, svcMsg(protocol.Event{
		Kind:   protocol.EventLocalSubmitResult,
		Status: "ok",
		Text:   "updated 1 config setting(s): Dynamic workflows\nconfig: /workspace/.whale/config.local.toml",
	}))
	if len(m.transcript) != before {
		t.Fatalf("config apply result should stay inline, before=%d after=%d", before, len(m.transcript))
	}
	rendered := xansi.Strip(m.renderConfigManager())
	if !strings.Contains(rendered, "saved: updated 1 config setting(s): Dynamic workflows") {
		t.Fatalf("expected inline saved status, got:\n%s", rendered)
	}
}

func TestPluginsManagerDetailView(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.width = 96
	m.height = 40
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventPluginsManagerUpdated,
		Open: true,
		Plugins: []protocol.PluginStatus{
			{
				Manifest:    protocol.PluginManifest{ID: "demo-plugin", Name: "Demo Plugin", Version: "0.3.0", Description: "Demo plugin"},
				Enabled:     true,
				Commands:    []protocol.PluginCommand{{Name: "/demo-plugin:ask", Usage: "<topic>", Description: "Ask through plugin"}},
				Tools:       []string{"demo_tool"},
				Skills:      []string{"demo-skill"},
				Agents:      []string{"demo-plugin:reviewer"},
				Rules:       []string{"demo-plugin:style"},
				Hooks:       []string{"Plugin startup marker"},
				Services:    []protocol.PluginService{{Name: "mcp:search", Status: "configured"}},
				Diagnostics: []protocol.PluginDiagnostic{{Level: "ok", Label: "components", Detail: "loaded"}},
				Paths:       map[string]string{"cache": "/tmp/demo-cache"},
			},
		},
	})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if !m.pluginsManager.detail {
		t.Fatal("expected plugin detail view")
	}
	plain := xansi.Strip(m.renderPluginsManager())
	for _, want := range []string{
		"Plugin details",
		"demo-plugin · enabled",
		"version: 0.3.0",
		"Commands",
		"/demo-plugin:ask <topic> - Ask through plugin",
		"Tools",
		"demo_tool",
		"Agents",
		"demo-plugin:reviewer",
		"Rules",
		"demo-plugin:style",
		"Services",
		"mcp:search · configured",
		"Diagnostics",
		"ok components: loaded",
		"Paths",
		"cache: /tmp/demo-cache",
		"Esc back",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected plugin detail to contain %q, got:\n%s", want, plain)
		}
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("detail view should ignore Space, got intents %+v", *intents)
	}
	plain = xansi.Strip(m.renderPluginsManager())
	if strings.Contains(plain, "Space enable/disable") {
		t.Fatalf("detail footer should not advertise toggling, got:\n%s", plain)
	}
	if !strings.Contains(plain, "demo-plugin · enabled") {
		t.Fatalf("detail view should remain read-only after Space, got:\n%s", plain)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.mode != modePluginsManager || m.pluginsManager.detail {
		t.Fatalf("expected esc from detail to return to list, mode=%v detail=%v", m.mode, m.pluginsManager.detail)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.mode != modeChat {
		t.Fatalf("expected second esc to close manager, got %v", m.mode)
	}
}
func TestReviewMenuDispatchesAndPrefillsTargets(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	if m.mode != modeReviewMenu {
		t.Fatalf("expected review menu mode, got %v", m.mode)
	}
	rendered := m.renderReviewMenu()
	for _, want := range []string{"Review", "Local changes", "Branch", "vs default branch", "Pull request", "Custom instructions"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected review menu render to contain %q, got:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Current branch") {
		t.Fatalf("review menu should not contain duplicate Current branch entry:\n%s", rendered)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "/review local" {
		t.Fatalf("expected /review local submit intent, got %+v", *intents)
	}
	if m.mode != modeChat {
		t.Fatalf("expected review menu to close, got mode %v", m.mode)
	}
	if !m.busy || m.status != "running" {
		t.Fatalf("expected review action to enter running state, busy=%v status=%q", m.busy, m.status)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected review action to clear input, got %q", got)
	}

	m, _ = newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.mode != modeReviewBranchPicker || !m.reviewTargetPicker.loading {
		t.Fatalf("expected branch picker loading mode, mode=%v picker=%+v", m.mode, m.reviewTargetPicker)
	}
}
func TestReviewTargetPickersSubmitSelectedTargets(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	m.reviewMenu.selected = 2
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.mode != modeReviewPRPicker {
		t.Fatalf("expected PR picker, got %v", m.mode)
	}
	m, _ = updateTestModel(t, m, reviewPRsLoadedMsg{items: []reviewPRItem{
		{Number: 102, Title: "Improve review command", Head: "feat/review", Author: "alice"},
	}})
	rendered := m.renderReviewTargetPicker()
	if !strings.Contains(rendered, "#102 Improve review command") || !strings.Contains(rendered, "Type number or URL manually") {
		t.Fatalf("unexpected PR picker render:\n%s", rendered)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "/review pr 102" {
		t.Fatalf("expected selected PR submit intent, got %+v", *intents)
	}

	m, intents = newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	m.reviewMenu.selected = 3
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.mode != modeReviewCommitPicker {
		t.Fatalf("expected commit picker, got %v", m.mode)
	}
	m, _ = updateTestModel(t, m, reviewCommitsLoadedMsg{items: []reviewCommitItem{
		{SHA: "abc1234", Subject: "fix review picker", Author: "g", When: "2 minutes ago"},
	}})
	rendered = m.renderReviewTargetPicker()
	if !strings.Contains(rendered, "abc1234 fix review picker") || !strings.Contains(rendered, "Type SHA manually") {
		t.Fatalf("unexpected commit picker render:\n%s", rendered)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "/review commit abc1234" {
		t.Fatalf("expected selected commit submit intent, got %+v", *intents)
	}
	if m.mode != modeChat {
		t.Fatalf("expected chat mode after commit submit, got %v", m.mode)
	}

	m, intents = newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	m.reviewMenu.selected = 1
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.mode != modeReviewBranchPicker {
		t.Fatalf("expected branch picker, got %v", m.mode)
	}
	m, _ = updateTestModel(t, m, reviewBranchesLoadedMsg{items: []reviewBranchItem{
		{Name: "feature/review", Current: true},
		{Name: "diagnose/input-scroll"},
	}, defaultBranch: "origin/main"})
	rendered = m.renderReviewTargetPicker()
	if !strings.Contains(rendered, "Type to search branches") ||
		!strings.Contains(rendered, "feature/review -> origin/main") ||
		!strings.Contains(rendered, "feature/review -> diagnose/input-scroll") ||
		strings.Contains(rendered, "feature/review -> feature/review") ||
		!strings.Contains(rendered, "Type branch manually") {
		t.Fatalf("unexpected branch picker render:\n%s", rendered)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "/review branch origin/main" {
		t.Fatalf("expected selected branch submit intent, got %+v", *intents)
	}
}
func TestReviewBranchPickerFiltersBranches(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	m.reviewMenu.selected = 1
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	m, _ = updateTestModel(t, m, reviewBranchesLoadedMsg{items: []reviewBranchItem{
		{Name: "main"},
		{Name: "feat/btw-command", Current: true},
		{Name: "diagnose/input-scroll-garbled"},
		{Name: "design/plugin-memory"},
	}})
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("diag")})
	m = next.(model)
	rendered := m.renderReviewTargetPicker()
	if !strings.Contains(rendered, "Type to search branches: diag") ||
		!strings.Contains(rendered, "feat/btw-command -> diagnose/input-scroll-garbled") ||
		strings.Contains(rendered, "feat/btw-command -> design/plugin-memory") ||
		strings.Contains(rendered, "feat/btw-command -> feat/btw-command") {
		t.Fatalf("unexpected filtered branch picker render:\n%s", rendered)
	}
}
func TestReviewBranchPickerDefaultFirstAndLimitsVisibleRows(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	m.reviewMenu.selected = 1
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	m, _ = updateTestModel(t, m, reviewBranchesLoadedMsg{items: []reviewBranchItem{
		{Name: "topic", Current: true},
		{Name: "z-last"},
		{Name: "main"},
		{Name: "branch-1"},
		{Name: "branch-2"},
		{Name: "branch-3"},
		{Name: "branch-4"},
		{Name: "branch-5"},
		{Name: "branch-6"},
	}, defaultBranch: "origin/main"})

	branches := m.filteredReviewBranches()
	if len(branches) == 0 || branches[0].Name != "origin/main" {
		t.Fatalf("expected default branch first, got %+v", branches)
	}
	rendered := m.renderReviewTargetPicker()
	if !strings.Contains(rendered, "topic -> origin/main") || strings.Contains(rendered, "topic -> branch-6") || strings.Contains(rendered, "Type branch manually") {
		t.Fatalf("expected first page of 6 branch rows, got:\n%s", rendered)
	}

	for i := 0; i < 7; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(model)
	}
	rendered = m.renderReviewTargetPicker()
	if !strings.Contains(rendered, "topic -> branch-5") {
		t.Fatalf("expected down arrow to scroll branch rows, got:\n%s", rendered)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "/review branch branch-5" {
		t.Fatalf("expected selected branch submit intent, got %+v", *intents)
	}
}
func TestParseReviewBranchesParsesTabSeparator(t *testing.T) {
	items := parseReviewBranches("main\t\nfeature\t*\n")
	if len(items) != 2 || items[0].Name != "main" || items[0].Current || items[1].Name != "feature" || !items[1].Current {
		t.Fatalf("unexpected parsed branches: %+v", items)
	}
}
func TestReviewTargetPickerManualInputAndEsc(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	m.reviewMenu.selected = 3
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	m, _ = updateTestModel(t, m, reviewCommitsLoadedMsg{})
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = next.(model)
	if m.mode != modeChat || m.input.Value() != "/review commit a" {
		t.Fatalf("expected manual commit prefill, mode=%v input=%q", m.mode, m.input.Value())
	}

	m, _ = newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	m.reviewMenu.selected = 1
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	m, _ = updateTestModel(t, m, reviewPRsLoadedMsg{})
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.mode != modeReviewMenu || m.status != "review" {
		t.Fatalf("expected esc to return to review menu, mode=%v status=%q", m.mode, m.status)
	}
}
func TestReviewMenuEscCloses(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{Kind: protocol.EventReviewRequested})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.mode != modeChat || m.status != "ready" {
		t.Fatalf("expected review menu to close, mode=%v status=%q", m.mode, m.status)
	}
}
func TestPickerAndModalViewsHideComposer(t *testing.T) {
	const draft = "composer draft should stay hidden"
	base := func() model {
		m := newModel(nil, "deepseek-chat", "medium", "on")
		m.width = 100
		m.height = 30
		m.input.SetValue(draft)
		return m
	}

	chat := base()
	chat.mode = modeChat
	if view := chat.renderBottom(100); !strings.Contains(view, draft) {
		t.Fatalf("chat mode should render composer draft:\n%s", view)
	}

	cases := []struct {
		name  string
		setup func(*model)
		want  string
	}{
		{
			name: "review menu",
			setup: func(m *model) {
				m.mode = modeReviewMenu
			},
			want: "Choose what to review",
		},
		{
			name: "review target picker",
			setup: func(m *model) {
				m.mode = modeReviewBranchPicker
				m.reviewTargetPicker.branches = []reviewBranchItem{{Name: "main"}}
			},
			want: "Type to search branches",
		},
		{
			name: "review commit picker",
			setup: func(m *model) {
				m.mode = modeReviewCommitPicker
				m.reviewTargetPicker.commits = []reviewCommitItem{{SHA: "abc1234", Subject: "fix picker"}}
			},
			want: "Choose commit",
		},
		{
			name: "review pr picker",
			setup: func(m *model) {
				m.mode = modeReviewPRPicker
				m.reviewTargetPicker.prs = []reviewPRItem{{Number: 12, Title: "Fix picker"}}
			},
			want: "Choose pull request",
		},
		{
			name: "approval",
			setup: func(m *model) {
				m.mode = modeApproval
				m.approval.toolCallID = "tool-1"
				m.approval.toolName = "shell_run"
				m.approval.reason = "ls"
			},
			want: "Approval required",
		},
		{
			name: "user input",
			setup: func(m *model) {
				m.mode = modeUserInput
				m.userInput.questions = []protocol.UserInputQuestion{{
					ID:       "continue",
					Question: "Continue?",
					Options:  []protocol.UserInputOption{{Label: "Yes", Description: "Continue now."}},
				}}
			},
			want: "Continue?",
		},
		{
			name: "model picker",
			setup: func(m *model) {
				m.mode = modeModelPicker
				m.modelPicker.models = []string{"deepseek-chat"}
				m.modelPicker.efforts = []string{"medium"}
				m.modelPicker.thinkings = []string{"on"}
			},
			want: "Select Model and Effort",
		},
		{
			name: "session picker",
			setup: func(m *model) {
				m.mode = modeSessionPicker
				m.sessionChoices = []string{"session-1"}
			},
			want: "sessions",
		},
		{
			name: "permissions menu",
			setup: func(m *model) {
				m.mode = modePermissionsMenu
				m.permissionsMenu.selected = 1
			},
			want: "Auto-review",
		},
		{
			name: "plan implementation picker",
			setup: func(m *model) {
				m.mode = modePlanImplementation
			},
			want: "Implement this plan?",
		},
		{
			name: "worktree exit",
			setup: func(m *model) {
				m.mode = modeWorktreeExit
				m.worktreeExit.summary = protocol.WorktreeExitSummary{
					Session:      protocol.WorktreeSession{Name: "feature", Path: "/tmp/repo/.whale/worktrees/feature", Branch: "worktree-feature"},
					ChangedFiles: 2,
					Commits:      1,
				}
			},
			want: "Exiting worktree session",
		},
		{
			name: "skills menu",
			setup: func(m *model) {
				m.mode = modeSkillsMenu
			},
			want: "Skills",
		},
		{
			name: "skills manager",
			setup: func(m *model) {
				m.mode = modeSkillsManager
				m.skillsManager.all = []skillManagerItem{{Name: "code-review", Enabled: true, Toggleable: true}}
				m.skillsManager.matches = []int{0}
			},
			want: "Enable/Disable Skills",
		},
		{
			name: "plugins manager",
			setup: func(m *model) {
				m.mode = modePluginsManager
				m.pluginsManager.all = []pluginManagerItem{{ID: "memory", Name: "Memory", Enabled: true}}
				m.pluginsManager.matches = []int{0}
			},
			want: "Plugins",
		},
		{
			name: "config manager",
			setup: func(m *model) {
				m.mode = modeConfigManager
				m.configManager.all = []protocol.ConfigSettingView{{ID: "workflows.enabled", Label: "Dynamic workflows", Type: "bool", Value: "true"}}
				m.configManager.matches = []int{0}
			},
			want: "Config",
		},
		{
			name: "help",
			setup: func(m *model) {
				m.mode = modeHelp
			},
			want: "Whale help",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.setup(&m)
			view := m.renderBottom(100)
			if strings.Contains(view, draft) || strings.Contains(view, "Type message or command") {
				t.Fatalf("composer should be hidden while %s is active:\n%s", tc.name, view)
			}
			if !strings.Contains(view, tc.want) {
				t.Fatalf("expected %s view to contain %q:\n%s", tc.name, tc.want, view)
			}
		})
	}
}
func TestRenderQueuedPromptsShowsPreviewLimit(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.queuedPrompts = []queuedPrompt{
		{Text: "first queued"},
		{Text: "second queued"},
		{Text: "third queued"},
		{Text: "fourth queued"},
	}

	view := m.renderQueuedPrompts(80)
	for _, want := range []string{"queued follow-up (4)", "first queued", "second queued", "third queued", "... 1 more"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected queued preview to contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "fourth queued") {
		t.Fatalf("expected queued preview to hide fourth prompt:\n%s", view)
	}
}
func TestFileDiffMetadataPreviewAllowsLargeTranslationDiff(t *testing.T) {
	metadata := largeTranslationDiffMetadata(190, 190)
	got := renderFileDiffMetadataPlain(metadata, fileDiffPreviewMaxLines)
	if !strings.Contains(got, "-中文 000") {
		t.Fatalf("expected diff preview to include deletions:\n%s", got)
	}
	if !strings.Contains(got, "+English 189") {
		t.Fatalf("expected 400-line diff preview to include additions:\n%s", got)
	}
	if strings.Contains(got, "... diff truncated (") {
		t.Fatalf("expected translation-size diff to fit in preview:\n%s", got)
	}
}
func TestPermissionsMenuNavigationWrapsAround(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventPermissionsSelectionRequested, AutoAccept: false, AutoReview: false}))
	m = next.(model)
	if m.mode != modePermissionsMenu || m.permissionsMenu.selected != 0 {
		t.Fatalf("expected permissions menu with ask selected, mode=%v selected=%d", m.mode, m.permissionsMenu.selected)
	}

	// Up from the first item (ask) wraps to the last (cancel).
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(model)
	if m.permissionsMenu.selected != 3 {
		t.Fatalf("expected up at ask to wrap to cancel, got %d", m.permissionsMenu.selected)
	}

	// Down from the last item wraps back to the first.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if m.permissionsMenu.selected != 0 {
		t.Fatalf("expected down at cancel to wrap to ask, got %d", m.permissionsMenu.selected)
	}
}
