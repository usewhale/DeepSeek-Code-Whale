package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/plugins"
)

func (a *App) HookEntries() []agent.HookListEntry {
	if a == nil || a.hookRunner == nil {
		return nil
	}
	return a.hookRunner.ListHooks()
}

func (a *App) buildHooksLocalResult() *LocalResult {
	entries := a.HookEntries()
	byEvent := hookCountsByEvent(entries)
	rows := []string{"Event                 Installed   Active      Review      Description"}
	reviewTotal := 0
	for _, info := range agent.HookEvents() {
		counts := byEvent[info.Event]
		reviewTotal += counts.Review
		rows = append(rows, fmt.Sprintf("%-20s  %-10d  %-10d  %-10d  %s", info.Event, counts.Installed, counts.Active, counts.Review, info.Description))
	}
	plain := "Hooks\nLifecycle hooks from config and enabled plugins.\n\n" + strings.Join(rows, "\n")
	if reviewTotal > 0 {
		plain += fmt.Sprintf("\n\n%d hook(s) need review before they can run. Run `/hooks trust all` to trust current hooks.", reviewTotal)
	}
	sections := []LocalResultSection{{
		Title: "Lifecycle hooks from config and enabled plugins.",
		Fields: []LocalResultField{{
			Label: "Event",
			Value: strings.Join(rows[1:], "\n"),
		}},
	}}
	if len(entries) > 0 {
		sections = append(sections, hookDetailSections(entries)...)
	}
	actions := []LocalResultAction{}
	if reviewTotal > 0 {
		actions = append(actions, LocalResultAction{Label: "Trust all", Command: "/hooks trust all", Tone: "warn", Description: "Trust the current hashes for all review hooks"})
	}
	return &LocalResult{
		Kind:      "hooks",
		Title:     "Hooks",
		Sections:  sections,
		Actions:   actions,
		PlainText: plain,
	}
}

func (a *App) HooksLocalResult() *LocalResult {
	return a.buildHooksLocalResult()
}

func (a *App) HooksNeedReview() bool {
	for _, entry := range a.HookEntries() {
		if agent.HookNeedsReview(entry) {
			return true
		}
	}
	return false
}

func (a *App) TrustHooks(keys []string) (string, error) {
	if a == nil || a.hookRunner == nil {
		return "", fmt.Errorf("hooks unavailable")
	}
	entries := a.hookRunner.ListHooks()
	next := agent.TrustHookStates(entries, a.hookStates, keys)
	if err := SaveHookStates(a.cfg.DataDir, a.workspaceRoot, next); err != nil {
		return "", err
	}
	a.hookStates = next
	return a.rebuildHookRunner(fmt.Sprintf("trusted %d hook(s)\nstate: %s", countTrustedHooks(entries, keys), HookStatePath(a.cfg.DataDir, a.workspaceRoot)))
}

func (a *App) SetHookEnabled(keys []string, enabled bool) (string, error) {
	if a == nil || a.hookRunner == nil {
		return "", fmt.Errorf("hooks unavailable")
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("missing hook key")
	}
	entries := a.hookRunner.ListHooks()
	next := agent.SetHookEnabledStates(entries, a.hookStates, keys, enabled)
	if err := SaveHookStates(a.cfg.DataDir, a.workspaceRoot, next); err != nil {
		return "", err
	}
	a.hookStates = next
	action := "enabled"
	if !enabled {
		action = "disabled"
	}
	return a.rebuildHookRunner(fmt.Sprintf("%s %d hook(s)\nstate: %s", action, countSelectedHooks(entries, keys), HookStatePath(a.cfg.DataDir, a.workspaceRoot)))
}

func (a *App) rebuildHookRunner(message string) (string, error) {
	pm := a.pluginManager
	if pm == nil {
		pm = plugins.NewManager(plugins.Context{DataDir: a.cfg.DataDir, WorkspaceRoot: a.workspaceRoot}, a.cfg.Plugins)
	}
	outcome := pm.Outcome()
	allHooks := append([]agent.ResolvedHook{}, a.hooks...)
	allHooks = append(allHooks, outcome.CommandHooks...)
	hookRunner := agent.NewHookRunnerWithState(allHooks, a.workspaceRoot, a.hookStates)
	hookRunner.AddHandlers(outcome.HookHandlers...)
	a.toolMu.Lock()
	a.hookRunner = hookRunner
	a.toolMu.Unlock()
	a.resetAgent()
	return message, nil
}

type hookEventCounts struct {
	Installed int
	Active    int
	Review    int
}

func hookCountsByEvent(entries []agent.HookListEntry) map[agent.HookEvent]hookEventCounts {
	out := map[agent.HookEvent]hookEventCounts{}
	for _, entry := range entries {
		counts := out[entry.Event]
		counts.Installed++
		if entry.Active {
			counts.Active++
		}
		if agent.HookNeedsReview(entry) {
			counts.Review++
		}
		out[entry.Event] = counts
	}
	return out
}

func hookDetailSections(entries []agent.HookListEntry) []LocalResultSection {
	byEvent := map[agent.HookEvent][]agent.HookListEntry{}
	for _, entry := range entries {
		byEvent[entry.Event] = append(byEvent[entry.Event], entry)
	}
	out := []LocalResultSection{}
	for _, info := range agent.HookEvents() {
		list := byEvent[info.Event]
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		fields := make([]LocalResultField, 0, len(list))
		for _, entry := range list {
			tone := "muted"
			if entry.Active {
				tone = "info"
			}
			if agent.HookNeedsReview(entry) {
				tone = "warn"
			}
			fields = append(fields, LocalResultField{
				Label: entry.Key,
				Value: hookDetailLine(entry),
				Tone:  tone,
			})
		}
		out = append(out, LocalResultSection{Title: string(info.Event), Fields: fields})
	}
	return out
}

func hookDetailLine(entry agent.HookListEntry) string {
	name := core.FirstNonEmpty(entry.Name, entry.Command, entry.Source)
	parts := []string{name, "source=" + entry.Source, "trust=" + string(entry.Trust)}
	if entry.Match != "" {
		parts = append(parts, "match="+entry.Match)
	}
	if entry.Command != "" {
		parts = append(parts, "command="+entry.Command)
	}
	if !entry.Active {
		parts = append(parts, "inactive")
	}
	return strings.Join(parts, " · ")
}

func countTrustedHooks(entries []agent.HookListEntry, keys []string) int {
	selected := map[string]bool{}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			selected[key] = true
		}
	}
	all := len(selected) == 0
	count := 0
	for _, entry := range entries {
		if entry.Managed || strings.TrimSpace(entry.Hash) == "" {
			continue
		}
		if all || selected[entry.Key] {
			count++
		}
	}
	return count
}

func countSelectedHooks(entries []agent.HookListEntry, keys []string) int {
	selected := map[string]bool{}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			selected[key] = true
		}
	}
	count := 0
	for _, entry := range entries {
		if selected[entry.Key] {
			count++
		}
	}
	return count
}
