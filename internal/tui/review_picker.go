package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type reviewTargetPickerState struct {
	kind          string
	selected      int
	loading       bool
	err           string
	query         string
	defaultBranch string
	branches      []reviewBranchItem
	commits       []reviewCommitItem
	prs           []reviewPRItem
}

type reviewBranchItem struct {
	Name    string
	Current bool
}

type reviewCommitItem struct {
	SHA     string
	Subject string
	Author  string
	When    string
}

type reviewPRItem struct {
	Number int
	Title  string
	Head   string
	Author string
}

type reviewCommitsLoadedMsg struct {
	items []reviewCommitItem
	err   string
}

type reviewBranchesLoadedMsg struct {
	items         []reviewBranchItem
	defaultBranch string
	err           string
}

type reviewPRsLoadedMsg struct {
	items []reviewPRItem
	err   string
}

func (m *model) openReviewTargetPicker(kind string) tea.Cmd {
	m.reviewTargetPicker.kind = kind
	m.reviewTargetPicker.selected = 0
	m.reviewTargetPicker.loading = true
	m.reviewTargetPicker.err = ""
	m.reviewTargetPicker.query = ""
	m.reviewTargetPicker.defaultBranch = ""
	m.reviewTargetPicker.branches = nil
	m.reviewTargetPicker.commits = nil
	m.reviewTargetPicker.prs = nil
	switch kind {
	case "branch":
		m.mode = modeReviewBranchPicker
		m.status = "review branch"
		return loadReviewBranchesCmd(m.cwdPath)
	case "commit":
		m.mode = modeReviewCommitPicker
		m.status = "review commit"
		return loadReviewCommitsCmd(m.cwdPath)
	case "pr":
		m.mode = modeReviewPRPicker
		m.status = "review pr"
		return loadReviewPRsCmd(m.cwdPath)
	default:
		m.closeReviewMenu()
		return nil
	}
}

func (m *model) handleReviewTargetPickerKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeReviewMenu
		m.status = "review"
		return nil
	case "backspace", "ctrl+h":
		if m.mode == modeReviewBranchPicker && m.reviewTargetPicker.query != "" {
			runes := []rune(m.reviewTargetPicker.query)
			m.reviewTargetPicker.query = string(runes[:len(runes)-1])
			m.reviewTargetPicker.selected = min(m.reviewTargetPicker.selected, max(0, m.reviewTargetPickerChoiceCount()-1))
			return nil
		}
	case "up", "k":
		m.reviewTargetPicker.selected = wrapSelection(m.reviewTargetPicker.selected, m.reviewTargetPickerChoiceCount(), -1)
	case "down", "j":
		m.reviewTargetPicker.selected = wrapSelection(m.reviewTargetPicker.selected, m.reviewTargetPickerChoiceCount(), 1)
	case "enter":
		return m.submitSelectedReviewTarget()
	case "/":
		return m.prefillReviewTarget("")
	default:
		if msg.Type == tea.KeyRunes {
			if m.mode == modeReviewBranchPicker {
				m.reviewTargetPicker.query += string(msg.Runes)
				m.reviewTargetPicker.selected = 0
				return nil
			}
			return m.prefillReviewTarget(string(msg.Runes))
		}
	}
	return nil
}

func (m *model) handleReviewCommitsLoaded(msg reviewCommitsLoadedMsg) {
	if m.mode != modeReviewCommitPicker {
		return
	}
	m.reviewTargetPicker.loading = false
	m.reviewTargetPicker.err = strings.TrimSpace(msg.err)
	m.reviewTargetPicker.commits = msg.items
	if m.reviewTargetPicker.selected >= m.reviewTargetPickerChoiceCount() {
		m.reviewTargetPicker.selected = max(0, m.reviewTargetPickerChoiceCount()-1)
	}
	m.refreshViewportContent()
}

func (m *model) handleReviewPRsLoaded(msg reviewPRsLoadedMsg) {
	if m.mode != modeReviewPRPicker {
		return
	}
	m.reviewTargetPicker.loading = false
	m.reviewTargetPicker.err = strings.TrimSpace(msg.err)
	m.reviewTargetPicker.prs = msg.items
	if m.reviewTargetPicker.selected >= m.reviewTargetPickerChoiceCount() {
		m.reviewTargetPicker.selected = max(0, m.reviewTargetPickerChoiceCount()-1)
	}
	m.refreshViewportContent()
}

func (m *model) handleReviewBranchesLoaded(msg reviewBranchesLoadedMsg) {
	if m.mode != modeReviewBranchPicker {
		return
	}
	m.reviewTargetPicker.loading = false
	m.reviewTargetPicker.err = strings.TrimSpace(msg.err)
	m.reviewTargetPicker.defaultBranch = strings.TrimSpace(msg.defaultBranch)
	m.reviewTargetPicker.branches = msg.items
	if m.reviewTargetPicker.selected >= m.reviewTargetPickerChoiceCount() {
		m.reviewTargetPicker.selected = max(0, m.reviewTargetPickerChoiceCount()-1)
	}
	m.refreshViewportContent()
}

func (m model) reviewTargetPickerChoiceCount() int {
	switch m.mode {
	case modeReviewBranchPicker:
		return len(m.filteredReviewBranches()) + 1
	case modeReviewCommitPicker:
		return len(m.reviewTargetPicker.commits) + 1
	case modeReviewPRPicker:
		return len(m.reviewTargetPicker.prs) + 1
	default:
		return 0
	}
}

func (m *model) submitSelectedReviewTarget() tea.Cmd {
	switch m.mode {
	case modeReviewBranchPicker:
		branches := m.filteredReviewBranches()
		if m.reviewTargetPicker.selected < len(branches) {
			item := branches[m.reviewTargetPicker.selected]
			if strings.TrimSpace(item.Name) != "" {
				m.closeReviewMenu()
				return m.submitPrompt("/review branch " + item.Name)
			}
		}
		return m.prefillReviewTarget("")
	case modeReviewCommitPicker:
		if m.reviewTargetPicker.selected < len(m.reviewTargetPicker.commits) {
			item := m.reviewTargetPicker.commits[m.reviewTargetPicker.selected]
			if strings.TrimSpace(item.SHA) != "" {
				m.closeReviewMenu()
				return m.submitPrompt("/review commit " + item.SHA)
			}
		}
		return m.prefillReviewTarget("")
	case modeReviewPRPicker:
		if m.reviewTargetPicker.selected < len(m.reviewTargetPicker.prs) {
			item := m.reviewTargetPicker.prs[m.reviewTargetPicker.selected]
			if item.Number > 0 {
				m.closeReviewMenu()
				return m.submitPrompt("/review pr " + strconv.Itoa(item.Number))
			}
		}
		return m.prefillReviewTarget("")
	default:
		return nil
	}
}

func (m *model) prefillReviewTarget(suffix string) tea.Cmd {
	prefix := "/review "
	switch m.mode {
	case modeReviewBranchPicker:
		prefix = "/review branch "
	case modeReviewCommitPicker:
		prefix = "/review commit "
	case modeReviewPRPicker:
		prefix = "/review pr "
	}
	m.closeReviewMenu()
	m.input.SetValue(prefix + suffix)
	m.skillBinding = nil
	m.resetHistoryNavigation()
	cmd := m.updateSlashMatches()
	m.refreshViewportContent()
	return cmd
}

func (m model) filteredReviewBranches() []reviewBranchItem {
	query := strings.ToLower(strings.TrimSpace(m.reviewTargetPicker.query))
	out := make([]reviewBranchItem, 0, len(m.reviewTargetPicker.branches))
	seen := map[string]bool{}
	if query == "" {
		if def := strings.TrimSpace(m.reviewTargetPicker.defaultBranch); def != "" && def != m.currentReviewBranch() {
			out = append(out, reviewBranchItem{Name: def})
			seen[def] = true
		}
	}
	for _, item := range m.reviewTargetPicker.branches {
		if item.Current {
			continue
		}
		if seen[item.Name] {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(item.Name), query) {
			out = append(out, item)
			seen[item.Name] = true
		}
	}
	return out
}

func (m model) currentReviewBranch() string {
	for _, item := range m.reviewTargetPicker.branches {
		if item.Current {
			return item.Name
		}
	}
	if strings.TrimSpace(m.gitBranch) != "" {
		return strings.TrimSpace(m.gitBranch)
	}
	return "HEAD"
}
