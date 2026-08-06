package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	appcommands "github.com/usewhale/whale/internal/runtime/commands"
	"github.com/usewhale/whale/internal/runtime/protocol"
	tuirender "github.com/usewhale/whale/internal/tui/render"
)

func selectSlashCommand(t *testing.T, m *model, want string) {
	t.Helper()
	for i, cmd := range m.slash.matches {
		if cmd.InsertText == want || cmd.Display == want {
			m.slash.selected = i
			return
		}
	}
	t.Fatalf("slash command %q not found in matches %+v", want, m.slash.matches)
}
func TestSlashCommandsShowSupportedCommandsAndOmitRemovedCommands(t *testing.T) {
	cmds := parseSlashCommands(appcommands.CommandsHelp())
	if !containsString(cmds, "/permissions") {
		t.Fatalf("expected /permissions in slash commands: %+v", cmds)
	}
	if !containsString(cmds, "/agent") {
		t.Fatalf("expected /agent in slash commands: %+v", cmds)
	}
	if !containsString(cmds, "/plan") {
		t.Fatalf("expected /plan in slash commands: %+v", cmds)
	}
	if !containsString(cmds, "/ask") {
		t.Fatalf("expected /ask in slash commands: %+v", cmds)
	}
	if !containsString(cmds, "/diff") {
		t.Fatalf("expected /diff in slash commands: %+v", cmds)
	}
	if containsString(cmds, "/approval") {
		t.Fatalf("removed command /approval should not appear in slash commands: %+v", cmds)
	}
	if containsString(cmds, "/thinking") {
		t.Fatalf("removed command /thinking should not appear in slash commands: %+v", cmds)
	}
	if containsString(cmds, "/budget") {
		t.Fatalf("removed command /budget should not appear in slash commands: %+v", cmds)
	}
	if containsString(cmds, "/step") {
		t.Fatalf("removed command /step should not appear in slash commands: %+v", cmds)
	}
}
func TestSlashSuggestionsHiddenForMultilineInput(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/sta\nmore")
	m.updateSlashMatches()
	if len(m.slash.matches) != 0 {
		t.Fatalf("expected slash suggestions hidden for multiline input, got %+v", m.slash.matches)
	}
}
func TestSlashSuggestionsShownForSingleLineSlash(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/")
	m.updateSlashMatches()
	if len(m.slash.matches) == 0 {
		t.Fatal("expected slash suggestions for bare slash")
	}
}
func TestSlashSuggestionsRenderDescriptions(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/")
	m.updateSlashMatches()
	rendered := m.renderSlashSuggestions()
	for _, want := range []string{"/model", "Choose model, effort, and thinking settings"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected slash suggestions to contain %q:\n%s", want, rendered)
		}
	}
}

func TestSuggestionsRenderAboveComposerBoundary(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*model)
		panelText  string
		inputText  string
		insideText string
	}{
		{
			name: "slash",
			setup: func(m *model) {
				m.input.SetValue("/")
				m.updateSlashMatches()
			},
			panelText:  "/model",
			inputText:  "› /",
			insideText: "/model",
		},
		{
			name: "file",
			setup: func(m *model) {
				m.input.SetValue("@mod")
				m.files.matches = []fileSuggestion{{Path: "internal/tui/model.go"}}
				m.files.selected = 0
			},
			panelText:  "internal/tui/model.go",
			inputText:  "@mod",
			insideText: "internal/tui/model.go",
		},
		{
			name: "skill",
			setup: func(m *model) {
				m.input.SetValue("$co")
				m.skills.matches = []skillSuggestion{{Name: "code-review", Description: "Review local changes"}}
				m.skills.selected = 0
			},
			panelText:  "$code-review",
			inputText:  "› $co",
			insideText: "$code-review",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(nil, "deepseek-v4-pro", "normal", "on")
			m.width = 100
			m.height = 24
			m.cwd = "~/Engineer/ai/dsk/whale"
			tt.setup(&m)

			bottom := xansi.Strip(m.renderBottom(100))
			lines := strings.Split(strings.TrimRight(bottom, "\n"), "\n")
			suggestionIdx := firstLineContaining(lines, tt.panelText)
			boundaryIdx := firstFullWidthBoundaryLine(lines, 100)
			composerIdx := firstLineContaining(lines, tt.inputText)
			footerIdx := len(lines) - 1

			if suggestionIdx < 0 || boundaryIdx < 0 || composerIdx < 0 {
				t.Fatalf("expected %s suggestions, composer boundary, and composer in bottom layout:\n%s", tt.name, bottom)
			}
			if !(suggestionIdx < boundaryIdx && boundaryIdx < composerIdx && composerIdx < footerIdx) {
				t.Fatalf("expected %s suggestions above composer boundary and footer after composer, got suggestion=%d boundary=%d composer=%d footer=%d:\n%s",
					tt.name, suggestionIdx, boundaryIdx, composerIdx, footerIdx, bottom)
			}
			if strings.Contains(strings.Join(lines[boundaryIdx+1:footerIdx], "\n"), tt.insideText) {
				t.Fatalf("%s suggestions should not be wrapped inside composer boundaries:\n%s", tt.name, bottom)
			}
			if footer := lines[footerIdx]; !strings.Contains(footer, "deepseek-v4-pro . normal") ||
				!strings.Contains(footer, "thinking: on") ||
				!strings.Contains(footer, "whale") {
				t.Fatalf("expected footer status fields on last line, got %q in:\n%s", footer, bottom)
			}
		})
	}
}

func TestSlashArgumentHintShownForCommandSpace(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/model ")
	m.updateSlashMatches()
	if !m.hasSlashPanel() {
		t.Fatal("expected slash argument hint panel")
	}
	if len(m.slash.matches) != 0 {
		t.Fatalf("did not expect /model option matches, got %+v", m.slash.matches)
	}
	if rendered := m.renderSlashSuggestions(); !strings.Contains(xansi.Strip(rendered), "Arguments [model]") {
		t.Fatalf("expected /model argument hint, got:\n%s", rendered)
	}
}
func TestSlashArgumentHintEscClearsPanelWithoutMutatingInput(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/model ")
	m.updateSlashMatches()
	if !m.hasSlashPanel() {
		t.Fatal("expected slash argument hint panel")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if got := m.input.Value(); got != "/model " {
		t.Fatalf("expected esc to preserve input, got %q", got)
	}
	if m.hasSlashPanel() || m.slash.argumentHint != "" || len(m.slash.matches) != 0 {
		t.Fatalf("expected esc to clear hint-only slash panel, hint=%q matches=%+v", m.slash.argumentHint, m.slash.matches)
	}
}
func TestSlashOptionSuggestionsInsertSubcommand(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/stats ")
	m.updateSlashMatches()
	if len(m.slash.matches) == 0 {
		t.Fatal("expected /stats option suggestions")
	}
	selectSlashCommand(t, &m, "/stats usage")
	if rendered := m.renderSlashSuggestions(); !strings.Contains(xansi.Strip(rendered), "/stats usage") || !strings.Contains(xansi.Strip(rendered), "Show token and cost usage") {
		t.Fatalf("expected /stats option description, got:\n%s", rendered)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected /stats usage option to dispatch, got intents %+v", *intents)
	}
	if (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != "/stats usage" {
		t.Fatalf("unexpected dispatched intent: %+v", (*intents)[0])
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared after /stats usage option, got %q", got)
	}
}
func TestSlashStatsOptionSuggestionsUseFullCommandLabels(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })

	m := newModel(nil, "", "", "")
	m.input.SetValue("/stats ")
	m.updateSlashMatches()
	rendered := m.renderSlashSuggestions()
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected styled stats suggestions, got:\n%s", rendered)
	}
	plain := xansi.Strip(rendered)
	for _, want := range []string{"/stats usage", "/stats cache", "/stats tools", "Show token and cost usage", "Show cache diagnostics", "Show tool-call counts"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected stats suggestions to contain %q, got:\n%s", want, plain)
		}
	}
}
func TestSlashCommandWithOptionsDrillsDownWhenSelected(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/sta")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/stats ")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("selecting /stats should only show options, got intents %+v", *intents)
	}
	if got := m.input.Value(); got != "/stats " {
		t.Fatalf("expected /stats selection to add trailing space, got %q", got)
	}
	if len(m.slash.matches) == 0 {
		t.Fatal("expected /stats option suggestions after selection")
	}
	if selected, ok := m.selectedSlashSuggestion(); !ok || selected.InsertText != "/stats usage" {
		t.Fatalf("expected /stats usage to be selected after drilling down, got %+v ok=%v", selected, ok)
	}
	if rendered := m.renderSlashSuggestions(); !strings.Contains(rendered, "usage") || !strings.Contains(rendered, "Show token and cost usage") {
		t.Fatalf("expected /stats option list after selection, got:\n%s", rendered)
	}
}

func TestPluginSlashCommandsFollowEnabledPluginState(t *testing.T) {
	m := newModel(nil, "", "", "")
	next, _ := m.Update(svcMsg(protocol.Event{
		Kind: protocol.EventSessionHydrated,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "memory", Name: "Memory"},
			Enabled:  true,
			Commands: []protocol.PluginCommand{{Name: "/memory", Usage: "/memory [list|path]", Description: "Manage memory entries"}},
		}},
	}))
	m = next.(model)
	m.input.SetValue("/mem")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/memory ")

	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventPluginsManagerUpdated,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "memory", Name: "Memory"},
			Enabled:  false,
			Commands: []protocol.PluginCommand{{Name: "/memory", Usage: "/memory [list|path]", Description: "Manage memory entries"}},
		}},
	})
	m.input.SetValue("/mem")
	m.updateSlashMatches()
	if len(m.slash.matches) != 0 {
		t.Fatalf("disabled plugin command should disappear, got %+v", m.slash.matches)
	}
}

func TestPluginTurnSlashCommandStartsNormalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventSessionHydrated,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "demo", Name: "Demo"},
			Enabled:  true,
			Commands: []protocol.PluginCommand{{Name: "/audit", Usage: "/audit [target]", Description: "Audit a target", Class: "turn"}},
		}},
	})
	m.input.SetValue("/audit repo")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "/audit repo" {
		t.Fatalf("plugin turn command should start a normal submit, got %+v", got)
	}
}

func TestPluginReadOnlySlashCommandUsesLocalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventSessionHydrated,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "demo", Name: "Demo"},
			Enabled:  true,
			Commands: []protocol.PluginCommand{{Name: "/where", Usage: "/where", Description: "Show location", Class: "read_only"}},
		}},
	})
	m.input.SetValue("/where")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/where" {
		t.Fatalf("plugin read-only command should use local submit, got %+v", got)
	}
}

func TestPluginMutatingTurnSlashCommandStartsNormalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.handleServiceEvent(protocol.Event{
		Kind: protocol.EventSessionHydrated,
		Plugins: []protocol.PluginStatus{{
			Manifest: protocol.PluginManifest{ID: "demo", Name: "Demo"},
			Enabled:  true,
			Commands: []protocol.PluginCommand{{Name: "/build", Usage: "/build", Description: "Run plugin build shell command", Class: "mutating", StartsTurn: true}},
		}},
	})
	m.input.SetValue("/build")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmit || got.Input != "/build" {
		t.Fatalf("plugin mutating turn command should start a normal submit, got %+v", got)
	}
}

func TestSlashCommandWithOptionsAndAutoRunStillExecutesBareCommand(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/rev")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/review")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected selected /review to dispatch, got %+v", *intents)
	}
	if (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != "/review" {
		t.Fatalf("unexpected dispatched intent: %+v", (*intents)[0])
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared after /review autorun, got %q", got)
	}
}
func TestSlashOptionSuggestionsFilterByTypedPrefix(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/review p")
	m.updateSlashMatches()
	if len(m.slash.matches) != 1 {
		t.Fatalf("expected one /review option match, got %+v", m.slash.matches)
	}
	if got := m.slash.matches[0].InsertText; got != "/review pr " {
		t.Fatalf("expected /review pr option, got %q", got)
	}
}
func TestSlashOptionNeedingArgumentOnlyFillsInput(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/review p")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/review pr ")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("/review pr option should wait for argument, got intents %+v", *intents)
	}
	if got := m.input.Value(); got != "/review pr " {
		t.Fatalf("expected /review pr option to keep trailing space, got %q", got)
	}
}
func TestSlashExactOptionDoesNotShowNestedSuggestions(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/review branch")
	m.updateSlashMatches()
	if m.hasSlashPanel() {
		t.Fatalf("expected no nested slash panel for exact option, got hint=%q matches=%+v", m.slash.argumentHint, m.slash.matches)
	}
}
func TestSlashSuggestionsHiddenForAbsolutePathInput(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/Users/goranka/Engineer/ai/dsk 里有好几个go项目的，你看看它们怎么做的")
	m.updateSlashMatches()
	if len(m.slash.matches) != 0 {
		t.Fatalf("expected slash suggestions hidden for absolute path prompt, got %+v", m.slash.matches)
	}
}
func TestFileSuggestionsShownForAtInput(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "internal", "tui", "model.go"), "package tui\n")
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("@mod")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	if !m.hasFileSuggestions() {
		t.Fatal("expected file suggestions for @mod")
	}
	if got := m.files.matches[0].Path; got != "internal/tui/model.go" {
		t.Fatalf("expected model.go first, got %+v", m.files.matches)
	}
	if m.hasSkillSuggestions() {
		t.Fatalf("expected skill suggestions hidden while file suggestions are visible, got %+v", m.skills.matches)
	}
}
func TestBareAtShowsFileHintWithoutScanning(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	m, intents := newModelWithDispatchSpy()
	m.cwdPath = dir
	m.input.SetValue("@")
	if cmd := m.updateSlashMatches(); cmd != nil {
		t.Fatal("bare @ should not start a file suggestion search")
	}
	if m.hasFileSuggestions() {
		t.Fatalf("bare @ should not expand file suggestions, got %+v", m.files.matches)
	}
	if !m.hasFilePanel() {
		t.Fatal("bare @ should show the file hint panel")
	}
	if rendered := m.renderFileSuggestions(); !strings.Contains(rendered, "Type to search workspace files") {
		t.Fatalf("expected idle file-search hint, got:\n%s", rendered)
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "@" {
		t.Fatalf("tab on bare @ should preserve input, got %q", got)
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "" {
		t.Fatalf("enter on bare @ should submit and clear input, got %q", got)
	}
	if len(*intents) != 1 || (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "@" {
		t.Fatalf("expected bare @ to submit as normal text, got %+v", *intents)
	}
}
func TestFindFileSuggestionsEmptyQueryReturnsNoMatches(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	if got := findFileSuggestions(dir, ""); len(got) != 0 {
		t.Fatalf("empty query should not scan and return matches, got %+v", got)
	}
}
func TestFindFileSuggestionsRanksLaterWorkspaceMatches(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 600; i++ {
		writeFileSuggestionFixture(t, filepath.Join(dir, "aaa", fmt.Sprintf("target.go-%03d.md", i)), "noise\n")
	}
	writeFileSuggestionFixture(t, filepath.Join(dir, "zzz", "src", "target.go"), "package src\n")

	got := findFileSuggestions(dir, "target.go")
	if len(got) == 0 {
		t.Fatal("expected file suggestions")
	}
	if got[0].Path != "zzz/src/target.go" {
		t.Fatalf("expected later exact workspace match first, got %+v", got)
	}
}
func TestFileSuggestionEnterInsertsSelectedPath(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "internal", "tui", "model.go"), "package tui\n")
	m, intents := newModelWithDispatchSpy()
	m.cwdPath = dir
	m.input.SetValue("review @mod")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	if !m.hasFileSuggestions() {
		t.Fatal("expected file suggestions")
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "review internal/tui/model.go " {
		t.Fatalf("expected selected path inserted, got %q", got)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no dispatch when inserting file suggestion, got %+v", *intents)
	}
	if m.hasFileSuggestions() {
		t.Fatalf("expected file suggestions cleared, got %+v", m.files.matches)
	}
	if m.hasFilePanel() {
		t.Fatal("expected file suggestion panel cleared after insertion")
	}
}
func TestFileSuggestionTabQuotesPathsWithSpaces(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "docs", "my file.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("@my")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != `"docs/my file.md" ` {
		t.Fatalf("expected quoted selected path, got %q", got)
	}
}
func TestFileSuggestionTabEscapesQuotedPathWithSpaces(t *testing.T) {
	if got := quoteFileSuggestionPath(`docs/my "file".md`); got != `"docs/my \"file\".md"` {
		t.Fatalf("expected escaped quoted path, got %q", got)
	}
}
func TestFileSuggestionEscClearsSuggestionsWithoutMutatingInput(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("@read")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	if !m.hasFileSuggestions() {
		t.Fatal("expected file suggestions")
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.input.Value(); got != "@read" {
		t.Fatalf("expected esc to preserve input, got %q", got)
	}
	if m.hasFileSuggestions() || m.files.selected != 0 {
		t.Fatalf("expected file suggestions cleared, got matches=%v selected=%d", m.files.matches, m.files.selected)
	}
}
func TestFileSuggestionsHiddenForMultilineBusyAndHistory(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("@read\nmore")
	m.updateSlashMatches()
	if m.hasFileSuggestions() {
		t.Fatalf("expected file suggestions hidden for multiline input, got %+v", m.files.matches)
	}
	m.input.SetValue("@read")
	m.busy = true
	m.updateSlashMatches()
	if m.hasFileSuggestions() {
		t.Fatalf("expected file suggestions hidden while busy, got %+v", m.files.matches)
	}
	m.busy = false
	m.inHistoryNav = true
	m.lastHistoryText = "@read"
	m.updateSlashMatches()
	if m.hasFileSuggestions() {
		t.Fatalf("expected file suggestions hidden during history navigation, got %+v", m.files.matches)
	}
}
func TestFileSuggestionsTakePriorityInsideSlashArguments(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "docs", "review.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("/review @rev")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	if m.hasSlashPanel() {
		t.Fatalf("expected file suggestions to suppress slash panel, hint=%q matches=%+v", m.slash.argumentHint, m.slash.matches)
	}
	if !m.hasFileSuggestions() {
		t.Fatal("expected file suggestions inside slash arguments")
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "/review docs/review.md " {
		t.Fatalf("expected selected path inserted into slash command argument, got %q", got)
	}
}
func TestFileSuggestionsWorkInsideOpenSlashArguments(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("/open @read")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	if m.hasSlashPanel() {
		t.Fatalf("expected file suggestions to suppress /open slash panel, hint=%q matches=%+v", m.slash.argumentHint, m.slash.matches)
	}
	if !m.hasFileSuggestions() {
		t.Fatal("expected file suggestions inside /open arguments")
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/open README.md " {
		t.Fatalf("expected selected path inserted into /open argument, got %q", got)
	}
}
func TestFileSuggestionsQuoteOpenSlashArgumentsWithSpaces(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "docs", "my file.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("/open @my")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	if !m.hasFileSuggestions() {
		t.Fatal("expected file suggestions inside /open arguments")
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != `/open "docs/my file.md" ` {
		t.Fatalf("expected quoted selected path inserted into /open argument, got %q", got)
	}
}
func TestFileSuggestionsEscapeWorkspaceRelativeTildeForOpen(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "~", "notes.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("/open @notes")
	m = runFileSuggestionSearchForTest(t, m, m.updateSlashMatches())
	if !m.hasFileSuggestions() {
		t.Fatal("expected file suggestions inside /open arguments")
	}
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/open ./~/notes.md " {
		t.Fatalf("expected workspace-relative tilde path escaped for /open, got %q", got)
	}
}
func TestFileSuggestionsIgnoreStaleAsyncResults(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "internal", "tui", "model.go"), "package tui\n")
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("@mod")
	staleCmd := m.updateSlashMatches()
	m.input.SetValue("@read")
	freshCmd := m.updateSlashMatches()
	m = runFileSuggestionSearchForTest(t, m, staleCmd)
	if m.hasFileSuggestions() {
		t.Fatalf("expected stale results ignored, got %+v", m.files.matches)
	}
	m = runFileSuggestionSearchForTest(t, m, freshCmd)
	if !m.hasFileSuggestions() || m.files.matches[0].Path != "README.md" {
		t.Fatalf("expected fresh README match, got %+v", m.files.matches)
	}
}
func TestFileSuggestionsCancelPreviousAsyncSearch(t *testing.T) {
	dir := t.TempDir()
	writeFileSuggestionFixture(t, filepath.Join(dir, "internal", "tui", "model.go"), "package tui\n")
	writeFileSuggestionFixture(t, filepath.Join(dir, "README.md"), "# test\n")
	m := newModel(nil, "", "", "")
	m.cwdPath = dir
	m.input.SetValue("@mod")
	staleCmd := m.updateSlashMatches()
	m.input.SetValue("@read")
	freshCmd := m.updateSlashMatches()

	msg, ok := staleCmd().(fileSuggestionsLoadedMsg)
	if !ok {
		t.Fatalf("expected fileSuggestionsLoadedMsg, got %T", msg)
	}
	if len(msg.matches) != 0 {
		t.Fatalf("expected canceled search to return no matches, got %+v", msg.matches)
	}
	m = runFileSuggestionSearchForTest(t, m, freshCmd)
	if !m.hasFileSuggestions() || m.files.matches[0].Path != "README.md" {
		t.Fatalf("expected fresh search to remain usable, got %+v", m.files.matches)
	}
}
func TestSlashSuggestionEnterAutoRunsSingleCommandAndClearsSuggestions(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/co")
	m.updateSlashMatches()
	if len(m.slash.matches) == 0 {
		t.Fatal("expected slash matches")
	}
	selectSlashCommand(t, &m, "/compact")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one dispatched intent, got %+v", *intents)
	}
	if (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != "/compact" {
		t.Fatalf("unexpected dispatched intent: %+v", (*intents)[0])
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared after autorun slash enter, got %q", got)
	}
	if len(m.slash.matches) != 0 || m.slash.selected != 0 {
		t.Fatalf("expected slash state cleared, got matches=%v selected=%d", m.slash.matches, m.slash.selected)
	}
	if !m.busy || m.status != "running" {
		t.Fatalf("expected running state after autorun slash enter, busy=%v status=%q", m.busy, m.status)
	}
}
func TestHelpCommandOpensInteractiveHelp(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.width = 100
	m.height = 30
	m.input.SetValue("/help")

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if len(*intents) != 0 {
		t.Fatalf("/help should open local help without dispatching, got %+v", *intents)
	}
	if m.mode != modeHelp {
		t.Fatalf("expected help mode, got %v", m.mode)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared, got %q", got)
	}
	view := m.View()
	for _, want := range []string{"Whale help", "Browse default commands:", "/diff", "For more help:", helpDocsURL, "Esc to cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected help view to contain %q:\n%s", want, view)
		}
	}
	for _, msg := range m.transcript {
		if msg.Role == "you" && msg.Text == "/help" {
			t.Fatalf("/help should not be written as a user transcript row")
		}
	}
}
func TestHelpCommandKeyboardNavigationIgnoresMouse(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 100
	m.height = 18
	m.openHelp()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if m.help.selected != 1 {
		t.Fatalf("expected down to move help selection, got %d", m.help.selected)
	}

	next, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = next.(model)
	if m.help.selected != 1 {
		t.Fatalf("expected mouse wheel to be ignored in help, got selection %d", m.help.selected)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.mode != modeChat {
		t.Fatalf("expected esc to close help, got mode %v", m.mode)
	}
}
func TestShiftTabModeToggleDoesNotStartWorkingState(t *testing.T) {
	m, intents := newModelWithDispatchSpy()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one mode toggle intent, got %+v", *intents)
	}
	if (*intents)[0].Kind != protocol.IntentToggleMode {
		t.Fatalf("unexpected intent: %+v", (*intents)[0])
	}
	if m.busy || !m.busySince.IsZero() {
		t.Fatalf("mode toggle should not start working state, busy=%v busySince=%v", m.busy, m.busySince)
	}
	if m.status != "ready" {
		t.Fatalf("mode toggle should wait for service info instead of local switching status, got %q", m.status)
	}
	if strings.Contains(m.View(), "Working") {
		t.Fatalf("mode toggle should not render working status:\n%s", m.View())
	}
}
func TestShiftTabModeToggleWaitsForPendingLocalSubmit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.localSubmitPending = 1
	m.status = "command pending"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = next.(model)

	if len(*intents) != 0 {
		t.Fatalf("pending local submit should block mode toggle intent, got %+v", *intents)
	}
	if m.localSubmitPending != 1 {
		t.Fatalf("expected pending local submit to remain, got %d", m.localSubmitPending)
	}
	if m.status != "wait for command to finish" {
		t.Fatalf("expected wait status while local submit is pending, got %q", m.status)
	}
	if m.busy {
		t.Fatal("mode shortcut barrier should not start working state")
	}
}
func TestLocalImmediateSlashCommandsDoNotStartWorkingState(t *testing.T) {
	for _, cmd := range []string{
		"/agent",
		"/ask",
		"/plan",
		"/model",
		"/permissions",
		"/skills",
		"/status",
		"/stats",
		"/stats usage",
		"/stats cache",
		"/stats tools",
		"/stats repair",
		"/stats recent",
		"/stats profile",
		"/stats all",
		"/mcp",
		"/resume",
		"/clear",
		"/new",
		"/new scratch",
		"/fork",
		"/fork scratch",
		"/model xxx",
		"/skills xxx",
		"/resume xxx",
		"/new a b",
		"/fork a b",
		"/stats bad",
		"/compact bad",
		"/plan show",
	} {
		t.Run(cmd, func(t *testing.T) {
			m, intents := newModelWithDispatchSpy()
			m.input.SetValue(cmd)

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(model)
			if len(*intents) != 1 {
				t.Fatalf("expected one dispatched intent, got %+v", *intents)
			}
			if (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != cmd {
				t.Fatalf("unexpected dispatched intent: %+v", (*intents)[0])
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("expected input cleared after %s, got %q", cmd, got)
			}
			if m.busy || !m.busySince.IsZero() {
				t.Fatalf("%s should not start working state, busy=%v busySince=%v", cmd, m.busy, m.busySince)
			}
			for _, msg := range m.transcript {
				if msg.Role == "you" && msg.Text == cmd {
					t.Fatalf("%s should not be written as a user transcript row", cmd)
				}
			}
		})
	}
}
func TestOpenCommandUsesTerminalExecInsteadOfServiceDispatch(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/open .")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if cmd == nil {
		t.Fatal("expected /open to return a terminal exec command")
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no service dispatch for /open, got %+v", *intents)
	}
	if m.localSubmitPending != 1 {
		t.Fatalf("expected pending local submit, got %d", m.localSubmitPending)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared, got %q", got)
	}
	if m.busy || !m.busySince.IsZero() {
		t.Fatalf("/open should not start working state, busy=%v busySince=%v", m.busy, m.busySince)
	}
}
func TestSlashSuggestionTabFillsInputWithoutDispatch(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.windowsPaste.enabled = true
	m.input.SetValue("/co")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/compact")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("expected no dispatch on tab, got %+v", *intents)
	}
	if got := m.input.Value(); got != "/compact" {
		t.Fatalf("expected tab to fill exact command, got %q", got)
	}
	if len(m.slash.matches) == 0 {
		t.Fatal("expected slash matches to remain after tab completion")
	}
}
func TestSlashSuggestionEscClearsSuggestionsWithoutMutatingInput(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.input.SetValue("/co")
	m.updateSlashMatches()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if got := m.input.Value(); got != "/co" {
		t.Fatalf("expected esc to preserve input, got %q", got)
	}
	if len(m.slash.matches) != 0 || m.slash.selected != 0 {
		t.Fatalf("expected esc to clear slash suggestions, got matches=%v selected=%d", m.slash.matches, m.slash.selected)
	}
}
func TestSkillSuggestionsShownForDollarInput(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.skills.all = []skillSuggestion{
		{Name: "code-review", Description: "Review local changes", When: "Use when reviewing code"},
		{Name: "release", Description: "Prepare a release"},
	}
	m.input.SetValue("$rev")
	m.updateSlashMatches()
	if len(m.skills.matches) != 1 || m.skills.matches[0].Name != "code-review" {
		t.Fatalf("expected code-review skill match, got %+v", m.skills.matches)
	}
	if m.hasSlashSuggestions() {
		t.Fatalf("expected slash suggestions to stay hidden for skill input: %+v", m.slash.matches)
	}
}
func TestSkillSuggestionEnterInsertsMention(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.skills.all = []skillSuggestion{{Name: "code-review", Description: "Review local changes", SkillFilePath: "/tmp/code-review/SKILL.md"}}
	m.input.SetValue("$co")
	m.updateSlashMatches()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if got := m.input.Value(); got != "$code-review " {
		t.Fatalf("expected selected skill inserted, got %q", got)
	}
	if m.skillBinding == nil || m.skillBinding.Name != "code-review" || m.skillBinding.SkillFilePath != "/tmp/code-review/SKILL.md" {
		t.Fatalf("expected skill binding for selected mention, got %+v", m.skillBinding)
	}
	if len(*intents) != 0 {
		t.Fatalf("expected no dispatch when inserting skill mention, got %+v", *intents)
	}
	if len(m.skills.matches) != 0 || m.skills.selected != 0 {
		t.Fatalf("expected skill suggestions cleared, got matches=%v selected=%d", m.skills.matches, m.skills.selected)
	}
}
func TestSkillSuggestionDownNavigationPreservesSelection(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.skills.all = []skillSuggestion{
		{Name: "code-review", Description: "Review local changes"},
		{Name: "git-worktree", Description: "Create an isolated worktree"},
		{Name: "grill-me", Description: "Interview the user relentlessly"},
		{Name: "skill-creator", Description: "Create or update skills"},
	}
	m.input.SetValue("$")
	m.updateSlashMatches()
	if len(m.skills.matches) != 4 {
		t.Fatalf("expected four skill matches, got %+v", m.skills.matches)
	}

	for i := 0; i < 3; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(model)
	}
	if got := m.skills.selected; got != 3 {
		t.Fatalf("expected selected index 3 after three down presses, got %d", got)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if got := m.input.Value(); got != "$skill-creator " {
		t.Fatalf("expected selected skill inserted, got %q", got)
	}
}
func TestSkillSuggestionSubmitIncludesBinding(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.skills.all = []skillSuggestion{{Name: "code-review", Description: "Review local changes", SkillFilePath: "/tmp/code-review/SKILL.md"}}
	m.input.SetValue("$co")
	m.updateSlashMatches()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	m.input.SetValue("$code-review review this diff")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	got := (*intents)[0]
	if got.Kind != protocol.IntentSubmit || got.Input != "$code-review review this diff" {
		t.Fatalf("unexpected submit intent: %+v", got)
	}
	if got.SkillBinding == nil || got.SkillBinding.Name != "code-review" || got.SkillBinding.SkillFilePath != "/tmp/code-review/SKILL.md" {
		t.Fatalf("expected submit skill binding, got %+v", got.SkillBinding)
	}
}
func TestSkillSuggestionSubmitDropsStaleBindingAfterNameEdit(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.skills.all = []skillSuggestion{{Name: "code-review", Description: "Review local changes", SkillFilePath: "/tmp/code-review/SKILL.md"}}
	m.input.SetValue("$co")
	m.updateSlashMatches()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	m.input.SetValue("$find-skills")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one submit intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.SkillBinding != nil {
		t.Fatalf("expected stale binding to be dropped, got %+v", got.SkillBinding)
	}
}
func TestSkillSuggestionsHiddenForSlashAndBusy(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.skills.all = []skillSuggestion{{Name: "code-review", Description: "Review local changes"}}
	m.input.SetValue("/")
	m.updateSlashMatches()
	if len(m.skills.matches) != 0 {
		t.Fatalf("expected skill suggestions hidden for slash input, got %+v", m.skills.matches)
	}
	m.input.SetValue("$co")
	m.busy = true
	m.updateSlashMatches()
	if len(m.skills.matches) != 0 {
		t.Fatalf("expected skill suggestions hidden while busy, got %+v", m.skills.matches)
	}
}
func TestSkillSuggestionsHiddenAfterInsertedMentionWithSpace(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.skills.all = []skillSuggestion{{Name: "code-review", Description: "Review local changes"}}
	m.input.SetValue("$code-review ")
	m.updateSlashMatches()
	if len(m.skills.matches) != 0 {
		t.Fatalf("expected skill suggestions hidden after mention insert, got %+v", m.skills.matches)
	}
}
func TestSlashSuggestionAskAutoRunsWhenSelected(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/as")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/ask")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 1 {
		t.Fatalf("expected one dispatch for selected /ask, got %+v", *intents)
	}
	if (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != "/ask" {
		t.Fatalf("unexpected dispatched intent: %+v", (*intents)[0])
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected input cleared after /ask autorun, got %q", got)
	}
	if m.busy || !m.busySince.IsZero() {
		t.Fatalf("expected /ask autorun not to start working state, busy=%v busySince=%v", m.busy, m.busySince)
	}
}
func TestSlashTurnStartingCommandsStillStartWorkingState(t *testing.T) {
	for _, prompt := range []string{"/ask inspect the parser", "/plan propose a fix", "/compact", "/init"} {
		t.Run(prompt, func(t *testing.T) {
			m, intents := newModelWithDispatchSpy()
			m.input.SetValue(prompt)

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(model)
			if len(*intents) != 1 {
				t.Fatalf("expected one dispatched intent, got %+v", *intents)
			}
			if (*intents)[0].Kind != protocol.IntentSubmit || (*intents)[0].Input != prompt {
				t.Fatalf("unexpected dispatched intent: %+v", (*intents)[0])
			}
			if !m.busy || m.status != "running" {
				t.Fatalf("expected prompt command to start working state, busy=%v status=%q", m.busy, m.status)
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("expected input cleared after prompt submit, got %q", got)
			}
		})
	}
}
func TestEnterWhileBusyExecutesReadOnlySlashAndExitImmediately(t *testing.T) {
	for _, cmd := range []string{"/status", "/stats usage", "/stats repair", "/mcp", "/diff", "/exit"} {
		t.Run(cmd, func(t *testing.T) {
			m, intents := newModelWithDispatchSpy()
			m.busy = true
			m.status = "running"
			m.input.SetValue(cmd)

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(model)

			if len(*intents) != 1 {
				t.Fatalf("expected immediate local dispatch, got %+v", *intents)
			}
			if (*intents)[0].Kind != protocol.IntentSubmitLocal || (*intents)[0].Input != cmd {
				t.Fatalf("unexpected dispatched intent: %+v", (*intents)[0])
			}
			if got := m.input.Value(); got != "" {
				t.Fatalf("expected input cleared after %s, got %q", cmd, got)
			}
			if len(m.queuedPrompts) != 0 {
				t.Fatalf("expected no queued prompts, got %+v", m.queuedPrompts)
			}
			if !m.busy {
				t.Fatal("expected active turn to remain busy")
			}
			if m.localSubmitPending != 1 {
				t.Fatalf("expected pending local submit count to be 1, got %d", m.localSubmitPending)
			}
		})
	}
}
func TestEnterWhileBusyBlocksSlashCommandsWithoutQueueing(t *testing.T) {
	for _, cmd := range []string{
		"/resume",
		"/clear",
		"/new scratch",
		"/fork",
		"/fork scratch",
		"/model",
		"/skills",
		"/stats bad",
		"/compact bad",
		"/plan show",
		"/ask inspect the parser",
		"/plan propose a fix",
		"/compact",
		"/init",
		"/unknown",
	} {
		t.Run(cmd, func(t *testing.T) {
			m, intents := newModelWithDispatchSpy()
			m.busy = true
			m.status = "running"
			m.input.SetValue(cmd)

			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = next.(model)

			if len(*intents) != 0 {
				t.Fatalf("expected no submitted intent while busy, got %+v", *intents)
			}
			if len(m.queuedPrompts) != 0 {
				t.Fatalf("expected local command not to be queued, got %+v", m.queuedPrompts)
			}
			if got := m.input.Value(); got != cmd {
				t.Fatalf("expected command to remain editable, got %q", got)
			}
			if !m.busy {
				t.Fatal("expected active turn to remain busy")
			}
			if !strings.Contains(m.status, "disabled while working") {
				t.Fatalf("expected disabled status, got %q", m.status)
			}
			gotTranscript := strings.Join(tuirender.ChatLines(m.chatMessages(), 80), "\n")
			if strings.Contains(gotTranscript, "disabled while") {
				t.Fatalf("blocked-command guidance should not be inserted into chat messages:\n%s", gotTranscript)
			}
			m.width = 100
			m.height = 24
			if view := m.View(); !strings.Contains(view, "disabled while working") {
				t.Fatalf("expected blocked-command guidance in busy status line:\n%s", view)
			}
		})
	}
}
func TestChatFooterFollowsContentAfterSlashSuggestionsClose(t *testing.T) {
	m := newModel(nil, "deepseek-v4-pro", "normal", "on")
	m.width = 100
	m.height = 24
	m.cwd = "~/Engineer/ai/dsk/whale"

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = next.(model)
	withSlash := m.View()
	assertFooterLastLine(t, withSlash, "deepseek-v4-pro . normal")
	assertFooterLastLine(t, withSlash, "whale")
	assertFooterLastLineNotContains(t, withSlash, "dir:")
	if !strings.Contains(withSlash, "Tab/Enter pick") {
		t.Fatalf("expected slash suggestions while / is present:\n%s", withSlash)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(model)
	afterDelete := m.View()
	assertFooterLastLine(t, afterDelete, "deepseek-v4-pro . normal")
	assertFooterLastLine(t, afterDelete, "whale")
	assertFooterLastLineNotContains(t, afterDelete, "dir:")
	if strings.Contains(afterDelete, "Tab/Enter pick") {
		t.Fatalf("expected slash suggestions to disappear after deleting /:\n%s", afterDelete)
	}
	if got := countVisibleLines(afterDelete); got >= m.height {
		t.Fatalf("expected short chat view to use natural height below terminal height %d, got %d:\n%s", m.height, got, afterDelete)
	}
}
func TestLocalSlashCommandsEchoBeforeResults(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 80
	m.height = 18
	localInfo := func(text string) protocol.Event {
		return protocol.Event{Kind: protocol.EventLocalSubmitResult, Status: "info", Text: text}
	}
	localDone := func() protocol.Event {
		return protocol.Event{Kind: protocol.EventLocalSubmitDone, Metadata: map[string]any{protocol.EventMetadataLocalSubmit: true}}
	}

	m.input.SetValue("/mcp")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(svcMsg(localInfo("MCP\n\nconfig: /tmp/mcp.json servers: none")))
	m = next.(model)
	next, _ = m.Update(svcMsg(localDone()))
	m = next.(model)

	m.input.SetValue("/status")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(svcMsg(localInfo("Status\n\nsession: test-session")))
	m = next.(model)
	next, _ = m.Update(svcMsg(localDone()))
	m = next.(model)

	got := strings.Join(tuirender.ChatLines(m.transcript, 80), "\n")
	for _, want := range []string{"/mcp", "config: /tmp/mcp.json", "/status", "session: test-session"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected transcript to contain %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "/mcp") > strings.Index(got, "config: /tmp/mcp.json") {
		t.Fatalf("expected /mcp before its result:\n%s", got)
	}
	if strings.Index(got, "/status") > strings.Index(got, "session: test-session") {
		t.Fatalf("expected /status before its result:\n%s", got)
	}
}
func TestBusySlashWarningStaysOutOfLiveTurn(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.width = 100
	m.height = 30
	m.busy = true
	m.status = "running"

	next, _ := m.Update(svcMsg(protocol.Event{Kind: protocol.EventAssistantDelta, Text: "already visible"}))
	m = next.(model)
	m.input.SetValue("/model")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if len(*intents) != 0 {
		t.Fatalf("expected blocked slash not to dispatch, got %+v", *intents)
	}

	rendered := strings.Join(tuirender.ChatLines(m.chatMessages(), 100), "\n")
	if !strings.Contains(rendered, "already visible") {
		t.Fatalf("expected existing live assistant output:\n%s", rendered)
	}
	if strings.Contains(rendered, "disabled while") {
		t.Fatalf("busy slash warning should not be inserted into live chat output:\n%s", rendered)
	}
	if view := m.View(); !strings.Contains(view, "/model disabled while working") {
		t.Fatalf("expected busy slash warning in status line:\n%s", view)
	}
	if view := m.View(); strings.Contains(view, "Enter to queue") {
		t.Fatalf("blocked slash draft should not show queue guidance:\n%s", view)
	}
	if view := m.View(); strings.Contains(view, "Esc/Ctrl+C to interrupt") {
		t.Fatalf("blocked slash draft should not claim Ctrl+C interrupts:\n%s", view)
	}

	next, _ = m.Update(svcMsg(protocol.Event{
		Kind:         protocol.EventTurnDone,
		LastResponse: "already visible recovered tail",
		Metadata:     map[string]any{protocol.EventMetadataAgentTurn: true},
	}))
	m = next.(model)
	rendered = strings.Join(tuirender.ChatLines(m.transcript, 100), "\n")
	assistantIx := strings.Index(rendered, "already visible")
	tailIx := strings.Index(rendered, "recovered tail")
	if assistantIx < 0 || tailIx < 0 || !(assistantIx < tailIx) {
		t.Fatalf("expected committed order assistant then recovered tail:\n%s", rendered)
	}
	if strings.Contains(rendered, "disabled while") {
		t.Fatalf("busy slash warning should not be committed to transcript:\n%s", rendered)
	}
}
func TestBusySlashWarningDoesNotHideProviderRetryStatus(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 140
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("/model")
	m.status = "/model disabled while working"
	m.providerRetryStatus = "API rate limited, retrying in 1s (1/3)"
	m.providerRetryUntil = time.Now().Add(time.Second)

	view := m.View()
	if !strings.Contains(view, "API rate limited, retrying in 1s (1/3) (12s)") {
		t.Fatalf("expected retry status to take priority over blocked slash status:\n%s", view)
	}
	if strings.Contains(view, "/model disabled while working") {
		t.Fatalf("blocked slash status should not hide retry status:\n%s", view)
	}
	if strings.Contains(view, "Enter to queue") {
		t.Fatalf("blocked slash draft should not show queue guidance during retry:\n%s", view)
	}
}
func TestChatBusyViewShowsBlockedSlashDraftHint(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("/model")
	m.status = "/model disabled while working"

	view := m.View()
	if !strings.Contains(view, "/model disabled while working (12s) · Edit command or press Esc to interrupt · Ctrl+C clears draft") {
		t.Fatalf("expected blocked slash busy status line:\n%s", view)
	}
	if strings.Contains(view, "Enter to queue") {
		t.Fatalf("blocked slash draft should not show queue guidance:\n%s", view)
	}
	if strings.Contains(view, "Esc/Ctrl+C to interrupt") {
		t.Fatalf("blocked slash draft should not claim Ctrl+C interrupts:\n%s", view)
	}
}
func TestChatBusyViewShowsBlockedSlashPrefixDraftHint(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("/mo")
	m.status = "/model disabled while working"

	view := m.View()
	if !strings.Contains(view, "/model disabled while working (12s) · Edit command or press Esc to interrupt · Ctrl+C clears draft") {
		t.Fatalf("expected expanded blocked slash prefix status line:\n%s", view)
	}
	if strings.Contains(view, "Enter to queue") {
		t.Fatalf("blocked slash prefix draft should not show queue guidance:\n%s", view)
	}
}
func TestChatBusyViewDoesNotQueueUnsentSlashDraft(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 120
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("/model")

	view := m.View()
	if !strings.Contains(view, "Working (12s) · Slash commands are disabled while working · Esc to interrupt · Ctrl+C clears draft") {
		t.Fatalf("expected unsent slash draft busy guidance:\n%s", view)
	}
	if strings.Contains(view, "Enter to queue") {
		t.Fatalf("unsent slash draft should not show queue guidance:\n%s", view)
	}
}
func TestChatBusyViewShowsRunHintForBusyImmediateSlashDraft(t *testing.T) {
	for _, draft := range []string{"/status", "/btw remember this"} {
		t.Run(draft, func(t *testing.T) {
			m := newModel(nil, "", "", "")
			m.width = 120
			m.height = 24
			m.startBusy()
			m.busySince = time.Now().Add(-12 * time.Second)
			m.input.SetValue(draft)

			view := m.View()
			if !strings.Contains(view, "Working (12s) · Enter to run · Esc to interrupt · Ctrl+C clears draft") {
				t.Fatalf("expected busy-immediate slash draft run guidance:\n%s", view)
			}
			if strings.Contains(view, "Slash commands are disabled while working") {
				t.Fatalf("busy-immediate slash draft should not show disabled guidance:\n%s", view)
			}
			if strings.Contains(view, "Enter to queue") {
				t.Fatalf("busy-immediate slash draft should not show queue guidance:\n%s", view)
			}
		})
	}
}
func TestChatBusyViewDoesNotQueueEditedSlashDraft(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 120
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("/permissions")
	m.status = "/model disabled while working"

	view := m.View()
	if !strings.Contains(view, "Working (12s) · Slash commands are disabled while working · Esc to interrupt · Ctrl+C clears draft") {
		t.Fatalf("expected edited slash draft busy guidance:\n%s", view)
	}
	if strings.Contains(view, "/model disabled while working") {
		t.Fatalf("edited slash draft should not show stale blocked status:\n%s", view)
	}
	if strings.Contains(view, "Enter to queue") {
		t.Fatalf("edited slash draft should not show queue guidance:\n%s", view)
	}
}
func TestChatBusyViewIgnoresStaleBlockedSlashStatusForNormalDraft(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 24
	m.startBusy()
	m.busySince = time.Now().Add(-12 * time.Second)
	m.input.SetValue("normal follow up")
	m.status = "/model disabled while working"

	view := m.View()
	if strings.Contains(view, "/model disabled while working") {
		t.Fatalf("stale blocked slash status should not label normal drafts:\n%s", view)
	}
	if !strings.Contains(view, "Working (12s) · Enter to queue · Esc interrupts and sends · Ctrl+C clears draft") {
		t.Fatalf("expected normal draft queue guidance after slash edit:\n%s", view)
	}
}
func TestChatStoppingViewShowsBlockedSlashStatus(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.width = 100
	m.height = 24
	m.startBusy()
	m.stopping = true
	m.busySince = time.Now().Add(-(time.Minute + 5*time.Second))
	m.input.SetValue("/model")
	m.status = "/model disabled while stopping"

	view := m.View()
	if !strings.Contains(view, "/model disabled while stopping (1m 05s)") {
		t.Fatalf("expected blocked slash stopping status line:\n%s", view)
	}
	if strings.Contains(view, "Enter to queue") {
		t.Fatalf("blocked slash while stopping should not show queue guidance:\n%s", view)
	}
	if strings.Contains(view, "to interrupt") {
		t.Fatalf("stopping view should not show interrupt hint:\n%s", view)
	}
}
func TestBtwSlashSuggestionDoesNotAutoRunWithoutQuestion(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.input.SetValue("/bt")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/btw ")
	suggestion, ok := m.selectedSlashSuggestion()
	if !ok {
		t.Fatal("expected /btw slash suggestion")
	}
	if suggestion.AutoRun {
		t.Fatal("/btw should not auto-run from suggestions without a question")
	}
}
func TestBtwSlashSuggestionEnterCompletesWithSpace(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.input.SetValue("/bt")
	m.updateSlashMatches()

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if got := m.input.Value(); got != "/btw " {
		t.Fatalf("expected /btw completion with trailing space, got %q", got)
	}
	if m.hasSlashSuggestions() {
		t.Fatalf("expected suggestions hidden after required-arg completion, got %+v", m.slash.matches)
	}
}

func TestStopSlashSuggestionEnterAutoRuns(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/sto")
	m.updateSlashMatches()
	selectSlashCommand(t, &m, "/stop")

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(*intents) != 1 {
		t.Fatalf("expected /stop suggestion to submit one intent, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/stop" {
		t.Fatalf("unexpected /stop suggestion intent: %+v", got)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("expected /stop auto-run to clear input, got %q", got)
	}
	if m.hasSlashSuggestions() {
		t.Fatalf("expected suggestions hidden after /stop auto-run, got %+v", m.slash.matches)
	}
}
func TestBtwExactSlashEnterShowsUsage(t *testing.T) {
	m, intents := newModelWithDispatchSpy()
	m.input.SetValue("/btw")
	m.updateSlashMatches()

	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(*intents) != 1 {
		t.Fatalf("expected /btw usage local submit, got %+v", *intents)
	}
	if got := (*intents)[0]; got.Kind != protocol.IntentSubmitLocal || got.Input != "/btw" {
		t.Fatalf("unexpected intent: %+v", got)
	}
}
func TestSlashSuggestionNavigationWrapsAround(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.input.SetValue("/")
	m.updateSlashMatches()
	count := len(m.slash.matches)
	if count < 2 {
		t.Fatalf("expected at least two slash suggestions, got %d", count)
	}

	// Down from the last item wraps back to the first.
	m.slash.selected = count - 1
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.slash.selected != 0 {
		t.Fatalf("expected down at last to wrap to first, got %d", m.slash.selected)
	}

	// Up from the first item wraps to the last.
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.slash.selected != count-1 {
		t.Fatalf("expected up at first to wrap to last, got %d", m.slash.selected)
	}
}
func TestSkillSuggestionNavigationWrapsAround(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.skills.all = []skillSuggestion{
		{Name: "code-review", Description: "Review local changes"},
		{Name: "git-worktree", Description: "Create an isolated worktree"},
		{Name: "grill-me", Description: "Interview the user relentlessly"},
		{Name: "skill-creator", Description: "Create or update skills"},
	}
	m.input.SetValue("$")
	m.updateSlashMatches()
	if len(m.skills.matches) != 4 {
		t.Fatalf("expected four skill matches, got %+v", m.skills.matches)
	}

	// Up from the first item wraps to the last.
	m.skills.selected = 0
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.skills.selected != 3 {
		t.Fatalf("expected up at first to wrap to last, got %d", m.skills.selected)
	}

	// Down from the last item wraps back to the first.
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.skills.selected != 0 {
		t.Fatalf("expected down at last to wrap to first, got %d", m.skills.selected)
	}
}
func TestFileSuggestionNavigationWrapsAround(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.files.matches = []fileSuggestion{
		{Path: "a.go"},
		{Path: "b.go"},
		{Path: "c.go"},
	}
	m.files.selected = 2

	// Down from the last item wraps back to the first.
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.files.selected != 0 {
		t.Fatalf("expected down at last to wrap to first, got %d", m.files.selected)
	}

	// Up from the first item wraps to the last.
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.files.selected != 2 {
		t.Fatalf("expected up at first to wrap to last, got %d", m.files.selected)
	}
}
func TestHelpCommandNavigationWrapsAround(t *testing.T) {
	m, _ := newModelWithDispatchSpy()
	m.width = 100
	m.height = 18
	m.openHelp()
	count := len(helpCommands())
	if count < 2 {
		t.Fatalf("expected at least two help commands, got %d", count)
	}

	// Up from the first item wraps to the last.
	m.help.selected = 0
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.help.selected != count-1 {
		t.Fatalf("expected up at first to wrap to last, got %d", m.help.selected)
	}

	// Down from the last item wraps back to the first.
	m, _ = updateTestModel(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.help.selected != 0 {
		t.Fatalf("expected down at last to wrap to first, got %d", m.help.selected)
	}
}
