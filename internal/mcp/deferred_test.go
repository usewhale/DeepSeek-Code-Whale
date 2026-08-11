package mcp

import (
	"strings"
	"testing"
)

func testCatalogMeta(name, server, desc string) DeferredToolMeta {
	return DeferredToolMeta{Name: name, Server: server, Description: desc}
}

func TestNewDeferredToolCatalogSortsByServerThenName(t *testing.T) {
	tools := []DeferredToolMeta{
		testCatalogMeta("mcp__z__b", "z", "z b"),
		testCatalogMeta("mcp__a__c", "a", "a c"),
		testCatalogMeta("mcp__a__a", "a", "a a"),
	}
	c := NewDeferredToolCatalog(tools)

	names := c.Names()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
	if names[0] != "mcp__a__a" || names[1] != "mcp__a__c" || names[2] != "mcp__z__b" {
		t.Fatalf("unexpected order: %v", names)
	}
}

func TestNewDeferredToolCatalogEmpty(t *testing.T) {
	c := NewDeferredToolCatalog(nil)
	if !c.Empty() {
		t.Fatal("nil input should be empty")
	}

	c = NewDeferredToolCatalog([]DeferredToolMeta{})
	if !c.Empty() {
		t.Fatal("empty slice should be empty")
	}
}

func TestDeferredToolCatalogNilReceiver(t *testing.T) {
	var c *DeferredToolCatalog
	if !c.Empty() {
		t.Fatal("nil catalog should be empty")
	}
	if c.Names() != nil {
		t.Fatal("nil catalog Names should be nil")
	}
	if c.Tools() != nil {
		t.Fatal("nil catalog Tools should be nil")
	}
	if c.ByServer() != nil {
		t.Fatal("nil catalog ByServer should be nil")
	}
	if c.Search("anything") != nil {
		t.Fatal("nil catalog Search should be nil")
	}
	if c.Hash() != "" {
		t.Fatal("nil catalog Hash should be empty string")
	}
}

func TestDeferredToolCatalogHashDeterministic(t *testing.T) {
	tools := []DeferredToolMeta{
		testCatalogMeta("mcp__s__x", "s", "x desc"),
		testCatalogMeta("mcp__s__y", "s", "y desc"),
	}
	c1 := NewDeferredToolCatalog(tools)
	c2 := NewDeferredToolCatalog(tools)

	if c1.Hash() != c2.Hash() {
		t.Fatalf("hash should be deterministic: %q != %q", c1.Hash(), c2.Hash())
	}
	if c1.Hash() == "" {
		t.Fatal("hash should not be empty")
	}
}

func TestDeferredToolCatalogHashChangesWithContent(t *testing.T) {
	c1 := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__a", "s", "desc a"),
	})
	c2 := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__b", "s", "desc b"),
	})

	if c1.Hash() == c2.Hash() {
		t.Fatal("different tools should have different hashes")
	}
}

func TestDeferredToolCatalogToolsReturnsCopy(t *testing.T) {
	tools := []DeferredToolMeta{
		testCatalogMeta("mcp__s__x", "s", "x"),
	}
	c := NewDeferredToolCatalog(tools)
	cp := c.Tools()
	cp[0] = DeferredToolMeta{Name: "mutated"}
	if c.Tools()[0].Name != "mcp__s__x" {
		t.Fatal("Tools() should return a copy")
	}
}

func TestDeferredToolCatalogByServerGroups(t *testing.T) {
	tools := []DeferredToolMeta{
		testCatalogMeta("mcp__s1__a", "s1", "a"),
		testCatalogMeta("mcp__s2__b", "s2", "b"),
		testCatalogMeta("mcp__s1__c", "s1", "c"),
	}
	c := NewDeferredToolCatalog(tools)
	byServer := c.ByServer()
	if len(byServer) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(byServer))
	}
	if len(byServer["s1"]) != 2 {
		t.Fatalf("s1 should have 2 tools, got %d", len(byServer["s1"]))
	}
	if len(byServer["s2"]) != 1 {
		t.Fatalf("s2 should have 1 tool, got %d", len(byServer["s2"]))
	}
	// Order within server should be preserved (sorted).
	if byServer["s1"][0].Name != "mcp__s1__a" || byServer["s1"][1].Name != "mcp__s1__c" {
		t.Fatalf("tools within s1 out of order: %v", byServer["s1"])
	}
}

// --- Search tests ---

func TestSearchSelectForm(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__alpha", "s", "first tool"),
		testCatalogMeta("mcp__s__beta", "s", "second tool"),
		testCatalogMeta("mcp__s__gamma", "s", "third tool"),
	})

	results := c.Search("select:mcp__s__alpha,mcp__s__gamma")
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Name != "mcp__s__alpha" {
		t.Fatalf("expected alpha, got %s", results[0].Name)
	}
	if results[1].Name != "mcp__s__gamma" {
		t.Fatalf("expected gamma, got %s", results[1].Name)
	}
}

func TestSearchSelectFormUnknownName(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__a", "s", "a"),
	})
	results := c.Search("select:nonexistent")
	if len(results) != 0 {
		t.Fatalf("expected 0 results for unknown name, got %d", len(results))
	}
}

func TestSearchSelectFormEmptyNames(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__a", "s", "a"),
	})
	results := c.Search("select:,,,")
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty names, got %d", len(results))
	}
}

func TestSearchSelectFormNoCap(t *testing.T) {
	// select: queries should not be capped at maxSearchResults — the user
	// explicitly requested those names, so all should be returned.
	tools := make([]DeferredToolMeta, 0, maxSearchResults+5)
	for i := range maxSearchResults + 5 {
		tools = append(tools, testCatalogMeta(
			"mcp__s__tool_"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			"s",
			"",
		))
	}
	c := NewDeferredToolCatalog(tools)
	// Build a select: query with all tool names.
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	results := c.Search("select:" + strings.Join(names, ","))
	if len(results) != len(tools) {
		t.Fatalf("select should return all %d requested tools, got %d", len(tools), len(results))
	}
}

func TestSearchMustHaveForm(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__github__search_code", "github", "search code in repos"),
		testCatalogMeta("mcp__github__list_issues", "github", "list issues"),
		testCatalogMeta("mcp__slack__post_message", "slack", "post message"),
	})

	// Match by server prefix
	results := c.Search("+github")
	if len(results) != 2 {
		t.Fatalf("expected 2 results for +github, got %d", len(results))
	}
	for _, r := range results {
		if r.Server != "github" {
			t.Fatalf("unexpected server: %s", r.Server)
		}
	}
}

func TestSearchMustHaveWithExtras(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__github__search_code", "github", "search code in repos"),
		testCatalogMeta("mcp__github__list_issues", "github", "list issues"),
		testCatalogMeta("mcp__github__search_issues", "github", "search issues"),
	})

	// +github search — should score search_code and search_issues higher than list_issues
	results := c.Search("+github search")
	if len(results) == 0 {
		t.Fatal("expected results for +github search")
	}
	// First results should have "search" in name
	if !strings.Contains(results[0].Name, "search") {
		t.Fatalf("expected search-scored tool first, got %s", results[0].Name)
	}
}

func TestSearchMustHaveNoMatch(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__a", "s", "a"),
	})
	results := c.Search("+nonexistent")
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchRegexForm(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__alpha", "s", "first tool"),
		testCatalogMeta("mcp__s__beta", "s", "second tool"),
		testCatalogMeta("mcp__s__gamma", "s", "third tool"),
	})

	results := c.Search("alpha")
	if len(results) != 1 || results[0].Name != "mcp__s__alpha" {
		t.Fatalf("expected alpha only, got %v", results)
	}
}

func TestSearchRegexFormCaseInsensitive(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__Alpha", "s", "first tool"),
	})

	results := c.Search("alpha")
	if len(results) != 1 || results[0].Name != "mcp__s__Alpha" {
		t.Fatalf("case-insensitive search failed: %v", results)
	}
}

func TestSearchRegexFallbackLiteralSubstring(t *testing.T) {
	// A regex pattern with an unmatched parenthesis triggers the fallback
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__alpha(", "s", "open paren tool"),
		testCatalogMeta("mcp__s__beta", "s", "beta tool"),
	})
	// Invalid regex: unmatched paren
	results := c.Search("alpha(")
	if len(results) != 1 || results[0].Name != "mcp__s__alpha(" {
		t.Fatalf("expected fallback substring match, got %v", results)
	}
}

func TestSearchMatchInDescription(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__t1", "s", "search repositories"),
		testCatalogMeta("mcp__s__t2", "s", "other"),
	})

	results := c.Search("repositor")
	if len(results) != 1 || results[0].Name != "mcp__s__t1" {
		t.Fatalf("expected description match, got %v", results)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__s__a", "s", "a"),
	})
	results := c.Search("")
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty query, got %d", len(results))
	}
	results = c.Search("   ")
	if len(results) != 0 {
		t.Fatalf("expected 0 results for whitespace query, got %d", len(results))
	}
}

func TestSearchCappedAtMaxResults(t *testing.T) {
	tools := make([]DeferredToolMeta, 0, maxSearchResults+5)
	for i := range maxSearchResults + 5 {
		tools = append(tools, testCatalogMeta(
			"mcp__s__tool_"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			"s",
			"",
		))
	}
	c := NewDeferredToolCatalog(tools)
	// regex match all by common prefix
	results := c.Search("mcp")
	if len(results) > maxSearchResults {
		t.Fatalf("results capped at %d, got %d", maxSearchResults, len(results))
	}
}

// --- Render tests ---

func TestRenderAvailableDeferredToolsNil(t *testing.T) {
	if out := RenderAvailableDeferredTools(nil); out != "" {
		t.Fatalf("expected empty for nil catalog, got %q", out)
	}
}

func TestRenderAvailableDeferredToolsEmpty(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{})
	if out := RenderAvailableDeferredTools(c); out != "" {
		t.Fatalf("expected empty for empty catalog, got %q", out)
	}
}

func TestRenderAvailableDeferredToolsFormat(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__github__search", "github", "search code"),
		testCatalogMeta("mcp__slack__post", "slack", "post message"),
	})

	out := RenderAvailableDeferredTools(c)
	if !strings.Contains(out, "<available-deferred-tools>") {
		t.Fatal("missing opening tag")
	}
	if !strings.Contains(out, "</available-deferred-tools>") {
		t.Fatal("missing closing tag")
	}
	if !strings.Contains(out, "[server: github]") {
		t.Fatal("missing github server header")
	}
	if !strings.Contains(out, "[server: slack]") {
		t.Fatal("missing slack server header")
	}
	if !strings.Contains(out, "mcp__github__search — search code") {
		t.Fatal("missing tool entry")
	}
	if !strings.Contains(out, "mcp__slack__post — post message") {
		t.Fatal("missing tool entry")
	}
}

func TestRenderAvailableDeferredToolsServersSorted(t *testing.T) {
	c := NewDeferredToolCatalog([]DeferredToolMeta{
		testCatalogMeta("mcp__z__t", "z", "z tool"),
		testCatalogMeta("mcp__a__t", "a", "a tool"),
	})

	out := RenderAvailableDeferredTools(c)
	aIdx := strings.Index(out, "[server: a]")
	zIdx := strings.Index(out, "[server: z]")
	if aIdx < 0 || zIdx < 0 || aIdx >= zIdx {
		t.Fatal("servers should be sorted alphabetically")
	}
}

// --- computeCatalogHash tests ---

func TestComputeCatalogHashEmpty(t *testing.T) {
	h := computeCatalogHash(nil)
	if h == "" {
		t.Fatal("empty tools should still produce a hash")
	}

	h2 := computeCatalogHash([]DeferredToolMeta{})
	if h != h2 {
		t.Fatal("nil and empty slice should produce same hash")
	}
}

func TestComputeCatalogHashOrderIndependent(t *testing.T) {
	// computeCatalogHash is called on already-sorted tools, so we test that.
	sorted := []DeferredToolMeta{
		testCatalogMeta("mcp__s__a", "s", "a"),
		testCatalogMeta("mcp__s__b", "s", "b"),
	}
	h1 := computeCatalogHash(sorted)
	h2 := computeCatalogHash(sorted)
	if h1 != h2 {
		t.Fatal("identical input should produce identical hash")
	}
}

func TestComputeCatalogHashDifferentForDifferentContent(t *testing.T) {
	h1 := computeCatalogHash([]DeferredToolMeta{testCatalogMeta("a", "s1", "d1")})
	h2 := computeCatalogHash([]DeferredToolMeta{testCatalogMeta("b", "s2", "d2")})
	if h1 == h2 {
		t.Fatal("different content should produce different hashes")
	}
}
