package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/usewhale/whale/internal/runtime/protocol"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

type configManagerState struct {
	all      []protocol.ConfigSettingView
	matches  []int
	selected int
	query    string
	pending  map[string]string
	status   string
	saved    bool
}

func (m *model) handleConfigManagerEvent(ev protocol.Event) {
	m.setConfigManagerState(ev.Config)
	if !ev.Open && m.mode != modeConfigManager {
		return
	}
	m.clearProviderRetryStatus()
	m.stopBusy()
	m.stopping = false
	m.mode = modeConfigManager
	m.slash.matches = nil
	m.slash.selected = 0
	m.slash.argumentHint = ""
	m.skills.matches = nil
	m.skills.selected = 0
	m.status = "config"
}

func (m *model) handleConfigManagerSubmitResult(ev protocol.Event) bool {
	if m.mode != modeConfigManager || !isConfigManagerSubmitResult(ev.Text) {
		return false
	}
	m.configManager.status = firstConfigManagerResultLine(ev.Text)
	m.configManager.saved = ev.Status != "error"
	return true
}

func (m *model) setConfigManagerState(state *protocol.ConfigManagerState) {
	if state == nil {
		state = &protocol.ConfigManagerState{}
	}
	current := m.selectedConfigSettingID()
	m.configManager.all = append([]protocol.ConfigSettingView(nil), state.Items...)
	if m.configManager.pending == nil {
		m.configManager.pending = map[string]string{}
	}
	m.resetConfigManagerMatches()
	if current != "" {
		for visible, idx := range m.configManager.matches {
			if idx >= 0 && idx < len(m.configManager.all) && m.configManager.all[idx].ID == current {
				m.configManager.selected = visible
				return
			}
		}
	}
}

func (m *model) handleConfigManagerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.closeConfigManager(false)
	case "enter":
		if len(m.configManager.pending) > 0 {
			m.applyConfigManagerChanges()
		} else {
			m.toggleSelectedConfigSetting()
		}
	case "ctrl+s":
		m.applyConfigManagerChanges()
	case "backspace":
		if m.configManager.query != "" {
			runes := []rune(m.configManager.query)
			m.configManager.query = string(runes[:len(runes)-1])
			m.resetConfigManagerMatches()
		} else {
			m.closeConfigManager(false)
		}
	case "up", "k":
		if len(m.configManager.matches) > 0 {
			m.configManager.selected = wrapSelection(m.configManager.selected, len(m.configManager.matches), -1)
		}
	case "down", "j":
		if len(m.configManager.matches) > 0 {
			m.configManager.selected = wrapSelection(m.configManager.selected, len(m.configManager.matches), 1)
		}
	case " ", "space":
		m.toggleSelectedConfigSetting()
	default:
		if msg.Type == tea.KeyRunes {
			m.configManager.query += msg.String()
			m.resetConfigManagerMatches()
		}
	}
	return nil
}

func (m *model) closeConfigManager(apply bool) {
	if apply {
		m.applyConfigManagerChanges()
		return
	}
	m.mode = modeChat
	m.configManager.matches = nil
	m.configManager.selected = 0
	m.configManager.query = ""
	m.configManager.pending = nil
	m.configManager.status = ""
	m.configManager.saved = false
	m.status = "ready"
}

func (m *model) resetConfigManagerMatches() {
	query := strings.ToLower(strings.TrimSpace(m.configManager.query))
	matches := make([]int, 0, len(m.configManager.all))
	for i, item := range m.configManager.all {
		if query == "" || configSettingMatches(item, query) {
			matches = append(matches, i)
		}
	}
	m.configManager.matches = matches
	if len(matches) == 0 {
		m.configManager.selected = 0
		return
	}
	if m.configManager.selected < 0 {
		m.configManager.selected = 0
	}
	if m.configManager.selected >= len(matches) {
		m.configManager.selected = len(matches) - 1
	}
}

func configSettingMatches(item protocol.ConfigSettingView, query string) bool {
	fields := []string{item.ID, item.Label, item.Description, item.Scope, item.Source}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func (m *model) toggleSelectedConfigSetting() {
	idx, ok := m.selectedConfigSettingIndex()
	if !ok {
		return
	}
	item := m.configManager.all[idx]
	if item.Type != "bool" {
		return
	}
	current := m.configSettingValue(item)
	next := "true"
	if strings.EqualFold(current, "true") {
		next = "false"
	}
	if m.configManager.pending == nil {
		m.configManager.pending = map[string]string{}
	}
	if next == item.Value {
		delete(m.configManager.pending, item.ID)
	} else {
		m.configManager.pending[item.ID] = next
	}
	m.configManager.status = ""
	m.configManager.saved = false
}

func (m *model) applyConfigManagerChanges() {
	if len(m.configManager.pending) == 0 {
		return
	}
	ids := make([]string, 0, len(m.configManager.pending))
	for id := range m.configManager.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	updates := make([]protocol.ConfigSettingUpdate, 0, len(ids))
	for _, id := range ids {
		updates = append(updates, protocol.ConfigSettingUpdate{ID: id, Value: m.configManager.pending[id]})
	}
	m.configManager.pending = nil
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentApplyConfigSettings, ConfigUpdates: updates})
}

func (m model) selectedConfigSettingIndex() (int, bool) {
	if m.configManager.selected < 0 || m.configManager.selected >= len(m.configManager.matches) {
		return 0, false
	}
	idx := m.configManager.matches[m.configManager.selected]
	if idx < 0 || idx >= len(m.configManager.all) {
		return 0, false
	}
	return idx, true
}

func (m model) selectedConfigSettingID() string {
	idx, ok := m.selectedConfigSettingIndex()
	if !ok {
		return ""
	}
	return m.configManager.all[idx].ID
}

func (m model) configSettingValue(item protocol.ConfigSettingView) string {
	if m.configManager.pending != nil {
		if pending, ok := m.configManager.pending[item.ID]; ok {
			return pending
		}
	}
	return item.Value
}

func (m model) renderConfigManager() string {
	rows := []string{
		pickerTitle("Config"),
		pickerHint("Search settings"),
		"",
		renderConfigSearch(m.configManager.query),
		"",
	}
	const maxRows = 8
	if len(m.configManager.matches) == 0 {
		if strings.TrimSpace(m.configManager.query) == "" {
			rows = append(rows, pickerHint("  no settings available"))
		} else {
			rows = append(rows, pickerHint(fmt.Sprintf("  no settings match %q", m.configManager.query)))
		}
	} else {
		start := 0
		if len(m.configManager.matches) > maxRows {
			start = m.configManager.selected - maxRows/2
			if start < 0 {
				start = 0
			}
			if start > len(m.configManager.matches)-maxRows {
				start = len(m.configManager.matches) - maxRows
			}
		}
		end := len(m.configManager.matches)
		if end > start+maxRows {
			end = start + maxRows
		}
		for visible := start; visible < end; visible++ {
			item := m.configManager.all[m.configManager.matches[visible]]
			rows = append(rows, m.renderConfigManagerRow(item, visible == m.configManager.selected)...)
		}
	}
	hint := "  type to search · ↑/↓ select · Space toggle · Enter/Ctrl+S save · Esc discard"
	if len(m.configManager.pending) > 0 {
		hint = fmt.Sprintf("  %d pending · ↑/↓ select · Space toggle · Enter/Ctrl+S save · Esc discard", len(m.configManager.pending))
	}
	if m.configManager.status != "" {
		rows = append(rows, "", renderConfigManagerStatus(m.configManager.status, m.configManager.saved))
	}
	rows = append(rows, "", pickerHint(hint))
	return strings.Join(rows, "\n")
}

func isConfigManagerSubmitResult(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "updated ") || text == "config unchanged"
}

func firstConfigManagerResultLine(text string) string {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return "config saved"
}

func renderConfigManagerStatus(text string, saved bool) string {
	tone := tuitheme.Default.Warn
	prefix := "  ! "
	if saved {
		tone = tuitheme.Default.Success
		prefix = "  saved: "
	}
	return lipgloss.NewStyle().Foreground(tone).Bold(true).Render(prefix + text)
}

func renderConfigSearch(query string) string {
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	value := query
	if value == "" {
		value = muted.Render("Search settings...")
	}
	return muted.Render("  / ") + value
}

func (m model) renderConfigManagerRow(item protocol.ConfigSettingView, selected bool) []string {
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	selector := muted.Render("  ")
	if selected {
		selector = lipgloss.NewStyle().Foreground(tuitheme.Default.InfoSoft).Bold(true).Render("> ")
	}
	value := m.configSettingValue(item)
	changed := value != item.Value
	marker := " "
	markerStyle := muted
	if strings.EqualFold(value, "true") {
		marker = "x"
		markerStyle = lipgloss.NewStyle().Foreground(tuitheme.Default.Success).Bold(true)
	}
	if changed {
		markerStyle = lipgloss.NewStyle().Foreground(tuitheme.Default.Warn).Bold(true)
	}
	nameStyle := lipgloss.NewStyle()
	if selected {
		nameStyle = nameStyle.Foreground(tuitheme.Default.InfoSoft).Bold(true)
	}
	head := selector +
		muted.Render("[") +
		markerStyle.Render(marker) +
		muted.Render("]") +
		" " +
		nameStyle.Render(item.Label)
	if changed {
		head += " " + lipgloss.NewStyle().Foreground(tuitheme.Default.Warn).Render("*")
	}
	source := strings.TrimSpace(item.Source)
	if source == "" {
		source = "default"
	}
	scope := strings.TrimSpace(item.Scope)
	if scope == "" {
		scope = "project local"
	}
	meta := fmt.Sprintf("%s · source: %s · saves: %s", item.ID, source, scope)
	detailWidth := m.width - 8
	if detailWidth < 24 {
		detailWidth = 24
	}
	out := []string{head}
	if item.Description != "" {
		out = append(out, "    "+trimDisplayWidth(item.Description, detailWidth))
	}
	out = append(out, "    "+muted.Render(trimDisplayWidth(meta, detailWidth)))
	return out
}

func trimDisplayWidth(s string, width int) string {
	s = strings.TrimSpace(s)
	if width <= 0 || xansi.StringWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return xansi.Truncate(s, width, "")
	}
	if width <= 3 {
		return xansi.Truncate(s, width, "")
	}
	return xansi.Truncate(s, width-3, "") + "..."
}
