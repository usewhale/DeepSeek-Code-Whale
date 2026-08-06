package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	appcommands "github.com/usewhale/whale/internal/runtime/commands"
	"github.com/usewhale/whale/internal/runtime/protocol"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
)

type pluginManagerItem struct {
	ID          string
	Name        string
	Description string
	Usage       string
	Enabled     bool
	Status      protocol.PluginStatus
}

func (m *model) setPluginsManagerItems(statuses []protocol.PluginStatus) {
	m.setPluginSlashCommands(statuses)
	current := ""
	if m.pluginsManager.selected >= 0 && m.pluginsManager.selected < len(m.pluginsManager.matches) {
		idx := m.pluginsManager.matches[m.pluginsManager.selected]
		if idx >= 0 && idx < len(m.pluginsManager.all) {
			current = m.pluginsManager.all[idx].ID
		}
	}
	items := make([]pluginManagerItem, 0, len(statuses))
	for _, st := range statuses {
		name := strings.TrimSpace(st.Manifest.Name)
		if name == "" {
			name = st.Manifest.ID
		}
		items = append(items, pluginManagerItem{
			ID:          strings.TrimSpace(st.Manifest.ID),
			Name:        name,
			Description: strings.TrimSpace(st.Manifest.Description),
			Usage:       pluginUsageSummary(st),
			Enabled:     st.Enabled,
			Status:      st,
		})
	}
	m.pluginsManager.all = items
	m.resetPluginsManagerMatches()
	if current != "" {
		for visible, idx := range m.pluginsManager.matches {
			if idx >= 0 && idx < len(m.pluginsManager.all) && m.pluginsManager.all[idx].ID == current {
				m.pluginsManager.selected = visible
				return
			}
		}
	}
}

func (m *model) setPluginSlashCommands(statuses []protocol.PluginStatus) {
	specs := appcommands.DefaultSlashCommands()
	seen := make(map[string]bool, len(specs))
	commandClasses := make(map[string]appcommands.SubmitClass)
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name != "" {
			seen[name] = true
		}
	}
	for _, st := range statuses {
		if !st.Enabled {
			continue
		}
		for _, cmd := range st.Commands {
			name := strings.TrimSpace(cmd.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			commandClasses[name] = pluginSubmitClass(cmd)
			specs = append(specs, appcommands.SlashCommandSpec{
				Name:         name,
				Description:  strings.TrimSpace(cmd.Description),
				ArgumentHint: pluginCommandArgumentHint(cmd),
			})
		}
	}
	m.slash.all = specs
	m.slash.commandClasses = commandClasses
}

func pluginSubmitClass(cmd protocol.PluginCommand) appcommands.SubmitClass {
	if cmd.StartsTurn {
		return appcommands.SubmitTurnStarting
	}
	switch strings.TrimSpace(cmd.Class) {
	case "read_only":
		return appcommands.SubmitLocalReadOnly
	case "mutating":
		return appcommands.SubmitLocalMutating
	case "ui":
		return appcommands.SubmitLocalUI
	default:
		return appcommands.SubmitTurnStarting
	}
}

func pluginCommandArgumentHint(cmd protocol.PluginCommand) string {
	usage := strings.TrimSpace(cmd.Usage)
	name := strings.TrimSpace(cmd.Name)
	if usage == "" || name == "" {
		return ""
	}
	if usage == name {
		return ""
	}
	if strings.HasPrefix(usage, name+" ") {
		return strings.TrimSpace(strings.TrimPrefix(usage, name))
	}
	return usage
}

func pluginUsageSummary(st protocol.PluginStatus) string {
	parts := []string{}
	if len(st.Commands) > 0 {
		usages := make([]string, 0, len(st.Commands))
		for _, cmd := range st.Commands {
			if usage := strings.TrimSpace(cmd.Usage); usage != "" {
				usages = append(usages, usage)
				continue
			}
			if name := strings.TrimSpace(cmd.Name); name != "" {
				usages = append(usages, name)
			}
		}
		if len(usages) > 0 {
			parts = append(parts, "Run "+strings.Join(usages, ", "))
		}
	}
	if len(st.Tools) > 0 {
		parts = append(parts, "Agent tools: "+strings.Join(st.Tools, ", "))
	}
	if len(st.Skills) > 0 {
		parts = append(parts, "Adds skill: $"+strings.Join(st.Skills, ", $"))
	}
	if len(st.Agents) > 0 {
		parts = append(parts, "Subagents: "+strings.Join(st.Agents, ", "))
	}
	if len(st.Rules) > 0 {
		parts = append(parts, fmt.Sprintf("Rules: %d", len(st.Rules)))
	}
	if len(st.Services) > 0 {
		services := make([]string, 0, len(st.Services))
		for _, svc := range st.Services {
			if name := strings.TrimSpace(svc.Name); name != "" {
				services = append(services, name)
			}
		}
		if len(services) > 0 {
			parts = append(parts, "Services: "+strings.Join(services, ", "))
		}
	}
	if len(st.Hooks) > 0 {
		parts = append(parts, fmt.Sprintf("Hooks: %d active", len(st.Hooks)))
	}
	if len(st.Diagnostics) > 0 {
		warnings := 0
		for _, diag := range st.Diagnostics {
			switch strings.TrimSpace(strings.ToLower(diag.Level)) {
			case "warn", "fail", "error":
				warnings++
			}
		}
		if warnings > 0 {
			parts = append(parts, fmt.Sprintf("Diagnostics: %d warning", warnings))
		}
	}
	if len(parts) == 0 && len(st.Manifest.Capabilities) > 0 {
		caps := make([]string, 0, len(st.Manifest.Capabilities))
		for _, cap := range st.Manifest.Capabilities {
			caps = append(caps, cap)
		}
		parts = append(parts, strings.Join(caps, ", "))
	}
	return strings.Join(parts, "\n")
}

func (m *model) resetPluginsManagerMatches() {
	matches := make([]int, 0, len(m.pluginsManager.all))
	for i := range m.pluginsManager.all {
		matches = append(matches, i)
	}
	m.pluginsManager.matches = matches
	if len(matches) == 0 {
		m.pluginsManager.selected = 0
		return
	}
	if m.pluginsManager.selected < 0 {
		m.pluginsManager.selected = 0
	}
	if m.pluginsManager.selected >= len(matches) {
		m.pluginsManager.selected = len(matches) - 1
	}
}

func (m *model) handlePluginsManagerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c", "q", "backspace":
		if m.pluginsManager.detail {
			m.pluginsManager.detail = false
			m.pluginsManager.offset = 0
			return nil
		}
		m.mode = modeChat
		m.pluginsManager.matches = nil
		m.pluginsManager.selected = 0
		m.pluginsManager.detail = false
		m.pluginsManager.offset = 0
		m.status = "ready"
	case "enter":
		if len(m.pluginsManager.matches) > 0 {
			m.pluginsManager.detail = true
			m.pluginsManager.offset = 0
		}
	case "up", "k":
		if m.pluginsManager.detail {
			if m.pluginsManager.offset > 0 {
				m.pluginsManager.offset--
			}
		} else if len(m.pluginsManager.matches) > 0 {
			m.pluginsManager.selected = wrapSelection(m.pluginsManager.selected, len(m.pluginsManager.matches), -1)
		}
	case "down", "j":
		if m.pluginsManager.detail {
			m.pluginsManager.offset++
		} else if len(m.pluginsManager.matches) > 0 {
			m.pluginsManager.selected = wrapSelection(m.pluginsManager.selected, len(m.pluginsManager.matches), 1)
		}
	case " ", "space":
		if m.pluginsManager.detail {
			return nil
		}
		m.toggleSelectedManagedPlugin()
	}
	return nil
}

func (m *model) toggleSelectedManagedPlugin() {
	if m.pluginsManager.selected < 0 || m.pluginsManager.selected >= len(m.pluginsManager.matches) {
		return
	}
	idx := m.pluginsManager.matches[m.pluginsManager.selected]
	if idx < 0 || idx >= len(m.pluginsManager.all) {
		return
	}
	item := &m.pluginsManager.all[idx]
	item.Enabled = !item.Enabled
	item.Status.Enabled = item.Enabled
	m.setPluginSlashCommands(m.pluginManagerStatuses())
	m.dispatchIntent(protocol.Intent{Kind: protocol.IntentSetPluginEnabled, PluginID: item.ID, PluginEnabled: item.Enabled})
}

func (m *model) pluginManagerStatuses() []protocol.PluginStatus {
	statuses := make([]protocol.PluginStatus, 0, len(m.pluginsManager.all))
	for _, item := range m.pluginsManager.all {
		statuses = append(statuses, item.Status)
	}
	return statuses
}

func (m model) renderPluginsManager() string {
	if m.pluginsManager.detail {
		return m.renderPluginManagerDetail()
	}
	rows := []string{
		pickerTitle("Plugins"),
		pickerHint("Installed plugins"),
		"",
	}
	const maxRows = 8
	if len(m.pluginsManager.all) == 0 {
		rows = append(rows, pickerHint("  no plugins found"))
	} else {
		start := 0
		if len(m.pluginsManager.matches) > maxRows {
			start = m.pluginsManager.selected - maxRows/2
			if start < 0 {
				start = 0
			}
			if start > len(m.pluginsManager.matches)-maxRows {
				start = len(m.pluginsManager.matches) - maxRows
			}
		}
		end := len(m.pluginsManager.matches)
		if end > start+maxRows {
			end = start + maxRows
		}
		for visible := start; visible < end; visible++ {
			item := m.pluginsManager.all[m.pluginsManager.matches[visible]]
			rows = append(rows, renderPluginsManagerRow(item, visible == m.pluginsManager.selected, m.width)...)
		}
	}
	rows = append(rows, "", pickerHint("  ↑/↓ select · Enter details · Space enable/disable · Esc close"))
	return strings.Join(rows, "\n")
}

func (m model) renderPluginManagerDetail() string {
	item, ok := m.selectedManagedPlugin()
	if !ok {
		return m.renderPluginsManager()
	}
	rows := []string{
		pickerTitle("Plugins"),
		pickerHint("Plugin details"),
		"",
	}
	rows = append(rows, renderPluginDetailHeader(item, m.width)...)
	rows = append(rows, renderPluginDetailSections(item.Status, m.width)...)
	scrollable := false
	maxRows := pluginDetailMaxRows(m.height)
	if maxRows > 0 && len(rows) > maxRows {
		scrollable = true
		maxOffset := len(rows) - maxRows
		if m.pluginsManager.offset > maxOffset {
			m.pluginsManager.offset = maxOffset
		}
		if m.pluginsManager.offset < 0 {
			m.pluginsManager.offset = 0
		}
		rows = rows[m.pluginsManager.offset : m.pluginsManager.offset+maxRows]
	}
	hint := "  Esc back"
	if scrollable {
		hint = "  ↑/↓ scroll · Esc back"
	}
	rows = append(rows, "", pickerHint(hint))
	return strings.Join(rows, "\n")
}

func renderPluginsManagerRow(item pluginManagerItem, selected bool, width int) []string {
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	selector := muted.Render("  ")
	if selected {
		selector = lipgloss.NewStyle().Foreground(tuitheme.Default.InfoSoft).Bold(true).Render("> ")
	}

	marker := " "
	markerStyle := muted
	if item.Enabled {
		marker = "x"
		markerStyle = lipgloss.NewStyle().Foreground(tuitheme.Default.Success).Bold(true)
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
		nameStyle.Render(item.ID)

	details := []string{}
	if desc := strings.TrimSpace(item.Description); desc != "" {
		details = append(details, desc)
	}
	if usage := strings.TrimSpace(item.Usage); usage != "" {
		details = append(details, usage)
	}
	out := []string{head}
	if len(details) == 0 {
		return out
	}
	detailWidth := width - 8
	if detailWidth < 24 {
		detailWidth = 72
	}
	for _, detail := range details {
		for _, line := range wrapPluginDetailLine(detail, detailWidth) {
			out = append(out, muted.Render("      "+line))
		}
	}
	return out
}

func (m model) selectedManagedPlugin() (pluginManagerItem, bool) {
	if m.pluginsManager.selected < 0 || m.pluginsManager.selected >= len(m.pluginsManager.matches) {
		return pluginManagerItem{}, false
	}
	idx := m.pluginsManager.matches[m.pluginsManager.selected]
	if idx < 0 || idx >= len(m.pluginsManager.all) {
		return pluginManagerItem{}, false
	}
	return m.pluginsManager.all[idx], true
}

func renderPluginDetailHeader(item pluginManagerItem, width int) []string {
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	state := "disabled"
	stateStyle := muted
	if item.Enabled {
		state = "enabled"
		stateStyle = lipgloss.NewStyle().Foreground(tuitheme.Default.Success).Bold(true)
	}
	rows := []string{
		lipgloss.NewStyle().Bold(true).Render(item.ID) + muted.Render(" · ") + stateStyle.Render(state),
	}
	if name := strings.TrimSpace(item.Name); name != "" && name != item.ID {
		rows = append(rows, muted.Render("name: ")+name)
	}
	if version := strings.TrimSpace(item.Status.Manifest.Version); version != "" {
		rows = append(rows, muted.Render("version: ")+version)
	}
	if desc := strings.TrimSpace(item.Description); desc != "" {
		rows = append(rows, wrapPluginDetailBlock("description", desc, width)...)
	}
	return append(rows, "")
}

func renderPluginDetailSections(st protocol.PluginStatus, width int) []string {
	var rows []string
	rows = append(rows, renderPluginCommandSection(st.Commands, width)...)
	rows = append(rows, renderPluginStringSection("Tools", st.Tools, width)...)
	rows = append(rows, renderPluginStringSection("Skills", st.Skills, width)...)
	rows = append(rows, renderPluginStringSection("Agents", st.Agents, width)...)
	rows = append(rows, renderPluginStringSection("Rules", st.Rules, width)...)
	rows = append(rows, renderPluginHookSection(st.Hooks, width)...)
	rows = append(rows, renderPluginServiceSection(st.Services, width)...)
	rows = append(rows, renderPluginDiagnosticSection(st.Diagnostics, width)...)
	rows = append(rows, renderPluginPathSection(st.Paths, width)...)
	return rows
}

func renderPluginCommandSection(commands []protocol.PluginCommand, width int) []string {
	if len(commands) == 0 {
		return nil
	}
	var lines []string
	for _, cmd := range commands {
		name := strings.TrimSpace(cmd.Name)
		if name == "" {
			continue
		}
		detail := name
		if usage := strings.TrimSpace(cmd.Usage); usage != "" && usage != name {
			detail += " " + usage
		}
		if desc := strings.TrimSpace(cmd.Description); desc != "" {
			detail += " - " + desc
		}
		lines = append(lines, detail)
	}
	return renderPluginSection("Commands", lines, width)
}

func renderPluginStringSection(title string, values []string, width int) []string {
	if len(values) == 0 {
		return nil
	}
	var lines []string
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return renderPluginSection(title, lines, width)
}

func renderPluginHookSection(hooks []string, width int) []string {
	return renderPluginStringSection("Hooks", hooks, width)
}

func renderPluginServiceSection(services []protocol.PluginService, width int) []string {
	if len(services) == 0 {
		return nil
	}
	var lines []string
	for _, svc := range services {
		name := strings.TrimSpace(svc.Name)
		if name == "" {
			continue
		}
		line := name
		if status := strings.TrimSpace(svc.Status); status != "" {
			line += " · " + status
		}
		if detail := strings.TrimSpace(svc.Detail); detail != "" {
			line += " · " + detail
		}
		lines = append(lines, line)
	}
	return renderPluginSection("Services", lines, width)
}

func renderPluginDiagnosticSection(diags []protocol.PluginDiagnostic, width int) []string {
	if len(diags) == 0 {
		return nil
	}
	var lines []string
	for _, diag := range diags {
		label := strings.TrimSpace(diag.Label)
		if label == "" {
			label = "diagnostic"
		}
		line := strings.TrimSpace(diag.Level)
		if line != "" {
			line += " "
		}
		line += label
		if detail := strings.TrimSpace(diag.Detail); detail != "" {
			line += ": " + detail
		}
		lines = append(lines, line)
	}
	return renderPluginSection("Diagnostics", lines, width)
}

func renderPluginPathSection(paths map[string]string, width int) []string {
	if len(paths) == 0 {
		return nil
	}
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		if value := strings.TrimSpace(paths[key]); value != "" {
			lines = append(lines, key+": "+value)
		}
	}
	return renderPluginSection("Paths", lines, width)
}

func renderPluginSection(title string, lines []string, width int) []string {
	if len(lines) == 0 {
		return nil
	}
	rows := []string{lipgloss.NewStyle().Bold(true).Render(title)}
	for _, line := range lines {
		rows = append(rows, wrapPluginDetailBlock("-", line, width)...)
	}
	return append(rows, "")
}

func wrapPluginDetailBlock(prefix, text string, width int) []string {
	muted := lipgloss.NewStyle().Foreground(tuitheme.Default.Muted)
	detailWidth := width - 6
	if detailWidth < 24 {
		detailWidth = 72
	}
	wrapped := wrapPluginDetailLine(text, detailWidth)
	if len(wrapped) == 0 {
		return nil
	}
	out := make([]string, 0, len(wrapped))
	firstPrefix := prefix
	if firstPrefix != "" {
		firstPrefix += " "
	}
	for i, line := range wrapped {
		if i == 0 {
			out = append(out, muted.Render(firstPrefix)+line)
			continue
		}
		out = append(out, muted.Render(strings.Repeat(" ", len(firstPrefix)))+line)
	}
	return out
}

func pluginDetailMaxRows(height int) int {
	if height <= 8 {
		return 0
	}
	return height - 2
}

func wrapPluginDetailLine(text string, width int) []string {
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
