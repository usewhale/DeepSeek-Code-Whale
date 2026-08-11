package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

type hooksManagerPage int

const (
	hooksPageEvents hooksManagerPage = iota
	hooksPageHandlers
)

type hooksManagerState struct {
	state             *protocol.HooksManagerState
	page              hooksManagerPage
	selectedEvent     int
	selectedHandler   int
	startupReviewOpen bool
	startupSelected   int
	suppressNextOpen  bool
}

func (m *model) handleHooksManagerEvent(ev protocol.Event) {
	m.setHooksManagerState(ev.Hooks)
	if m.hooksManager.suppressNextOpen {
		m.hooksManager.suppressNextOpen = false
		return
	}
	m.mode = modeHooksManager
}

func (m *model) handleHooksStartupReviewEvent(ev protocol.Event) {
	m.setHooksManagerState(ev.Hooks)
	m.hooksManager.startupReviewOpen = true
	m.hooksManager.startupSelected = 0
	m.mode = modeHooksStartupReview
}

func (m *model) setHooksManagerState(state *protocol.HooksManagerState) {
	if state == nil {
		state = &protocol.HooksManagerState{}
	}
	m.hooksManager.state = state
	if m.hooksManager.selectedEvent >= len(state.Events) {
		m.hooksManager.selectedEvent = max(0, len(state.Events)-1)
	}
	if m.hooksManager.selectedHandler >= len(m.hooksForSelectedEvent()) {
		m.hooksManager.selectedHandler = max(0, len(m.hooksForSelectedEvent())-1)
	}
}

func (m *model) handleHooksStartupReviewKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		if m.hooksManager.startupSelected > 0 {
			m.hooksManager.startupSelected--
		}
	case "down", "j":
		if m.hooksManager.startupSelected < 2 {
			m.hooksManager.startupSelected++
		}
	case "enter":
		switch m.hooksManager.startupSelected {
		case 0:
			m.mode = modeHooksManager
			m.hooksManager.page = hooksPageEvents
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentResolveHooksStartupReview, HooksReviewAction: "review"})
		case 1:
			m.hooksManager.startupReviewOpen = false
			m.hooksManager.suppressNextOpen = true
			m.mode = modeChat
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentResolveHooksStartupReview, HooksReviewAction: "trust_all"})
		default:
			m.hooksManager.startupReviewOpen = false
			m.mode = modeChat
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentResolveHooksStartupReview, HooksReviewAction: "continue"})
		}
	case "esc", "ctrl+c":
		m.hooksManager.startupReviewOpen = false
		m.mode = modeChat
		m.dispatchIntent(protocol.Intent{Kind: protocol.IntentResolveHooksStartupReview, HooksReviewAction: "continue"})
	}
	return nil
}

func (m *model) handleHooksManagerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		if m.hooksManager.page == hooksPageHandlers {
			m.hooksManager.page = hooksPageEvents
			m.hooksManager.selectedHandler = 0
			return nil
		}
		if m.hooksManager.startupReviewOpen {
			m.hooksManager.startupReviewOpen = false
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentResolveHooksStartupReview, HooksReviewAction: "continue"})
		}
		m.mode = modeChat
	case "enter":
		if m.hooksManager.page == hooksPageEvents {
			m.hooksManager.page = hooksPageHandlers
			m.hooksManager.selectedHandler = 0
			return nil
		}
		m.toggleSelectedHook()
	case "up", "k":
		if m.hooksManager.page == hooksPageEvents {
			if len(m.hooksManager.state.Events) > 0 {
				m.hooksManager.selectedEvent = wrapSelection(m.hooksManager.selectedEvent, len(m.hooksManager.state.Events), -1)
			}
		} else if len(m.hooksForSelectedEvent()) > 0 {
			m.hooksManager.selectedHandler = wrapSelection(m.hooksManager.selectedHandler, len(m.hooksForSelectedEvent()), -1)
		}
	case "down", "j":
		if m.hooksManager.page == hooksPageEvents {
			if len(m.hooksManager.state.Events) > 0 {
				m.hooksManager.selectedEvent = wrapSelection(m.hooksManager.selectedEvent, len(m.hooksManager.state.Events), 1)
			}
		} else if len(m.hooksForSelectedEvent()) > 0 {
			m.hooksManager.selectedHandler = wrapSelection(m.hooksManager.selectedHandler, len(m.hooksForSelectedEvent()), 1)
		}
	case " ":
		if m.hooksManager.page == hooksPageHandlers {
			m.toggleSelectedHook()
		}
	case "t":
		if m.hooksManager.page == hooksPageEvents {
			if m.hooksReviewNeededTotal() > 0 {
				m.dispatchIntent(protocol.Intent{Kind: protocol.IntentTrustHooks})
			}
		} else if hook, ok := m.selectedHook(); ok && needsHookReview(hook) {
			m.dispatchIntent(protocol.Intent{Kind: protocol.IntentTrustHook, HookKey: hook.Key})
		}
	}
	return nil
}

func (m *model) toggleSelectedHook() {
	hook, ok := m.selectedHook()
	if !ok || hook.Managed || needsHookReview(hook) {
		return
	}
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetHookEnabled, HookKey: hook.Key, HookEnabled: !hook.Enabled})
}

func (m model) selectedHook() (protocol.HookEntry, bool) {
	hooks := m.hooksForSelectedEvent()
	if m.hooksManager.selectedHandler < 0 || m.hooksManager.selectedHandler >= len(hooks) {
		return protocol.HookEntry{}, false
	}
	return hooks[m.hooksManager.selectedHandler], true
}

func (m model) hooksReviewNeededTotal() int {
	if m.hooksManager.state == nil {
		return 0
	}
	if m.hooksManager.state.ReviewNeededCount > 0 {
		return m.hooksManager.state.ReviewNeededCount
	}
	total := 0
	for _, hook := range m.hooksManager.state.Entries {
		if needsHookReview(hook) {
			total++
		}
	}
	return total
}

func (m model) hooksForSelectedEvent() []protocol.HookEntry {
	if m.hooksManager.state == nil || m.hooksManager.selectedEvent < 0 || m.hooksManager.selectedEvent >= len(m.hooksManager.state.Events) {
		return nil
	}
	event := m.hooksManager.state.Events[m.hooksManager.selectedEvent].Event
	out := []protocol.HookEntry{}
	for _, hook := range m.hooksManager.state.Entries {
		if hook.Event == event {
			out = append(out, hook)
		}
	}
	return out
}

func (m model) renderHooksStartupReview() string {
	count := 0
	if m.hooksManager.state != nil {
		count = m.hooksManager.state.ReviewNeededCount
	}
	rows := []string{
		pickerTitle("Hooks need review"),
		pickerHint(fmt.Sprintf("%d hook(s) changed or untrusted. Untrusted hooks will not run until trusted.", count)),
		"",
	}
	options := []string{"Review hooks", "Trust all and continue", "Continue without trusting"}
	for i, label := range options {
		prefix := "  "
		style := lipgloss.NewStyle()
		if i == m.hooksManager.startupSelected {
			prefix = "> "
			style = style.Foreground(tuitheme.Default.InfoSoft).Bold(true)
		}
		rows = append(rows, style.Render(prefix+label))
	}
	rows = append(rows, "", pickerHint("  ↑/↓ select · Enter choose · Esc continue without trusting"))
	return strings.Join(rows, "\n")
}

func (m model) renderHooksManager() string {
	if m.hooksManager.page == hooksPageHandlers {
		return m.renderHookHandlers()
	}
	return m.renderHookEvents()
}

func (m model) renderHookEvents() string {
	rows := []string{pickerTitle("Hooks"), pickerHint("Lifecycle hooks from config and enabled plugins."), ""}
	if m.hooksManager.state == nil || len(m.hooksManager.state.Events) == 0 {
		rows = append(rows, pickerHint("  no hooks found"))
	} else {
		rows = append(rows, "  Event                 Installed  Active  Review  Description")
		for i, ev := range m.hooksManager.state.Events {
			line := fmt.Sprintf("  %-20s  %-9d  %-6d  %-6d  %s", ev.Event, ev.Installed, ev.Active, ev.Review, ev.Description)
			if i == m.hooksManager.selectedEvent {
				line = lipgloss.NewStyle().Foreground(tuitheme.Default.InfoSoft).Bold(true).Render("> " + strings.TrimPrefix(line, "  "))
			}
			if ev.Review > 0 {
				line = lipgloss.NewStyle().Foreground(tuitheme.Default.Warn).Render(line)
			}
			rows = append(rows, line)
		}
	}
	hint := "  ↑/↓ select · Enter view hooks · Esc close"
	if m.hooksReviewNeededTotal() > 0 {
		hint = "  ↑/↓ select · Enter review hooks · t trust all · Esc close"
	}
	rows = append(rows, "", pickerHint(hint))
	return strings.Join(rows, "\n")
}

func (m model) renderHookHandlers() string {
	state := m.hooksManager.state
	event := ""
	if state != nil && m.hooksManager.selectedEvent >= 0 && m.hooksManager.selectedEvent < len(state.Events) {
		event = state.Events[m.hooksManager.selectedEvent].Event
	}
	rows := []string{pickerTitle("Hooks · " + event), pickerHint("Handlers for selected lifecycle event."), ""}
	hooks := m.hooksForSelectedEvent()
	if len(hooks) == 0 {
		rows = append(rows, pickerHint("  no handlers installed"))
	} else {
		for i, hook := range hooks {
			rows = append(rows, renderHookRow(hook, i == m.hooksManager.selectedHandler, m.width)...)
		}
	}
	rows = append(rows, "", pickerHint(m.hookHandlersHint(hooks)))
	return strings.Join(rows, "\n")
}

func (m model) hookHandlersHint(hooks []protocol.HookEntry) string {
	prefix := "  ↑/↓ select · "
	if len(hooks) == 0 {
		return prefix + "Esc back"
	}
	hook, ok := m.selectedHook()
	if !ok {
		return prefix + "Esc back"
	}
	if hook.Managed {
		return "  Managed hooks are always on · Esc back"
	}
	if needsHookReview(hook) {
		return prefix + "t trust · Esc back"
	}
	return prefix + "Space/Enter toggle · Esc back"
}

func renderHookRow(hook protocol.HookEntry, selected bool, width int) []string {
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	style := lipgloss.NewStyle()
	if selected {
		style = style.Foreground(tuitheme.Default.InfoSoft).Bold(true)
	}
	selector := "  "
	if selected {
		selector = "> "
	}
	marker := " "
	if hook.Active {
		marker = "x"
	}
	if needsHookReview(hook) {
		marker = "!"
	}
	name := hook.Name
	if strings.TrimSpace(name) == "" {
		name = hook.Command
	}
	head := style.Render(fmt.Sprintf("%s[%s] %s", selector, marker, name))
	details := []string{
		"key=" + hook.Key,
		"source=" + hook.Source,
		"trust=" + hook.Trust,
	}
	if hook.Match != "" {
		details = append(details, "match="+hook.Match)
	}
	if hook.Command != "" {
		details = append(details, "command="+hook.Command)
	}
	if hook.TimeoutSec > 0 {
		details = append(details, fmt.Sprintf("timeout=%ds", hook.TimeoutSec))
	}
	out := []string{head}
	detailWidth := width - 8
	if detailWidth < 24 {
		detailWidth = 72
	}
	for _, line := range wrapHookDetail(strings.Join(details, " · "), detailWidth) {
		out = append(out, muted.Render("      "+line))
	}
	return out
}

func wrapHookDetail(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if width < 8 {
		width = 8
	}
	wrapped := strings.TrimRight(xansi.Wordwrap(text, width, " "), "\n")
	if wrapped == "" {
		return nil
	}
	return strings.Split(wrapped, "\n")
}

func needsHookReview(hook protocol.HookEntry) bool {
	return hook.Trust == "Untrusted" || hook.Trust == "Modified"
}
