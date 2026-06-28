package app

import (
	"github.com/usewhale/whale/internal/skills"
	"sort"
	"strings"
)

func (a *App) reloadSkillDisabledConfig() error {
	if a == nil {
		return nil
	}
	loaded, err := LoadConfigFiles(a.cfg.DataDir, a.workspaceRoot)
	if err != nil {
		return err
	}
	cfg := Config{}
	if err := ApplyLoadedConfig(&cfg, loaded); err != nil {
		return err
	}
	a.cfg.SkillsDisabled = trimList(cfg.SkillsDisabled)
	if a.toolset != nil {
		a.toolset.SetSkillDisabled(a.cfg.SkillsDisabled)
	}
	a.resetAgent()
	return nil
}

func (a *App) reloadPluginConfig() error {
	if a == nil {
		return nil
	}
	loaded, err := LoadConfigFiles(a.cfg.DataDir, a.workspaceRoot)
	if err != nil {
		return err
	}
	cfg := Config{}
	if err := ApplyLoadedConfig(&cfg, loaded); err != nil {
		return err
	}
	a.cfg.Plugins = clonePluginConfigMap(cfg.Plugins)
	return nil
}

func reportHasSkill(report skills.Report, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, view := range allReportSkills(report) {
		if strings.ToLower(view.Name) == name {
			return true
		}
	}
	return false
}

func reportSkillNames(report skills.Report) []string {
	seen := map[string]bool{}
	var out []string
	for _, view := range allReportSkills(report) {
		name := strings.TrimSpace(view.Name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func allReportSkills(report skills.Report) []skills.SkillView {
	var out []skills.SkillView
	out = append(out, report.Ready...)
	out = append(out, report.NeedsSetup...)
	out = append(out, report.Disabled...)
	out = append(out, report.Problems...)
	return out
}

func disabledNameSet(names []string) map[string]string {
	out := map[string]string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[strings.ToLower(name)] = name
	}
	return out
}

func sortedSkillNames(names map[string]string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func mergeNames(existing, add []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(add))
	for _, name := range existing {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	for _, name := range add {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

func removeNames(names, remove []string) []string {
	removeSet := disabledNameSet(remove)
	if len(removeSet) == 0 {
		return trimList(names)
	}
	var out []string
	seen := map[string]bool{}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := removeSet[key]; ok || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

func parseSkillMention(line string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "$") {
		return "", "", false
	}
	head := trimmed
	if idx := strings.IndexAny(trimmed, " \t\n"); idx >= 0 {
		head = trimmed[:idx]
		args = strings.TrimSpace(trimmed[idx:])
	}
	name = strings.TrimPrefix(head, "$")
	if !skills.ValidName(name) {
		return "", "", false
	}
	return name, args, true
}

func parseCSVList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
