package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const maxSearchResults = 5

// DeferredToolMeta is the lightweight metadata stored for each deferred tool.
//
// Keep in sync with tools.DeferredToolEntry and the adapter in
// app.mcpCatalogAdapter.Search.
type DeferredToolMeta struct {
	Name        string // qualified name, e.g. mcp__github__search_code
	Server      string // MCP server name
	Description string // one-line description
}

// DeferredToolCatalog is an immutable, searchable catalog of deferred MCP tools.
type DeferredToolCatalog struct {
	tools []DeferredToolMeta
	hash  string
}

// NewDeferredToolCatalog builds a catalog from discovered tool metadata.
func NewDeferredToolCatalog(tools []DeferredToolMeta) *DeferredToolCatalog {
	sorted := append([]DeferredToolMeta(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Server != sorted[j].Server {
			return sorted[i].Server < sorted[j].Server
		}
		return sorted[i].Name < sorted[j].Name
	})
	c := &DeferredToolCatalog{tools: sorted}
	c.hash = computeCatalogHash(sorted)
	return c
}

// Empty returns true when there are no deferred tools.
func (c *DeferredToolCatalog) Empty() bool {
	return c == nil || len(c.tools) == 0
}

// Hash returns the catalog hash for version checking.
func (c *DeferredToolCatalog) Hash() string {
	if c == nil {
		return ""
	}
	return c.hash
}

// Names returns the list of deferred tool names.
func (c *DeferredToolCatalog) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.tools))
	for i, t := range c.tools {
		out[i] = t.Name
	}
	return out
}

// Tools returns a copy of all deferred tool metadata.
func (c *DeferredToolCatalog) Tools() []DeferredToolMeta {
	if c == nil {
		return nil
	}
	return append([]DeferredToolMeta(nil), c.tools...)
}

// ByServer groups deferred tool names by server, preserving sorted order.
func (c *DeferredToolCatalog) ByServer() map[string][]DeferredToolMeta {
	if c == nil {
		return nil
	}
	out := make(map[string][]DeferredToolMeta, len(c.tools))
	for _, t := range c.tools {
		out[t.Server] = append(out[t.Server], t)
	}
	return out
}

// Search finds tools matching a query. Three query forms are supported:
//
//	select:NameA,NameB  — exact name lookup
//	keyword phrase      — case-insensitive regex on name + description
//	+must_have extras   — name must contain must_have; extras score
//
// Returns at most maxSearchResults matches, sorted by relevance.
func (c *DeferredToolCatalog) Search(query string) []DeferredToolMeta {
	if c == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	query = strings.TrimSpace(query)

	// select:Name1,Name2,...
	if rest, ok := strings.CutPrefix(query, "select:"); ok {
		return c.selectSearch(rest)
	}

	// +must_have extra terms...
	if strings.HasPrefix(query, "+") {
		return c.mustHaveSearch(query)
	}

	// default: keyword/regex search
	return c.regexSearch(query)
}

func (c *DeferredToolCatalog) selectSearch(names string) []DeferredToolMeta {
	wanted := make(map[string]bool)
	for _, name := range strings.Split(names, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			wanted[name] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	var results []DeferredToolMeta
	for _, t := range c.tools {
		if wanted[t.Name] {
			results = append(results, t)
		}
	}
	return results
}

func (c *DeferredToolCatalog) mustHaveSearch(query string) []DeferredToolMeta {
	// Parse: +must_have term1 term2 ...
	parts := strings.Fields(query)
	if len(parts) == 0 {
		return nil
	}
	mustHave := strings.TrimPrefix(parts[0], "+")
	mustHave = strings.ToLower(mustHave)
	extras := parts[1:]

	type scored struct {
		tool  DeferredToolMeta
		score int
	}
	var results []scored

	for _, t := range c.tools {
		nameLower := strings.ToLower(t.Name)
		descLower := strings.ToLower(t.Description)
		if !strings.Contains(nameLower, mustHave) {
			continue
		}
		score := 2 // name matched mustHave
		for _, term := range extras {
			term = strings.ToLower(term)
			if strings.Contains(nameLower, term) {
				score += 2
			} else if strings.Contains(descLower, term) {
				score++
			}
		}
		results = append(results, scored{tool: t, score: score})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
	if len(results) > maxSearchResults {
		results = results[:maxSearchResults]
	}
	out := make([]DeferredToolMeta, len(results))
	for i, r := range results {
		out[i] = r.tool
	}
	return out
}

func (c *DeferredToolCatalog) regexSearch(query string) []DeferredToolMeta {
	type scored struct {
		tool  DeferredToolMeta
		score int
	}
	pat := compileCatalogRegex(query)
	var results []scored

	for _, t := range c.tools {
		var score int
		if pat.MatchString(t.Name) {
			score = 2
		} else if pat.MatchString(t.Description) {
			score = 1
		}
		if score > 0 {
			results = append(results, scored{tool: t, score: score})
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
	if len(results) > maxSearchResults {
		results = results[:maxSearchResults]
	}
	out := make([]DeferredToolMeta, len(results))
	for i, r := range results {
		out[i] = r.tool
	}
	return out
}

// compileCatalogRegex compiles a case-insensitive regex from pattern.
// If pattern contains invalid regex syntax, metacharacters are escaped
// so the query is treated as a literal search.
func compileCatalogRegex(pattern string) *regexp.Regexp {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		// Pattern has invalid regex syntax; escape metacharacters for literal match.
		re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pattern))
	}
	return re
}

func computeCatalogHash(tools []DeferredToolMeta) string {
	payload := make([]map[string]string, len(tools))
	for i, t := range tools {
		payload[i] = map[string]string{
			"name":        t.Name,
			"server":      t.Server,
			"description": t.Description,
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "error"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RenderAvailableDeferredTools returns the <available-deferred-tools> block
// listing deferred tools grouped by server.
func RenderAvailableDeferredTools(catalog *DeferredToolCatalog) string {
	if catalog == nil || catalog.Empty() {
		return ""
	}
	byServer := catalog.ByServer()
	serverNames := make([]string, 0, len(byServer))
	for s := range byServer {
		serverNames = append(serverNames, s)
	}
	sort.Strings(serverNames)

	var b strings.Builder
	b.WriteString("<available-deferred-tools>\n")
	for _, srv := range serverNames {
		tools := byServer[srv]
		fmt.Fprintf(&b, "[server: %s]\n", srv)
		for _, t := range tools {
			fmt.Fprintf(&b, "  %s — %s\n", t.Name, t.Description)
		}
	}
	b.WriteString("</available-deferred-tools>")
	return b.String()
}
