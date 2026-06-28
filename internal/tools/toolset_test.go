package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/skills"
)

func tc(name string, in any) core.ToolCall {
	b, _ := json.Marshal(in)
	return core.ToolCall{ID: "tc-1", Name: name, Input: string(b)}
}

func TestViewWriteEdit(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	writeRes, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "a.txt",
		"content":   "hello\nworld\n",
	}))
	if err != nil || writeRes.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, writeRes)
	}

	viewRes, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
		"offset":    1,
		"limit":     1,
	}))
	if err != nil || viewRes.IsError() {
		t.Fatalf("view failed: err=%v res=%+v", err, viewRes)
	}
	if !strings.Contains(viewRes.ModelText, "world") {
		t.Fatalf("unexpected view content: %s", viewRes.ModelText)
	}

	editRes, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "world",
		"replace":   "whale",
	}))
	if err != nil || editRes.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, editRes)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if !strings.Contains(string(got), "whale") {
		t.Fatalf("edit not applied: %s", string(got))
	}
}

func TestReadFileNormalizesCRLFContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("zero\r\none\r\ntwo\r\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := metricString(t, data, "mode"); got != "full" {
		t.Fatalf("mode = %q, want full", got)
	}
	if metricBool(t, data, "truncated") {
		t.Fatalf("small file should not be truncated: %#v", data["metrics"])
	}
	if content := readFileContent(t, data); content != "zero\none\ntwo" {
		t.Fatalf("content = %q, want normalized LF content", content)
	}
	if strings.Contains(readFileContent(t, data), "\r") {
		t.Fatalf("content still contains CR: %q", readFileContent(t, data))
	}
}

func TestReadFileStripsUTF8BOMFromVisibleContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), append([]byte{0xEF, 0xBB, 0xBF}, []byte("zero\none\n")...), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	content := readFileContent(t, data)
	if content != "zero\none" {
		t.Fatalf("content = %q, want BOM stripped from visible content", content)
	}
	if strings.Contains(content, "\ufeff") {
		t.Fatalf("content still contains BOM: %q", content)
	}
}

func TestEditFileDoesNotRequireObservedReadState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta",
		"replace":   "whale",
	}))
	if err != nil {
		t.Fatalf("edit dispatch error: %v", err)
	}
	if res.IsError() {
		t.Fatalf("edit without prior read failed: %s", res.ModelText)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\nwhale\n" {
		t.Fatalf("content = %q, want edit to apply", string(got))
	}
}

func TestEditFileUsesObservedReadState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta",
		"replace":   "whale",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\nwhale\n" {
		t.Fatalf("content = %q, want edited content", string(got))
	}
}

func TestEditFileUsesCurrentDiskContentAfterExternalChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")
	if err := os.WriteFile(path, []byte("external\nbeta\n"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}

	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta",
		"replace":   "whale",
	}))
	if err != nil {
		t.Fatalf("edit dispatch error: %v", err)
	}
	if res.IsError() {
		t.Fatalf("edit after external change failed: %s", res.ModelText)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "external\nwhale\n" {
		t.Fatalf("content = %q, want edit against current disk content", string(got))
	}
}

func TestEditFileMarksInternalReadBeforeEditing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")
	if err := os.WriteFile(path, []byte("external\nbeta\n"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	internalReadMarked := false
	ts.afterFileRead = func(abs string) {
		if abs == path {
			internalReadMarked = true
		}
	}

	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta",
		"replace":   "whale",
	}))
	if err != nil {
		t.Fatalf("edit dispatch error: %v", err)
	}
	if res.IsError() {
		t.Fatalf("edit after external change failed: %s", res.ModelText)
	}
	if !internalReadMarked {
		t.Fatalf("edit did not mark its internal read")
	}
}

func TestEditFileUsesCurrentDiskContentAfterRangeReadStateChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
		"offset":    0,
		"limit":     1,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("range read failed: err=%v res=%+v", err, res)
	}
	if err := os.WriteFile(path, []byte("external\nbeta\n"), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}

	res, err = ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta",
		"replace":   "whale",
	}))
	if err != nil {
		t.Fatalf("edit dispatch error: %v", err)
	}
	if res.IsError() {
		t.Fatalf("edit after changed range-read state failed: %s", res.ModelText)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "external\nwhale\n" {
		t.Fatalf("content = %q, want edit against current disk content", string(got))
	}
}

func TestEditFileValidStateStillReportsMissingSearch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "missing",
		"replace":   "whale",
	}))
	if err != nil {
		t.Fatalf("edit dispatch error: %v", err)
	}
	if got := toolErrorCode(t, res); got != "search_not_found" {
		t.Fatalf("code = %q, want search_not_found; content=%s", got, res.ModelText)
	}
	recovery := toolRecoveryData(t, res)
	if got := recovery["code"]; got != "edit_search_not_found" {
		t.Fatalf("recovery code = %#v, want edit_search_not_found", got)
	}
	if got := recovery["recommended_next_tool"]; got != "read_file" {
		t.Fatalf("recommended_next_tool = %#v, want read_file", got)
	}
	input := recovery["recommended_input"].(map[string]any)
	if got := input["file_path"]; got != "a.txt" {
		t.Fatalf("recommended file_path = %#v, want a.txt", got)
	}
}

func TestEditFileSuccessfulEditRefreshesRuntimeState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")

	first, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "alpha",
		"replace":   "ALPHA",
	}))
	if err != nil || first.IsError() {
		t.Fatalf("first edit failed: err=%v res=%+v", err, first)
	}
	second, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta",
		"replace":   "BETA",
	}))
	if err != nil || second.IsError() {
		t.Fatalf("second edit failed without reread: err=%v res=%+v", err, second)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "ALPHA\nBETA\ngamma\n" {
		t.Fatalf("content = %q, want both edits applied", string(got))
	}
}

func TestWriteFileRefreshesRuntimeStateForEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	writeRes, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "a.txt",
		"content":   "alpha\nbeta\n",
	}))
	if err != nil || writeRes.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, writeRes)
	}
	editRes, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta",
		"replace":   "whale",
	}))
	if err != nil || editRes.IsError() {
		t.Fatalf("edit after write failed: err=%v res=%+v", err, editRes)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\nwhale\n" {
		t.Fatalf("content = %q, want edit based on write state", string(got))
	}
}

func TestMultiEditDoesNotRequireObservedReadStateAndFeedsFollowupEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.multiEditFile(context.Background(), tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "alpha", "replace": "ALPHA"},
			{"search": "ALPHA\nbeta", "replace": "first\nBETA"},
		},
	}))
	if err != nil || res.IsError() {
		t.Fatalf("multi_edit failed: err=%v res=%+v", err, res)
	}
	editRes, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "gamma",
		"replace":   "whale",
	}))
	if err != nil || editRes.IsError() {
		t.Fatalf("edit after multi_edit failed: err=%v res=%+v", err, editRes)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "first\nBETA\nwhale\n" {
		t.Fatalf("content = %q, want ordered multi_edit and edit applied", string(got))
	}
}

func TestMultiEditFailureLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")

	res, err := ts.multiEditFile(context.Background(), tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "alpha", "replace": "ALPHA"},
			{"search": "missing", "replace": "MISSING"},
		},
	}))
	if err != nil {
		t.Fatalf("multi_edit returned dispatch error: %v", err)
	}
	if got := toolErrorCode(t, res); got != "search_not_found" {
		t.Fatalf("code = %q, want search_not_found; content=%s", got, res.ModelText)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\nbeta\n" {
		t.Fatalf("file modified despite failed multi_edit: %q", string(got))
	}
}

func TestMultiEditRejectsNonUniqueSearchUnlessAllTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("foo\nfoo\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")

	res, err := ts.multiEditFile(context.Background(), tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "foo", "replace": "bar"},
		},
	}))
	if err != nil {
		t.Fatalf("multi_edit returned dispatch error: %v", err)
	}
	if got := toolErrorCode(t, res); got != "search_not_unique" {
		t.Fatalf("code = %q, want search_not_unique; content=%s", got, res.ModelText)
	}
	// The error must point at the actual match locations so the model can add a
	// disambiguating line without re-reading the file.
	for _, want := range []string{"line 1:", "line 2:"} {
		if !strings.Contains(res.ModelText, want) {
			t.Fatalf("error missing match location %q; content=%s", want, res.ModelText)
		}
	}
	recovery := toolRecoveryData(t, res)
	if recovery["recommended_next_tool"] != "multi_edit" {
		t.Fatalf("recommended_next_tool = %v, want multi_edit", recovery["recommended_next_tool"])
	}
	if recovery["retryable"] != true {
		t.Fatalf("retryable = %v, want true", recovery["retryable"])
	}

	res, err = ts.multiEditFile(context.Background(), tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "foo", "replace": "bar", "all": true},
		},
	}))
	if err != nil || res.IsError() {
		t.Fatalf("multi_edit all=true failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "bar\nbar\n" {
		t.Fatalf("content = %q, want all matches replaced", string(got))
	}
}

func TestMultiEditNonUniqueErrorCountsNonOverlappingMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	// "aa" overlaps itself: byte offsets 0,1,2 all start an "aa", but
	// strings.Count reports 2 non-overlapping matches. The listed locations
	// must agree with that count.
	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")

	res, err := ts.multiEditFile(context.Background(), tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "aa", "replace": "b"},
		},
	}))
	if err != nil {
		t.Fatalf("multi_edit returned dispatch error: %v", err)
	}
	if got := toolErrorCode(t, res); got != "search_not_unique" {
		t.Fatalf("code = %q, want search_not_unique; content=%s", got, res.ModelText)
	}
	if !strings.Contains(res.ModelText, "matched 2 locations") {
		t.Fatalf("want count of 2 matches; content=%s", res.ModelText)
	}
	// Two non-overlapping matches => exactly two listed "line " entries.
	if got := strings.Count(res.ModelText, "  line "); got != 2 {
		t.Fatalf("listed %d locations, want 2; content=%s", got, res.ModelText)
	}
}

func TestMultiEditNonUniqueErrorTruncatesLongLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "min.js")
	// A minified-style single line where the ambiguous token repeats. The
	// error must not embed the whole multi-kilobyte line per match.
	long := "x=1;" + strings.Repeat("y", 5000) + ";z=1;" + strings.Repeat("y", 5000) + ";z=1;"
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "min.js")

	res, err := ts.multiEditFile(context.Background(), tc("multi_edit", map[string]any{
		"file_path": "min.js",
		"edits": []map[string]any{
			{"search": "z=1;"},
		},
	}))
	if err != nil {
		t.Fatalf("multi_edit returned dispatch error: %v", err)
	}
	if got := toolErrorCode(t, res); got != "search_not_unique" {
		t.Fatalf("code = %q, want search_not_unique; content=%s", got, res.ModelText)
	}
	if !strings.Contains(res.ModelText, "line truncated") {
		t.Fatalf("want truncation marker; content=%s", res.ModelText)
	}
	// The whole error stays far below the raw line size despite two matches.
	if len(res.ModelText) > 2000 {
		t.Fatalf("error length %d not bounded; content=%s", len(res.ModelText), res.ModelText)
	}
}

func TestMultiEditMatchesLFSearchAndPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")
	call := tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "beta", "replace": "whale"},
		},
	})

	preview, err := ts.previewMultiEditFile(context.Background(), call)
	if err != nil {
		t.Fatalf("preview multi_edit: %v", err)
	}
	if diff := firstMetadataDiff(t, preview); !strings.Contains(diff, "-beta") || !strings.Contains(diff, "+whale") {
		t.Fatalf("unexpected preview diff:\n%s", diff)
	}
	res, err := ts.multiEditFile(context.Background(), call)
	if err != nil || res.IsError() {
		t.Fatalf("multi_edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\r\nwhale\r\ngamma\r\n" {
		t.Fatalf("content = %q, want CRLF preserved", string(got))
	}
}

func TestMultiEditPreservesMixedLineEndings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\ngamma\r\ndelta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")

	res, err := ts.multiEditFile(context.Background(), tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "beta", "replace": "whale\ninserted"},
		},
	}))
	if err != nil || res.IsError() {
		t.Fatalf("multi_edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\r\nwhale\ninserted\ngamma\r\ndelta\n" {
		t.Fatalf("content = %q, want mixed line endings preserved", string(got))
	}
}

func TestMultiEditPreviewAndResultMetadataMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	readFileFull(t, ts, "a.txt")
	call := tc("multi_edit", map[string]any{
		"file_path": "a.txt",
		"edits": []map[string]any{
			{"search": "world", "replace": "whale"},
		},
	})

	preview, err := ts.previewMultiEditFile(context.Background(), call)
	if err != nil {
		t.Fatalf("preview multi_edit: %v", err)
	}
	res, err := ts.multiEditFile(context.Background(), call)
	if err != nil || res.IsError() {
		t.Fatalf("multi_edit failed: err=%v res=%+v", err, res)
	}
	if got, want := firstMetadataDiff(t, res.Metadata), firstMetadataDiff(t, preview); got != want {
		t.Fatalf("preview/result diff mismatch:\npreview:\n%s\nresult:\n%s", want, got)
	}
}

func TestMultiEditToolDescriptionDocumentsSchema(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	var spec core.ToolSpec
	for _, tool := range ts.Tools() {
		if tool.Name() == "multi_edit" {
			spec = core.DescribeTool(tool)
			break
		}
	}
	if spec.Name == "" {
		t.Fatal("multi_edit tool not found")
	}
	for _, want := range []string{
		"Apply multiple ordered SEARCH/REPLACE edits",
		"all=false requires search to match exactly once",
		"all=true replaces every occurrence",
	} {
		if !strings.Contains(spec.Description, want) {
			t.Fatalf("multi_edit description missing %q:\n%s", want, spec.Description)
		}
	}
	props, _ := spec.Parameters["properties"].(map[string]any)
	if _, ok := props["edits"].(map[string]any); !ok {
		t.Fatalf("multi_edit schema missing edits property: %+v", props)
	}
}

func TestReadFileDirectoryReturnsStableToolError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "subdir",
	}))
	if err != nil {
		t.Fatalf("read_file dispatch error: %v", err)
	}
	if !res.IsError() {
		t.Fatalf("expected read_file directory to fail, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "not_file") || strings.Contains(res.ModelText, "Incorrect function") {
		t.Fatalf("expected stable not_file error without OS-specific text, got: %s", res.ModelText)
	}
	recovery := toolRecoveryData(t, res)
	if got := recovery["code"]; got != "read_file_target_is_directory" {
		t.Fatalf("recovery code = %#v, want read_file_target_is_directory", got)
	}
	if got := recovery["recommended_next_tool"]; got != "list_dir" {
		t.Fatalf("recommended_next_tool = %#v, want list_dir", got)
	}
	input := recovery["recommended_input"].(map[string]any)
	if got := input["path"]; got != "subdir" {
		t.Fatalf("recommended path = %#v, want subdir", got)
	}
}

func TestWriteFilePreservesCRLFWhenOverwritingExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "a.txt",
		"content":   "alpha\nwhale\n",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\r\nwhale\r\n" {
		t.Fatalf("content = %q, want CRLF preserved", string(got))
	}
}

func TestWriteFilePreservesLFWhenOverwritingExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "a.txt",
		"content":   "alpha\r\nwhale\r\n",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\nwhale\n" {
		t.Fatalf("content = %q, want LF preserved", string(got))
	}
}

func TestWriteFilePreservesUTF8BOMWhenOverwritingExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte("alpha\r\nbeta\r\n")...), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "a.txt",
		"content":   "alpha\nwhale\n",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	want := append([]byte{0xEF, 0xBB, 0xBF}, []byte("alpha\r\nwhale\r\n")...)
	if string(got) != string(want) {
		t.Fatalf("content bytes = % x, want % x", got, want)
	}
}

func TestWriteFileKeepsNewFileContentExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "a.txt",
		"content":   "alpha\r\nwhale\r\n",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\r\nwhale\r\n" {
		t.Fatalf("content = %q, want new file content unchanged", string(got))
	}
}

func TestWriteFilePreservesExistingExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix execute bits are not portable to Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho before\n"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "script.sh",
		"content":   "#!/bin/sh\necho after\n",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, res)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}
	if got := info.Mode().Perm() & 0o111; got == 0 {
		t.Fatalf("existing executable bits were not preserved: mode=%#o", info.Mode().Perm())
	}
}

func TestWriteFileKeepsExistingFileContentExactWhenNoLineEndingStyle(t *testing.T) {
	for _, tt := range []struct {
		name    string
		before  string
		content string
	}{
		{name: "empty placeholder", before: "", content: "alpha\r\nwhale\r\n"},
		{name: "single line placeholder", before: "placeholder", content: "alpha\rwhale\r"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "a.txt")
			if err := os.WriteFile(path, []byte(tt.before), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			ts, err := NewToolset(dir)
			if err != nil {
				t.Fatalf("new toolset: %v", err)
			}

			res, err := ts.writeFile(context.Background(), tc("write", map[string]any{
				"file_path": "a.txt",
				"content":   tt.content,
			}))
			if err != nil || res.IsError() {
				t.Fatalf("write failed: err=%v res=%+v", err, res)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read result: %v", err)
			}
			if string(got) != tt.content {
				t.Fatalf("content = %q, want exact requested content %q", string(got), tt.content)
			}
		})
	}
}

func TestWriteFileConcurrentCreateConflictDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	var mu sync.Mutex
	reads := 0
	ready := make(chan struct{})
	release := make(chan struct{})
	ts.afterFileRead = func(abs string) {
		if abs != path {
			return
		}
		mu.Lock()
		reads++
		if reads == 2 {
			close(ready)
		}
		mu.Unlock()
		<-release
	}

	calls := []core.ToolCall{
		tc("write", map[string]any{"file_path": "new.txt", "content": "one\n"}),
		tc("write", map[string]any{"file_path": "new.txt", "content": "two\n"}),
	}
	results := make([]core.ToolResult, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = ts.writeFile(context.Background(), calls[i])
		}(i)
	}
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatalf("writes did not both reach read barrier")
	}
	close(release)
	wg.Wait()

	successes, conflicts := 0, 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("write %d returned dispatch error: %v", i, errs[i])
		}
		if res.IsError() {
			if got := toolErrorCode(t, res); got != "write_conflict" {
				t.Fatalf("write %d error code = %q, want write_conflict; content=%s", i, got, res.ModelText)
			}
			conflicts++
			continue
		}
		successes++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d results=%+v", successes, conflicts, results)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "one\n" && string(got) != "two\n" {
		t.Fatalf("unexpected final content after conflict-safe writes: %q", string(got))
	}
}

func TestEditFileMatchesLFSearchAndPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "alpha\nbeta",
		"replace":   "alpha\nwhale",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\r\nwhale\r\ngamma\r\n" {
		t.Fatalf("content = %q, want CRLF preserved", string(got))
	}
}

func TestEditFileRepairsJSONEscapedControlCharacters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("\tbody := string(raw)\n\tcontent := body\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    `\tbody := string(raw)\n\tcontent := body`,
		"replace":   `\tbody := string(raw)\n\ttext := htmlToText(body)\n\tcontent := text`,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	want := "\tbody := string(raw)\n\ttext := htmlToText(body)\n\tcontent := text\n"
	if string(got) != want {
		t.Fatalf("content = %q, want %q", string(got), want)
	}
	var out struct {
		Data struct {
			Repair string `json:"repair"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(res.ModelText), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out.Data.Repair != "json_escape_unwrapped" {
		t.Fatalf("repair = %q, want json_escape_unwrapped", out.Data.Repair)
	}
}

func TestEditFileRejectsAmbiguousJSONEscapedRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\nalpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    `alpha\nbeta`,
		"replace":   `alpha\nwhale`,
	}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "JSON-escaped") || !strings.Contains(res.ModelText, "multiple") {
		t.Fatalf("expected ambiguous escaped-search error, got: %s", res.ModelText)
	}
}

func TestEditFileMatchesFirstLineAfterUTF8BOMAndPreservesBOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, append([]byte{0xEF, 0xBB, 0xBF}, []byte("SIF LOCAL:1 > 0\r\n中文┃全角\r\n")...), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "SIF LOCAL:1 > 0",
		"replace":   "SIF LOCAL:1 > 1",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	want := append([]byte{0xEF, 0xBB, 0xBF}, []byte("SIF LOCAL:1 > 1\r\n中文┃全角\r\n")...)
	if string(got) != string(want) {
		t.Fatalf("content bytes = % x, want % x", got, want)
	}
}

func TestEditFilePreservesMixedLineEndings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\ngamma\r\ndelta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "beta\ngamma",
		"replace":   "whale\ngamma",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "alpha\r\nwhale\ngamma\r\ndelta\n" {
		t.Fatalf("content = %q, want mixed line endings preserved", string(got))
	}
}

func TestEditFileDuplicateReplacementKeepsMixedLineEndings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("a\r\nb\nc\r\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "b",
		"replace":   "c",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "a\r\nc\nc\r\n" {
		t.Fatalf("content = %q, want duplicate replacement to preserve mixed endings", string(got))
	}
}

func TestEditFileConcurrentConflictDoesNotLoseUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	var mu sync.Mutex
	reads := 0
	ready := make(chan struct{})
	release := make(chan struct{})
	ts.afterFileRead = func(abs string) {
		if abs != path {
			return
		}
		mu.Lock()
		reads++
		if reads == 2 {
			close(ready)
		}
		mu.Unlock()
		<-release
	}

	readFileFull(t, ts, "a.txt")
	calls := []core.ToolCall{
		tc("edit", map[string]any{"file_path": "a.txt", "search": "alpha", "replace": "ALPHA"}),
		tc("edit", map[string]any{"file_path": "a.txt", "search": "beta", "replace": "BETA"}),
	}
	results := make([]core.ToolResult, len(calls))
	errs := make([]error, len(calls))
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = ts.editFile(context.Background(), calls[i])
		}(i)
	}
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatalf("edits did not both reach read barrier")
	}
	close(release)
	wg.Wait()

	successes, conflicts := 0, 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("edit %d returned dispatch error: %v", i, errs[i])
		}
		if res.IsError() {
			if got := toolErrorCode(t, res); got != "write_conflict" {
				t.Fatalf("edit %d error code = %q, want write_conflict; content=%s", i, got, res.ModelText)
			}
			conflicts++
			continue
		}
		successes++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d results=%+v", successes, conflicts, results)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "ALPHA\nbeta\n" && string(got) != "alpha\nBETA\n" {
		t.Fatalf("unexpected final content after conflict-safe edits: %q", string(got))
	}
}

func TestEditFileDetectsExternalChangeBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	var once sync.Once
	ts.beforeFileCommit = func(abs string) {
		if abs != path {
			return
		}
		once.Do(func() {
			if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
				t.Errorf("external write: %v", err)
			}
		})
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "alpha",
		"replace":   "whale",
	}))
	if err != nil {
		t.Fatalf("edit returned dispatch error: %v", err)
	}
	if got := toolErrorCode(t, res); got != "write_conflict" {
		t.Fatalf("code = %q, want write_conflict; content=%s", got, res.ModelText)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "external\n" {
		t.Fatalf("content = %q, want external write preserved", string(got))
	}
}

func TestReadFileRangeDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("zero\none\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	limitOnly, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
		"limit":     1,
	}))
	if err != nil || limitOnly.IsError() {
		t.Fatalf("limit-only read failed: err=%v res=%+v", err, limitOnly)
	}
	limitData := readFileData(t, limitOnly)
	if got := rangeNumber(t, limitData, "start"); got != 0 {
		t.Fatalf("limit-only start = %d, want 0", got)
	}
	if got := rangeNumber(t, limitData, "end"); got != 1 {
		t.Fatalf("limit-only end = %d, want 1", got)
	}
	if content := readFileContent(t, limitData); content != "zero" {
		t.Fatalf("limit-only content = %q, want zero", content)
	}
	if note, _ := limitData["note"].(string); !strings.Contains(note, "offset was not provided; defaulted to 0") {
		t.Fatalf("missing offset default note in %#v", limitData["note"])
	}

	offsetOnly, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
		"offset":    1,
	}))
	if err != nil || offsetOnly.IsError() {
		t.Fatalf("offset-only read failed: err=%v res=%+v", err, offsetOnly)
	}
	offsetData := readFileData(t, offsetOnly)
	if got := rangeNumber(t, offsetData, "start"); got != 1 {
		t.Fatalf("offset-only start = %d, want 1", got)
	}
	if got := rangeNumber(t, offsetData, "end"); got != 4 {
		t.Fatalf("offset-only end = %d, want 4", got)
	}
	if content := readFileContent(t, offsetData); content != "one\ntwo\nthree" {
		t.Fatalf("offset-only content = %q, want remaining lines", content)
	}
	if note, _ := offsetData["note"].(string); !strings.Contains(note, "limit was not provided; defaulted to 2000 lines") {
		t.Fatalf("missing limit default note in %#v", offsetData["note"])
	}

	fullRead, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
	}))
	if err != nil || fullRead.IsError() {
		t.Fatalf("full read failed: err=%v res=%+v", err, fullRead)
	}
	fullData := readFileData(t, fullRead)
	if _, ok := fullData["note"]; ok {
		t.Fatalf("unexpected note for full read: %#v", fullData["note"])
	}

	scopedRead, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "a.txt",
		"offset":    1,
		"limit":     1,
	}))
	if err != nil || scopedRead.IsError() {
		t.Fatalf("scoped read failed: err=%v res=%+v", err, scopedRead)
	}
	scopedData := readFileData(t, scopedRead)
	if _, ok := scopedData["note"]; ok {
		t.Fatalf("unexpected note for scoped read: %#v", scopedData["note"])
	}
}

func TestReadFileDefaultsToBoundedResult(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i := 0; i < 1200; i++ {
		body.WriteString("large outline line ")
		body.WriteString(strings.Repeat("x", 40))
		body.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "large.txt",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := rangeNumber(t, data, "start"); got != 0 {
		t.Fatalf("start = %d, want 0", got)
	}
	if got := rangeNumber(t, data, "end"); got != defaultReadFileOutlineLines {
		t.Fatalf("end = %d, want %d", got, defaultReadFileOutlineLines)
	}
	if got := metricString(t, data, "mode"); got != "outline" {
		t.Fatalf("mode = %q, want outline", got)
	}
	if got := metricNumber(t, data, "returned_lines"); got != defaultReadFileOutlineLines {
		t.Fatalf("returned_lines = %d, want %d", got, defaultReadFileOutlineLines)
	}
	if !metricBool(t, data, "truncated") {
		t.Fatalf("expected truncated metrics in %#v", data["metrics"])
	}
	if got := metricString(t, data, "truncated_by"); got != "outline" {
		t.Fatalf("truncated_by = %q, want outline", got)
	}
	if !payloadBool(t, data, "has_more_after") {
		t.Fatalf("expected has_more_after in %#v", data["payload"])
	}
	content := readFileContent(t, data)
	if !strings.Contains(content, "outline mode") || !strings.Contains(content, "[head 80 lines for orientation]") {
		t.Fatalf("missing outline content: %q", content)
	}
	if note, _ := data["note"].(string); !strings.Contains(note, "offset=0 limit=2000") {
		t.Fatalf("missing continuation note: %#v", data["note"])
	}
}

func TestEditFileAfterLargeOutlineRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	var body strings.Builder
	for i := 0; i < 5000; i++ {
		body.WriteString("large outline line ")
		body.WriteString(strings.Repeat("x", 40))
		body.WriteByte('\n')
	}
	body.WriteString("UNIQUE_LARGE_FILE_MARKER\n")
	for i := 0; i < 5000; i++ {
		body.WriteString("large outline tail ")
		body.WriteString(strings.Repeat("y", 40))
		body.WriteByte('\n')
	}
	if body.Len() <= 150*1024 {
		t.Fatalf("fixture size = %d, want >150KB", body.Len())
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "large.txt",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := metricString(t, data, "mode"); got != "outline" {
		t.Fatalf("mode = %q, want outline", got)
	}
	payload := data["payload"].(map[string]any)
	if _, ok := payload["snapshot_id"]; ok {
		t.Fatalf("read_file should not expose snapshot_id: %#v", payload)
	}

	res, err = ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "large.txt",
		"search":    "UNIQUE_LARGE_FILE_MARKER",
		"replace":   "UNIQUE_LARGE_FILE_EDITED",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit after outline read failed: err=%v res=%+v", err, res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !strings.Contains(string(got), "UNIQUE_LARGE_FILE_EDITED") {
		t.Fatalf("large file edit was not applied")
	}
}

func TestEditFileUsesCurrentDiskContentAfterLargeOutlineReadStateChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	body := strings.Repeat("outline\n", 22000) + "UNIQUE_LARGE_FILE_MARKER\n"
	if len(body) <= 150*1024 {
		t.Fatalf("fixture size = %d, want >150KB", len(body))
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "large.txt",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	if got := metricString(t, readFileData(t, res), "mode"); got != "outline" {
		t.Fatalf("mode = %q, want outline", got)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(body, "outline\n", "changed\n", 1)), 0o644); err != nil {
		t.Fatalf("external write: %v", err)
	}

	res, err = ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "large.txt",
		"search":    "UNIQUE_LARGE_FILE_MARKER",
		"replace":   "UNIQUE_LARGE_FILE_EDITED",
	}))
	if err != nil {
		t.Fatalf("edit dispatch error: %v", err)
	}
	if res.IsError() {
		t.Fatalf("edit after changed outline-read state failed: %s", res.ModelText)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !strings.Contains(string(got), "UNIQUE_LARGE_FILE_EDITED") {
		t.Fatalf("content missing edit after changed outline-read state")
	}
}

func TestReadFileDefaultNearRegistryCapDoesNotAdvertiseFull(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", defaultReadFileFullMaxBytes-100)
	fileName := "near-$(touch pwn)'cap.txt"
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	registry := core.NewToolRegistry(ts.Tools())

	res, err := registry.Dispatch(context.Background(), tc("read_file", map[string]any{
		"file_path": fileName,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	if len(res.ModelText) > core.DefaultMaxToolResultChars {
		t.Fatalf("registry result len = %d, want <= %d", len(res.ModelText), core.DefaultMaxToolResultChars)
	}
	data, ok := res.Payload.(map[string]any)
	if !ok {
		t.Fatalf("missing structured payload: %s", res.ModelText)
	}
	if outputTruncated, _ := res.Metadata["output_truncated"].(bool); outputTruncated {
		t.Fatalf("registry should not truncate read_file result: %+v", res.Metadata)
	}
	if got := metricString(t, data, "mode"); got != "outline" {
		t.Fatalf("mode = %q, want outline", got)
	}
	if got := metricNumber(t, data, "truncated_line_count"); got != 1 {
		t.Fatalf("truncated_line_count = %d, want 1", got)
	}
	if content := readFileContent(t, data); !strings.Contains(content, "[line truncated]") {
		t.Fatalf("outline should line-truncate oversized head line: %q", content)
	}
	if note, _ := data["note"].(string); !strings.Contains(note, "shell_run") || strings.Contains(note, "offset=0 limit=2000") {
		t.Fatalf("outline note should point oversized lines to shell_run, got: %#v", data["note"])
	}
	if note, _ := data["note"].(string); strings.Contains(note, `"`+fileName+`"`) || !strings.Contains(note, `'near-$(touch pwn)'\''cap.txt'`) {
		t.Fatalf("outline note should shell-single-quote unsafe path, got: %#v", data["note"])
	}
	if content := readFileContent(t, data); !strings.Contains(content, "shell_run") || strings.Contains(content, "offset=0 limit=2000") {
		t.Fatalf("outline content should point oversized lines to shell_run, got: %q", content)
	}
	if content := readFileContent(t, data); strings.Contains(content, `"`+fileName+`"`) || !strings.Contains(content, `'near-$(touch pwn)'\''cap.txt'`) {
		t.Fatalf("outline content should shell-single-quote unsafe path, got: %q", content)
	}
}

func TestReadFileOutlineShrinksToFitRegistryCap(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i := 0; i < defaultReadFileOutlineLines; i++ {
		body.WriteString(strings.Repeat("x", 400))
		body.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "wide-outline.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	registry := core.NewToolRegistry(ts.Tools())

	res, err := registry.Dispatch(context.Background(), tc("read_file", map[string]any{
		"file_path": "wide-outline.txt",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	if len(res.ModelText) > core.DefaultMaxToolResultChars {
		t.Fatalf("registry result len = %d, want <= %d", len(res.ModelText), core.DefaultMaxToolResultChars)
	}
	data, ok := res.Payload.(map[string]any)
	if !ok {
		t.Fatalf("missing structured payload: %s", res.ModelText)
	}
	if outputTruncated, _ := res.Metadata["output_truncated"].(bool); outputTruncated {
		t.Fatalf("registry should not truncate read_file result: %+v", res.Metadata)
	}
	if got := metricString(t, data, "mode"); got != "outline" {
		t.Fatalf("mode = %q, want outline", got)
	}
	returned := metricNumber(t, data, "returned_lines")
	if returned <= 0 || returned >= defaultReadFileOutlineLines {
		t.Fatalf("returned_lines = %d, want shrunk outline below %d", returned, defaultReadFileOutlineLines)
	}
	if got := rangeNumber(t, data, "end"); got != returned {
		t.Fatalf("range end = %d, want returned_lines %d", got, returned)
	}
	if !payloadBool(t, data, "has_more_after") {
		t.Fatalf("expected has_more_after in %#v", data["payload"])
	}
}

func TestReadFileDefaultByteCap(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i := 0; i < 100; i++ {
		body.WriteString(strings.Repeat("x", 1024))
		body.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "wide.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "wide.txt",
		"offset":    0,
		"limit":     100,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := metricString(t, data, "mode"); got != "range" {
		t.Fatalf("mode = %q, want range", got)
	}
	if !metricBool(t, data, "truncated") {
		t.Fatalf("expected truncated metrics in %#v", data["metrics"])
	}
	if got := metricString(t, data, "truncated_by"); got != "bytes" {
		t.Fatalf("truncated_by = %q, want bytes", got)
	}
	if got := metricNumber(t, data, "returned_bytes"); got > defaultReadFileMaxBytes {
		t.Fatalf("returned_bytes = %d, want <= %d", got, defaultReadFileMaxBytes)
	}
	if got := metricNumber(t, data, "returned_lines"); got >= 100 {
		t.Fatalf("returned_lines = %d, want byte-capped result", got)
	}
	if note, _ := data["note"].(string); !strings.Contains(note, "truncated by bytes") {
		t.Fatalf("missing byte truncation note: %#v", data["note"])
	}
}

func TestReadFileFirstLineExceedsByteCap(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", defaultReadFileMaxBytes+1) + "\nnext\n"
	fileName := "one $(touch pwn)'line.txt"
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": fileName,
		"offset":    0,
		"limit":     2,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if content := readFileContent(t, data); content != "" {
		t.Fatalf("content = %q, want empty content for over-limit first line", content)
	}
	if got := rangeNumber(t, data, "end"); got != 0 {
		t.Fatalf("end = %d, want 0", got)
	}
	if !metricBool(t, data, "truncated") {
		t.Fatalf("expected truncated metrics in %#v", data["metrics"])
	}
	if note, _ := data["note"].(string); !strings.Contains(note, "line 1 exceeds") || !strings.Contains(note, "head -c") {
		t.Fatalf("missing first-line note: %#v", data["note"])
	}
	if note, _ := data["note"].(string); strings.Contains(note, fileName+" |") || !strings.Contains(note, "< 'one $(touch pwn)'\\''line.txt' | head -c") {
		t.Fatalf("first-line note should shell-single-quote unsafe path, got: %#v", data["note"])
	}
}

func TestWriteAndEditReturnDiffMetadata(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	readFileFull(t, ts, "a.txt")
	res, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": "a.txt",
		"search":    "world",
		"replace":   "whale",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("edit failed: err=%v res=%+v", err, res)
	}
	diff := firstMetadataDiff(t, res.Metadata)
	if !strings.Contains(diff, "-world") || !strings.Contains(diff, "+whale") {
		t.Fatalf("expected edit diff metadata, got:\n%s", diff)
	}

	res, err = ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": "b.txt",
		"content":   "new file\n",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("write failed: err=%v res=%+v", err, res)
	}
	diff = firstMetadataDiff(t, res.Metadata)
	if !strings.Contains(diff, "+++ b/b.txt") || !strings.Contains(diff, "+new file") {
		t.Fatalf("expected write diff metadata, got:\n%s", diff)
	}
}

func firstMetadataDiff(t *testing.T, metadata map[string]any) string {
	t.Helper()
	if metadata["kind"] != fileDiffMetadataKind {
		t.Fatalf("expected file diff metadata, got %+v", metadata)
	}
	files, ok := metadata["files"].([]map[string]any)
	if !ok || len(files) == 0 {
		t.Fatalf("expected metadata files, got %+v", metadata["files"])
	}
	diff, _ := files[0]["unified_diff"].(string)
	if strings.TrimSpace(diff) == "" {
		t.Fatalf("expected non-empty unified diff, got %+v", files[0])
	}
	return diff
}

func TestPathEscapeDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inside.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	insideRes, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": filepath.Join(dir, "inside.txt"),
	}))
	if err != nil || insideRes.IsError() {
		t.Fatalf("expected absolute path inside workspace to be allowed: err=%v res=%+v", err, insideRes)
	}
	if !strings.Contains(insideRes.ModelText, "ok") {
		t.Fatalf("expected inside file content, got: %s", insideRes.ModelText)
	}
	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": "../x",
	}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "permission_denied") {
		t.Fatalf("expected permission_denied, got: %+v", res)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	res, err = ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": outside,
	}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "permission_denied") {
		t.Fatalf("expected absolute outside path to be denied, got: %+v", res)
	}
}

func TestReadOnlyPathErrorsExplainExternalApproval(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.listDir(context.Background(), tc("list_dir", map[string]any{"path": "codex"}))
	if err != nil {
		t.Fatalf("list_dir err: %v", err)
	}
	msg := toolErrorMessage(t, res)
	for _, want := range []string{
		"Current workspace root: " + workspace,
		"Requested path: codex",
		"Resolved path: " + filepath.Join(workspace, "codex"),
		"External read paths require file access approval",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected list_dir diagnostic to contain %q:\n%s", want, msg)
		}
	}
}

func TestReadOnlyPathEscapeErrorsExplainExternalApproval(t *testing.T) {
	workspace := t.TempDir()
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	cases := []struct {
		name string
		call core.ToolCall
		run  func(context.Context, core.ToolCall) (core.ToolResult, error)
	}{
		{
			name: "read_file",
			call: tc("read_file", map[string]any{"file_path": "../codex/README.md"}),
			run:  ts.readFile,
		},
		{
			name: "list_dir",
			call: tc("list_dir", map[string]any{"path": "../codex"}),
			run:  ts.listDir,
		},
		{
			name: "grep",
			call: tc("grep", map[string]any{"path": "../codex", "pattern": "hook"}),
			run:  ts.searchContent,
		},
		{
			name: "search_files",
			call: tc("search_files", map[string]any{"path": "../codex", "pattern": "hook"}),
			run:  ts.searchFiles,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.run(context.Background(), tc.call)
			if err != nil {
				t.Fatalf("%s err: %v", tc.name, err)
			}
			msg := toolErrorMessage(t, res)
			for _, want := range []string{
				"path escapes workspace",
				"Current workspace root: " + workspace,
				"Requested path: ../codex",
				"External read paths require file access approval",
			} {
				if !strings.Contains(msg, want) {
					t.Fatalf("expected %s diagnostic to contain %q:\n%s", tc.name, want, msg)
				}
			}
		})
	}
}

func TestApprovedExternalReadRootsAllowReadOnlyTools(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	external := filepath.Join(parent, "external")
	if err := os.MkdirAll(filepath.Join(external, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir external: %v", err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(external, "nested", "guide.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write external fixture: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	ctx := WithApprovedExternalReadRoots(context.Background(), []string{external})

	readRes, err := ts.readFile(ctx, tc("read_file", map[string]any{"file_path": filepath.Join(external, "nested", "guide.txt")}))
	if err != nil || readRes.IsError() || !strings.Contains(readRes.ModelText, "needle") {
		t.Fatalf("approved external read_file failed: err=%v res=%+v", err, readRes)
	}
	listRes, err := ts.listDir(ctx, tc("list_dir", map[string]any{"path": external}))
	if err != nil || listRes.IsError() || !strings.Contains(listRes.ModelText, "nested/") {
		t.Fatalf("approved external list_dir failed: err=%v res=%+v", err, listRes)
	}
	grepRes, err := ts.searchContent(ctx, tc("grep", map[string]any{"path": external, "pattern": "needle"}))
	if err != nil || grepRes.IsError() || !strings.Contains(grepRes.ModelText, "guide.txt") {
		t.Fatalf("approved external grep failed: err=%v res=%+v", err, grepRes)
	}
	searchRes, err := ts.searchFiles(ctx, tc("search_files", map[string]any{"path": external, "pattern": "guide"}))
	if err != nil || searchRes.IsError() || !strings.Contains(searchRes.ModelText, "guide.txt") {
		t.Fatalf("approved external search_files failed: err=%v res=%+v", err, searchRes)
	}
}

func TestToolResultReadRootsAllowOnlyReadAndSearchTools(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	root := filepath.Join(parent, ".whale", "tool-results")
	current := filepath.Join(root, "session-1")
	sibling := filepath.Join(root, "session-2")
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatalf("mkdir current tool results: %v", err)
	}
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatalf("mkdir sibling tool results: %v", err)
	}
	currentFile := filepath.Join(current, "long-tool.txt")
	siblingFile := filepath.Join(sibling, "long-tool.txt")
	if err := os.WriteFile(currentFile, []byte("needle current\n"), 0o600); err != nil {
		t.Fatalf("write current tool result: %v", err)
	}
	if err := os.WriteFile(siblingFile, []byte("needle sibling\n"), 0o600); err != nil {
		t.Fatalf("write sibling tool result: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	ctx := WithToolResultReadRoots(context.Background(), []string{current})

	readRes, err := ts.readFile(ctx, tc("read_file", map[string]any{"file_path": currentFile}))
	if err != nil || readRes.IsError() || !strings.Contains(readRes.ModelText, "needle current") {
		t.Fatalf("tool result read_file failed: err=%v res=%+v", err, readRes)
	}
	grepRes, err := ts.searchContent(ctx, tc("grep", map[string]any{"path": current, "pattern": "needle"}))
	if err != nil || grepRes.IsError() || !strings.Contains(grepRes.ModelText, "long-tool.txt") {
		t.Fatalf("tool result grep failed: err=%v res=%+v", err, grepRes)
	}
	searchRes, err := ts.searchFiles(ctx, tc("search_files", map[string]any{"path": current, "pattern": "long-tool"}))
	if err != nil || searchRes.IsError() || !strings.Contains(searchRes.ModelText, "long-tool.txt") {
		t.Fatalf("tool result search_files failed: err=%v res=%+v", err, searchRes)
	}

	listRes, err := ts.listDir(ctx, tc("list_dir", map[string]any{"path": current}))
	if err != nil {
		t.Fatalf("list_dir err: %v", err)
	}
	if !listRes.IsError() || !strings.Contains(listRes.ModelText, "path escapes workspace") {
		t.Fatalf("list_dir should not inherit tool-result read roots: %+v", listRes)
	}
	siblingRes, err := ts.readFile(ctx, tc("read_file", map[string]any{"file_path": siblingFile}))
	if err != nil {
		t.Fatalf("sibling read err: %v", err)
	}
	if !siblingRes.IsError() || !strings.Contains(siblingRes.ModelText, "path escapes workspace") {
		t.Fatalf("sibling session should remain blocked: %+v", siblingRes)
	}
	parentRes, err := ts.searchContent(ctx, tc("grep", map[string]any{"path": filepath.Dir(root), "pattern": "needle"}))
	if err != nil {
		t.Fatalf("parent grep err: %v", err)
	}
	if !parentRes.IsError() || !strings.Contains(parentRes.ModelText, "path escapes workspace") {
		t.Fatalf("parent .whale path should remain blocked: %+v", parentRes)
	}
}

func TestApprovedExternalReadRootsPreserveMissingPathErrors(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	external := filepath.Join(parent, "external")
	if err := os.MkdirAll(external, 0o755); err != nil {
		t.Fatalf("mkdir external: %v", err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	ctx := WithApprovedExternalReadRoots(context.Background(), []string{external})
	missingFile := filepath.Join(external, "missing", "guide.txt")
	missingDir := filepath.Join(external, "missing")

	readRes, err := ts.readFile(ctx, tc("read_file", map[string]any{"file_path": missingFile}))
	if err != nil {
		t.Fatalf("read_file err: %v", err)
	}
	if got := toolErrorCode(t, readRes); got != "not_found" {
		t.Fatalf("read_file missing code = %q, want not_found; res=%+v", got, readRes)
	}

	listRes, err := ts.listDir(ctx, tc("list_dir", map[string]any{"path": missingDir}))
	if err != nil {
		t.Fatalf("list_dir err: %v", err)
	}
	if got := toolErrorCode(t, listRes); got != "not_found" {
		t.Fatalf("list_dir missing code = %q, want not_found; res=%+v", got, listRes)
	}

	grepRes, err := ts.searchContent(ctx, tc("grep", map[string]any{"path": missingDir, "pattern": "needle"}))
	if err != nil {
		t.Fatalf("grep err: %v", err)
	}
	if strings.Contains(grepRes.ModelText, "permission_denied") {
		t.Fatalf("grep missing path should not be permission_denied: %+v", grepRes)
	}

	searchRes, err := ts.searchFiles(ctx, tc("search_files", map[string]any{"path": missingDir, "pattern": "guide"}))
	if err != nil {
		t.Fatalf("search_files err: %v", err)
	}
	if searchRes.IsError() || strings.Contains(searchRes.ModelText, "permission_denied") {
		t.Fatalf("search_files missing path should not be permission_denied: %+v", searchRes)
	}
}

func TestApprovedExternalReadRootsExpandHomeBeforeWorkspaceFallback(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(workspace, "~"), 0o755); err != nil {
		t.Fatalf("mkdir workspace tilde dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "~", "guide.txt"), []byte("workspace tilde\n"), 0o644); err != nil {
		t.Fatalf("write workspace tilde file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "guide.txt"), []byte("home file\n"), 0o644); err != nil {
		t.Fatalf("write home file: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	ctx := WithApprovedExternalReadRoots(context.Background(), []string{home})

	for _, raw := range []string{"~/guide.txt", "$HOME/guide.txt"} {
		res, err := ts.readFile(ctx, tc("read_file", map[string]any{"file_path": raw}))
		if err != nil || res.IsError() {
			t.Fatalf("approved home read %q failed: err=%v res=%+v", raw, err, res)
		}
		if !strings.Contains(res.ModelText, "home file") || strings.Contains(res.ModelText, "workspace tilde") {
			t.Fatalf("home read %q used wrong file: %+v", raw, res)
		}
	}
}

func TestReadOnlyToolsCanReadDiscoveredGlobalSkillReferences(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(home, ".whale", "skills", "global-skill")
	refDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: global-skill\ndescription: Global test skill.\n---\n\n# Global Skill\n\nUse global instructions.\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	refPath := filepath.Join(refDir, "guide.md")
	if err := os.WriteFile(refPath, []byte("reference marker\n"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	loadRes, err := ts.loadSkill(context.Background(), tc("load_skill", map[string]any{
		"name":      "global-skill",
		"arguments": "arg text",
	}))
	if err != nil || loadRes.IsError() {
		t.Fatalf("load_skill failed: err=%v res=%+v", err, loadRes)
	}
	if !strings.Contains(loadRes.ModelText, "Use global instructions") || !strings.Contains(loadRes.ModelText, "arg text") {
		t.Fatalf("unexpected load_skill content: %s", loadRes.ModelText)
	}
	readRes, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": refPath,
	}))
	if err != nil {
		t.Fatalf("read_file err: %v", err)
	}
	if readRes.IsError() || !strings.Contains(readRes.ModelText, "reference marker") || !strings.Contains(readRes.ModelText, "$global-skill/references/guide.md") {
		t.Fatalf("expected read_file to read global skill reference, got: %+v", readRes)
	}

	lsRes, err := ts.listDir(context.Background(), tc("list_dir", map[string]any{
		"path": refDir,
	}))
	if err != nil || lsRes.IsError() || !strings.Contains(lsRes.ModelText, "guide.md") {
		t.Fatalf("expected list_dir to list global skill reference dir: err=%v res=%+v", err, lsRes)
	}

	filesRes, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"path":    skillDir,
		"pattern": "guide",
	}))
	if err != nil || filesRes.IsError() || !strings.Contains(filesRes.ModelText, "$global-skill/references/guide.md") {
		t.Fatalf("expected search_files to search global skill dir: err=%v res=%+v", err, filesRes)
	}

	grepRes, err := ts.searchContent(context.Background(), tc("grep", map[string]any{
		"path":    skillDir,
		"pattern": "reference marker",
	}))
	if err != nil || grepRes.IsError() || !strings.Contains(grepRes.ModelText, "$global-skill/references/guide.md") {
		t.Fatalf("expected grep to search global skill dir: err=%v res=%+v", err, grepRes)
	}

	writeRes, err := ts.writeFile(context.Background(), tc("write", map[string]any{
		"file_path": refPath,
		"content":   "changed\n",
	}))
	if err != nil {
		t.Fatalf("write_file err: %v", err)
	}
	if !writeRes.IsError() || !strings.Contains(writeRes.ModelText, "permission_denied") {
		t.Fatalf("expected write_file to deny global skill path, got: %+v", writeRes)
	}

	editRes, err := ts.editFile(context.Background(), tc("edit", map[string]any{
		"file_path": refPath,
		"search":    "reference",
		"replace":   "changed",
	}))
	if err != nil {
		t.Fatalf("edit_file err: %v", err)
	}
	if !editRes.IsError() || !strings.Contains(editRes.ModelText, "permission_denied") {
		t.Fatalf("expected edit_file to deny global skill path, got: %+v", editRes)
	}
}

func TestSkillReadPathDoesNotFollowSymlinkOutsideSkillDir(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(home, ".whale", "skills", "global-skill")
	refDir := filepath.Join(skillDir, "references")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: global-skill\ndescription: Global test skill.\n---\n\n# Global Skill\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(refDir, "outside.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": link,
	}))
	if err != nil {
		t.Fatalf("read_file err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "permission_denied") {
		t.Fatalf("expected symlink escape to be denied, got: %+v", res)
	}
}

func TestDisabledSkillDoesNotExpandReadBoundary(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(home, ".whale", "skills", "disabled-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: disabled-skill\ndescription: Disabled test skill.\n---\n\n# Disabled\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	refPath := filepath.Join(skillDir, "notes.md")
	if err := os.WriteFile(refPath, []byte("disabled reference"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	ts.SetSkillDisabled([]string{"disabled-skill"})
	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": refPath,
	}))
	if err != nil {
		t.Fatalf("read_file err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "permission_denied") {
		t.Fatalf("expected disabled skill reference to be denied, got: %+v", res)
	}
}

func TestInvalidSkillDoesNotExpandReadBoundary(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(home, ".whale", "skills", "bad-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: bad-skill\n---\n\n# Missing description\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	refPath := filepath.Join(skillDir, "notes.md")
	if err := os.WriteFile(refPath, []byte("invalid reference"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": refPath,
	}))
	if err != nil {
		t.Fatalf("read_file err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "permission_denied") {
		t.Fatalf("expected invalid skill reference to be denied, got: %+v", res)
	}
}

func TestLoadSkillUnknownListsAvailableAndRegistryReadOnly(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillDir := filepath.Join(workspace, ".whale", "skills", "known-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: known-skill\ndescription: Known skill.\n---\n\n# Known\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.loadSkill(context.Background(), tc("load_skill", map[string]any{
		"name": "missing-skill",
	}))
	if err != nil {
		t.Fatalf("load_skill err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "known-skill") {
		t.Fatalf("expected available skill in error, got: %s", res.ModelText)
	}
	var found bool
	for _, tool := range ts.Tools() {
		if tool.Name() == "load_skill" {
			found = true
			if !core.DescribeTool(tool).ReadOnly {
				t.Fatal("load_skill should be read-only")
			}
		}
	}
	if !found {
		t.Fatal("load_skill not registered")
	}
}

func TestLoadSkillFiltersDisabledPluginSkills(t *testing.T) {
	workspace := t.TempDir()
	ts, err := NewToolset(workspace)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	ts.SetExtraSkills([]*skills.Skill{{
		Name:          "plugin-skill",
		Description:   "Plugin skill.",
		Instructions:  "secret plugin instructions",
		SkillFilePath: "plugin://test/SKILL.md",
	}})
	ts.SetSkillDisabled([]string{"plugin-skill"})
	res, err := ts.loadSkill(context.Background(), tc("load_skill", map[string]any{
		"name": "plugin-skill",
	}))
	if err != nil {
		t.Fatalf("load_skill err: %v", err)
	}
	if !res.IsError() || !strings.Contains(res.ModelText, "skill disabled") {
		t.Fatalf("expected disabled plugin skill to be rejected, got: %+v", res)
	}

	missing, err := ts.loadSkill(context.Background(), tc("load_skill", map[string]any{
		"name": "missing-skill",
	}))
	if err != nil {
		t.Fatalf("missing load_skill err: %v", err)
	}
	if strings.Contains(missing.ModelText, "plugin-skill") {
		t.Fatalf("disabled plugin skill leaked into available list: %s", missing.ModelText)
	}
}

func TestListDirAndShellRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	lsRes, err := ts.listDir(context.Background(), tc("list_dir", map[string]any{}))
	if err != nil || lsRes.IsError() {
		t.Fatalf("ls failed: err=%v res=%+v", err, lsRes)
	}
	if !strings.Contains(lsRes.ModelText, "x.txt") {
		t.Fatalf("ls missing file: %s", lsRes.ModelText)
	}
	shellRes, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command": "echo hi",
	}))
	if err != nil || shellRes.IsError() {
		t.Fatalf("shell_run failed: err=%v res=%+v", err, shellRes)
	}
	if !strings.Contains(shellRes.ModelText, "hi") {
		t.Fatalf("unexpected shell output: %s", shellRes.ModelText)
	}
}

func TestWriteStdinRequiresTerminalWriteCapability(t *testing.T) {
	ts, err := NewToolset(t.TempDir())
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	for _, tool := range ts.Tools() {
		if tool.Name() != "write_stdin" {
			continue
		}
		spec := core.DescribeTool(tool)
		if !containsString(spec.Capabilities, "terminal.write") {
			t.Fatalf("write_stdin should expose terminal.write capability, got %#v", spec.Capabilities)
		}
		if containsString(spec.Capabilities, "shell.run") {
			t.Fatalf("write_stdin must not expose shell.run capability, got %#v", spec.Capabilities)
		}
		if containsString(spec.Capabilities, "shell.read") {
			t.Fatalf("write_stdin must not expose shell.read capability, got %#v", spec.Capabilities)
		}
		return
	}
	t.Fatal("write_stdin not registered")
}

func TestListDirIgnoresLegacyIgnoreInput(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".gitignore", "node_modules", "x.txt"} {
		path := filepath.Join(dir, name)
		if name == "node_modules" {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatalf("mkdir fixture: %v", err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	registry := core.NewToolRegistry(ts.Tools())
	lsRes, err := registry.Dispatch(context.Background(), tc("list_dir", map[string]any{
		"ignore": []string{".git", "node"},
	}))
	if err != nil || lsRes.IsError() {
		t.Fatalf("list_dir failed: err=%v res=%+v", err, lsRes)
	}
	for _, want := range []string{".gitignore", "node_modules/", "x.txt"} {
		if !strings.Contains(lsRes.ModelText, want) {
			t.Fatalf("list_dir should not filter %q with legacy ignore input: %s", want, lsRes.ModelText)
		}
	}
}

func TestListDirSchemaDocumentsDefaultRootAndToleratesDeprecatedIgnore(t *testing.T) {
	ts, err := NewToolset(t.TempDir())
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	for _, tool := range ts.Tools() {
		if tool.Name() != "list_dir" {
			continue
		}
		spec := core.DescribeTool(tool)
		props, ok := spec.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("list_dir properties have unexpected shape: %#v", spec.Parameters["properties"])
		}
		ignore, ok := props["ignore"].(map[string]any)
		if !ok {
			t.Fatalf("list_dir schema should tolerate deprecated ignore: %#v", props)
		}
		if desc, _ := ignore["description"].(string); !strings.Contains(desc, "Deprecated") || !strings.Contains(desc, "ignored") {
			t.Fatalf("list_dir deprecated ignore should be documented as ignored: %#v", ignore)
		}
		path, ok := props["path"].(map[string]any)
		if !ok {
			t.Fatalf("list_dir schema should expose path: %#v", props)
		}
		if desc, _ := path["description"].(string); !strings.Contains(desc, "Omit") || !strings.Contains(desc, "workspace root") {
			t.Fatalf("list_dir path should document default workspace root: %#v", path)
		}
		return
	}
	t.Fatal("list_dir not registered")
}

func TestSearchToolSchemasDocumentDefaultRoot(t *testing.T) {
	ts, err := NewToolset(t.TempDir())
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	wantTools := map[string]bool{"grep": false, "search_files": false}
	for _, tool := range ts.Tools() {
		if _, ok := wantTools[tool.Name()]; !ok {
			continue
		}
		spec := core.DescribeTool(tool)
		props, ok := spec.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties have unexpected shape: %#v", tool.Name(), spec.Parameters["properties"])
		}
		path, ok := props["path"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema should expose path: %#v", tool.Name(), props)
		}
		if desc, _ := path["description"].(string); !strings.Contains(desc, "Omit") || !strings.Contains(desc, "workspace root") {
			t.Fatalf("%s path should document default workspace root: %#v", tool.Name(), path)
		}
		wantTools[tool.Name()] = true
	}
	for name, found := range wantTools {
		if !found {
			t.Fatalf("%s not registered", name)
		}
	}
}

func TestSearchFilesSchemaDocumentsIncludeIsUnsupported(t *testing.T) {
	ts, err := NewToolset(t.TempDir())
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	for _, tool := range ts.Tools() {
		if tool.Name() != "search_files" {
			continue
		}
		spec := core.DescribeTool(tool)
		for _, want := range []string{
			"put a glob directly in pattern",
			"does not support include",
			"use grep",
		} {
			if !strings.Contains(spec.Description, want) {
				t.Fatalf("search_files description missing %q: %s", want, spec.Description)
			}
		}
		return
	}
	t.Fatal("search_files not registered")
}

func TestWriteSchemaDocumentsRegularFilePermissions(t *testing.T) {
	ts, err := NewToolset(t.TempDir())
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	for _, tool := range ts.Tools() {
		if tool.Name() != "write" {
			continue
		}
		spec := core.DescribeTool(tool)
		for _, want := range []string{"regular non-executable files", "chmod"} {
			if !strings.Contains(spec.Description, want) {
				t.Fatalf("write description missing %q:\n%s", want, spec.Description)
			}
		}
		return
	}
	t.Fatal("write not registered")
}

func TestSearchFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "alpha.go"), []byte("package sub"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "alpha",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.ModelText, "alpha.go") {
		t.Fatalf("expected alpha.go in result: %s", res.ModelText)
	}
}

func TestSearchFilesGlobPatternFiltersByExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{
		"version.go":      "package main",
		"sub/alpha.go":    "package sub",
		"notes.md":        "# notes",
		"sub/config.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "**/*.go",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	items := searchFileItems(t, data)
	joined := ""
	for _, it := range items {
		joined += fmt.Sprint(it) + "\n"
	}
	for _, want := range []string{"version.go", "alpha.go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in glob results: %#v", want, items)
		}
	}
	for _, unwanted := range []string{"notes.md", "config.json"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("did not expect %q in glob results: %#v", unwanted, items)
		}
	}
}

func TestSearchFilesAcceptsLimitAndCapsMatches(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "alpha"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("fixture"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "alpha",
		"limit":   3,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := metricNumber(t, data, "returned"); got != 3 {
		t.Fatalf("returned = %d, want 3", got)
	}
	if got := metricNumber(t, data, "match_limit"); got != 3 {
		t.Fatalf("match_limit = %d, want 3", got)
	}
	if !metricBool(t, data, "match_limit_reached") || !metricBool(t, data, "truncated") {
		t.Fatalf("expected match limit truncation in %#v", data["metrics"])
	}
	if got := searchFileItems(t, data); len(got) != 3 {
		t.Fatalf("items len = %d, want 3: %#v", len(got), got)
	}
	if summary, _ := data["summary"].(string); !strings.Contains(summary, "3 file matches limit reached") {
		t.Fatalf("missing limit summary: %q", summary)
	}
}

func TestSearchFilesDefaultsAndClampsLimit(t *testing.T) {
	if got := normalizeSearchFilesLimit(0); got != defaultSearchFilesLimit {
		t.Fatalf("default limit = %d, want %d", got, defaultSearchFilesLimit)
	}
	if got := normalizeSearchFilesLimit(maxSearchFilesLimit + 1); got != maxSearchFilesLimit {
		t.Fatalf("clamped limit = %d, want %d", got, maxSearchFilesLimit)
	}
}

func TestSearchFilesFallsBackWhenRipgrepUnavailable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "alpha",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := searchFileItems(t, data); len(got) != 1 || got[0] != "alpha.txt" {
		t.Fatalf("items = %#v, want alpha.txt", got)
	}
	if summary, _ := data["summary"].(string); !strings.Contains(summary, "ripgrep unavailable") {
		t.Fatalf("missing fallback summary: %q", summary)
	}
}

func TestSearchFilesGlobExcludesIgnoredDirs(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	dir := t.TempDir()
	for _, sub := range []string{"src", "node_modules", "vendor"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	for name, body := range map[string]string{
		"root.go":             "package main",
		"src/app.go":          "package src",
		"node_modules/dep.go": "package dep",
		"vendor/lib.go":       "package lib",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "**/*.go",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	joined := ""
	for _, it := range searchFileItems(t, data) {
		joined += fmt.Sprint(it) + "\n"
	}
	for _, want := range []string{"root.go", "app.go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in results: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"node_modules", "vendor"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("ignored dir %q leaked into glob results: %s", unwanted, joined)
		}
	}
}

func TestSearchFilesScopedGlobUsesRipgrep(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"src/app.ts":      "x",
		"src/sub/deep.ts": "x",
		"other/x.ts":      "x",
		"root.ts":         "x",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "src/**/*.ts",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	// Scoped glob must run on ripgrep, not silently fall back to the Go walker.
	if summary, _ := data["summary"].(string); strings.Contains(summary, "ripgrep unavailable") {
		t.Fatalf("scoped glob fell back to Go walker: %q", summary)
	}
	joined := ""
	for _, it := range searchFileItems(t, data) {
		joined += fmt.Sprint(it) + "\n"
	}
	for _, want := range []string{"src/app.ts", "src/sub/deep.ts"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("scoped glob missed %q: %s", want, joined)
		}
	}
	for _, unwanted := range []string{"other/x.ts", "root.ts"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("scoped glob over-matched %q: %s", unwanted, joined)
		}
	}
}

func TestSearchFilesNoMatchGlobStaysOnRipgrep(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "*.nomatch",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	// A no-match glob is a successful empty result; rg exits 1 but must NOT trigger
	// the Go-walker fallback (a second full scan reported as "ripgrep unavailable").
	if summary, _ := data["summary"].(string); strings.Contains(summary, "ripgrep unavailable") {
		t.Fatalf("no-match glob fell back to Go walker: %q", summary)
	}
	if items := searchFileItems(t, data); len(items) != 0 {
		t.Fatalf("expected no matches, got %#v", items)
	}
}

func TestSearchFilesLiteralBracketsStaySubstring(t *testing.T) {
	if patternIsGlob("app/users/[id]/page.tsx") {
		t.Fatal("a bracket-only pattern must not be treated as a glob")
	}
	dir := t.TempDir()
	target := filepath.Join("app", "users", "[id]", "page.tsx")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, target)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, target), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "[id]",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	joined := ""
	for _, it := range searchFileItems(t, data) {
		joined += fmt.Sprint(it) + "\n"
	}
	if !strings.Contains(joined, "[id]") {
		t.Fatalf("literal [id] path not found via substring search: %s", joined)
	}
}

func TestSearchFilesGlobBracketClassInGoFallback(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.js", "b.ts", "c.tsx", "d.css"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Force the pure-Go fallback so we exercise compileSearchFilesGlob directly.
	t.Setenv("PATH", t.TempDir())
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "*.[jt]s",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if summary, _ := data["summary"].(string); !strings.Contains(summary, "ripgrep unavailable") {
		t.Fatalf("expected Go fallback, summary=%q", summary)
	}
	joined := ""
	for _, it := range searchFileItems(t, data) {
		joined += fmt.Sprint(it) + "\n"
	}
	for _, want := range []string{"a.js", "b.ts"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bracket class missed %q (escaped instead of translated): %s", want, joined)
		}
	}
	for _, unwanted := range []string{"c.tsx", "d.css"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("bracket class over-matched %q: %s", unwanted, joined)
		}
	}
}

func TestSearchFilesGlobMatchesRootFilesInGoFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{
		"version.go":   "package main",
		"sub/alpha.go": "package sub",
		"notes.md":     "# notes",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Force the pure-Go fallback by hiding ripgrep from PATH.
	t.Setenv("PATH", t.TempDir())
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "**/*.go",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if summary, _ := data["summary"].(string); !strings.Contains(summary, "ripgrep unavailable") {
		t.Fatalf("expected Go fallback, summary=%q", summary)
	}
	items := searchFileItems(t, data)
	joined := ""
	for _, it := range items {
		joined += fmt.Sprint(it) + "\n"
	}
	for _, want := range []string{"version.go", "alpha.go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in fallback glob results (root file regression): %#v", want, items)
		}
	}
	if strings.Contains(joined, "notes.md") {
		t.Fatalf("did not expect notes.md in glob results: %#v", items)
	}
}

func TestSearchFilesDoesNotMatchWorkspaceParentPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "parenthit-workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "parenthit",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := searchFileItems(t, data); len(got) != 0 {
		t.Fatalf("items len = %d, want 0: %#v", len(got), got)
	}
}

func TestSearchFilesWithRipgrepIncludesGitignoredFiles(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skipf("rg not available: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchFiles(context.Background(), tc("search_files", map[string]any{
		"pattern": "ignored",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("search_files failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := searchFileItems(t, data); len(got) != 1 || got[0] != "ignored.txt" {
		t.Fatalf("items = %#v, want ignored.txt", got)
	}
}

func TestSearchFilesResultReportsTimeout(t *testing.T) {
	data := buildSearchFilesResult([]string{"alpha.txt"}, fileSearchMeta{TimedOut: true}, 10)
	metrics, ok := data["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("missing metrics in %#v", data)
	}
	if metrics["timed_out"] != true || metrics["truncated"] != true || metrics["truncated_by"] != "timeout" {
		t.Fatalf("missing timeout metrics: %#v", metrics)
	}
	if summary, _ := data["summary"].(string); !strings.Contains(summary, "search timed out") {
		t.Fatalf("missing timeout summary: %q", summary)
	}
}

func TestSearchFileNamesWithGoFallbackCancels(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.txt"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	matches, meta, err := searchFileNamesWithGo(ctx, dir, "alpha", false, 10, func(path string) string {
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return filepath.ToSlash(path)
		}
		return filepath.ToSlash(rel)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !meta.Cancelled {
		t.Fatalf("expected cancelled meta: %#v", meta)
	}
	if len(matches) != 0 {
		t.Fatalf("matches len = %d, want 0: %#v", len(matches), matches)
	}
}

func TestGrepAcceptsLimitAndCapsMatches(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "file"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("needle\n"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	registry := core.NewToolRegistry(ts.Tools())
	res, err := registry.Dispatch(context.Background(), tc("grep", map[string]any{
		"pattern": "needle",
		"include": "*.txt",
		"limit":   3,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("grep failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if got := metricNumber(t, data, "returned_matches"); got != 3 {
		t.Fatalf("returned_matches = %d, want 3", got)
	}
	if got := metricNumber(t, data, "match_limit"); got != 3 {
		t.Fatalf("match_limit = %d, want 3", got)
	}
	if !metricBool(t, data, "match_limit_reached") || !metricBool(t, data, "truncated") {
		t.Fatalf("expected match limit truncation in %#v", data["metrics"])
	}
	if matches := grepMatches(t, data); len(matches) != 3 {
		t.Fatalf("matches len = %d, want 3: %#v", len(matches), matches)
	}
	if summary, _ := data["summary"].(string); !strings.Contains(summary, "3 matches limit reached") {
		t.Fatalf("missing limit summary: %q", summary)
	}
}

func TestGrepDefaultsAndClampsLimit(t *testing.T) {
	if got := normalizeGrepLimit(0); got != defaultGrepLimit {
		t.Fatalf("default limit = %d, want %d", got, defaultGrepLimit)
	}
	if got := normalizeGrepLimit(maxGrepLimit + 1); got != maxGrepLimit {
		t.Fatalf("clamped limit = %d, want %d", got, maxGrepLimit)
	}
}

func TestGrepInvalidPatternIncludesEscapeRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.searchContent(context.Background(), tc("grep", map[string]any{
		"pattern": "[",
	}))
	if err != nil {
		t.Fatalf("grep dispatch error: %v", err)
	}
	if got := toolErrorCode(t, res); got != "invalid_pattern" {
		t.Fatalf("code = %q, want invalid_pattern; content=%s", got, res.ModelText)
	}
	recovery := toolRecoveryData(t, res)
	if got := recovery["code"]; got != "grep_invalid_pattern" {
		t.Fatalf("recovery code = %#v, want grep_invalid_pattern", got)
	}
	if got := recovery["recommended_next_tool"]; got != "grep" {
		t.Fatalf("recommended_next_tool = %#v, want grep", got)
	}
	// The literal-search flag is gone: recovery now points at escaping the
	// regex metacharacters, matching the regex-only pattern contract.
	if _, ok := recovery["recommended_input_patch"]; ok {
		t.Fatalf("recovery should not carry an input patch anymore: %#v", recovery)
	}
	reason, _ := recovery["reason"].(string)
	if !strings.Contains(reason, "escape") || !strings.Contains(reason, "regular expression") {
		t.Fatalf("recovery reason should mention escaping the regular expression, got %q", reason)
	}
}

func TestGrepTruncatesLongLines(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", maxGrepLineChars+50) + "needle\n"
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(long), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.searchContent(context.Background(), tc("grep", map[string]any{
		"pattern": "needle",
		"include": "*.txt",
		"limit":   10,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("grep failed: err=%v res=%+v", err, res)
	}
	data := readFileData(t, res)
	if !metricBool(t, data, "lines_truncated") || !metricBool(t, data, "truncated") {
		t.Fatalf("expected line truncation in %#v", data["metrics"])
	}
	matches := grepMatches(t, data)
	if len(matches) != 1 {
		t.Fatalf("matches len = %d, want 1", len(matches))
	}
	match, ok := matches[0].(map[string]any)
	if !ok {
		t.Fatalf("match is not an object: %#v", matches[0])
	}
	line, _ := match["line"].(string)
	if len([]rune(line)) != maxGrepLineChars+3 || !strings.HasPrefix(line, "...") {
		t.Fatalf("line was not truncated correctly: len=%d line=%q", len([]rune(line)), line)
	}
	if !strings.Contains(line, "needle") {
		t.Fatalf("truncated line must preserve matched text: %q", line)
	}
	submatches, ok := match["submatches"].([]any)
	if !ok || len(submatches) != 1 {
		t.Fatalf("missing submatches: %#v", match["submatches"])
	}
	submatch, ok := submatches[0].(map[string]any)
	if !ok {
		t.Fatalf("submatch is not an object: %#v", submatches[0])
	}
	start, _ := submatch["start"].(float64)
	end, _ := submatch["end"].(float64)
	if int(start) < 0 || int(end) > len(line) || int(start) >= int(end) {
		t.Fatalf("submatch offsets out of range: start=%v end=%v line_len=%d", start, end, len(line))
	}
	if got := line[int(start):int(end)]; got != "needle" {
		t.Fatalf("submatch offsets select %q, want needle in %q", got, line)
	}
}

func TestGrepFallsBackWhenRipgrepJSONLineExceedsScannerBuffer(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skipf("rg not available: %v", err)
	}
	dir := t.TempDir()
	long := strings.Repeat("x", grepScannerBufferBytes+1024) + "needle\n"
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), []byte(long), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	done := make(chan core.ToolResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := ts.searchContent(context.Background(), tc("grep", map[string]any{
			"pattern": "needle",
			"include": "*.txt",
			"limit":   10,
		}))
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	var res core.ToolResult
	select {
	case err := <-errs:
		t.Fatalf("grep returned error: %v", err)
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("grep hung after ripgrep scanner error")
	}
	if res.IsError() {
		t.Fatalf("grep returned tool error: %+v", res)
	}
	data := readFileData(t, res)
	matches := grepMatches(t, data)
	if len(matches) != 1 {
		t.Fatalf("matches len = %d, want 1", len(matches))
	}
	match, ok := matches[0].(map[string]any)
	if !ok {
		t.Fatalf("match is not an object: %#v", matches[0])
	}
	line, _ := match["line"].(string)
	if !strings.Contains(line, "needle") {
		t.Fatalf("fallback result did not preserve match text: %q", line)
	}
}

func TestGoSearchFallbackDoesNotMatchSyntheticFinalLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "trailing.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write trailing fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0o644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}

	matches, _, _, err := searchWithGo("^$", dir, "*.txt", defaultGrepLimit, nil)
	if err != nil {
		t.Fatalf("searchWithGo: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no synthetic empty-line matches, got: %+v", matches)
	}
}

func TestGoSearchFallbackMatchesRealEmptyLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blank.txt"), []byte("one\n\nthree\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	matches, _, _, err := searchWithGo("^$", dir, "*.txt", defaultGrepLimit, nil)
	if err != nil {
		t.Fatalf("searchWithGo: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one real empty-line match, got: %+v", matches)
	}
	if matches[0].LineNumber != 2 || matches[0].Line != "" {
		t.Fatalf("unexpected empty-line match: %+v", matches[0])
	}
}

func TestGrepTruncatesLongLinesAroundMatch(t *testing.T) {
	dir := t.TempDir()
	longLine := strings.Repeat("a", maxGrepLineChars+50) + "needle" + strings.Repeat("b", maxGrepLineChars+50)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(longLine), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.searchContent(context.Background(), tc("grep", map[string]any{
		"pattern": "needle",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("grep failed: err=%v res=%+v", err, res)
	}
	env, ok := core.ParseToolEnvelope(res.ModelText)
	if !ok {
		t.Fatalf("parse envelope: %s", res.ModelText)
	}
	payload := env.Data["payload"].(map[string]any)
	matches := payload["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %#v", matches)
	}
	match := matches[0].(map[string]any)
	line := match["line"].(string)
	if !strings.Contains(line, "needle") || !strings.HasPrefix(line, "...") {
		t.Fatalf("line truncation did not preserve match context: %q", line)
	}
	submatches := match["submatches"].([]any)
	if len(submatches) != 1 {
		t.Fatalf("expected one visible submatch, got %#v", submatches)
	}
	submatch := submatches[0].(map[string]any)
	start := int(submatch["start"].(float64))
	end := int(submatch["end"].(float64))
	if start < 0 || end > len(line) || start >= end {
		t.Fatalf("submatch offsets outside returned line: start=%d end=%d len=%d line=%q", start, end, len(line), line)
	}
	if got := line[start:end]; got != "needle" {
		t.Fatalf("submatch offset points to %q, want needle in line %q", got, line)
	}
}

func TestGrepTruncatesUnicodeLongLinesAroundByteOffset(t *testing.T) {
	dir := t.TempDir()
	prefix := strings.Repeat("你", maxGrepLineChars)
	longLine := prefix + "needle" + strings.Repeat("界", maxGrepLineChars)
	if err := os.WriteFile(filepath.Join(dir, "unicode.txt"), []byte(longLine), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.searchContent(context.Background(), tc("grep", map[string]any{
		"pattern": "needle",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("grep failed: err=%v res=%+v", err, res)
	}
	env, ok := core.ParseToolEnvelope(res.ModelText)
	if !ok {
		t.Fatalf("parse envelope: %s", res.ModelText)
	}
	payload := env.Data["payload"].(map[string]any)
	matches := payload["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %#v", matches)
	}
	match := matches[0].(map[string]any)
	line := match["line"].(string)
	if !strings.Contains(line, "needle") {
		t.Fatalf("unicode line truncation dropped match: %q", line)
	}
	submatches := match["submatches"].([]any)
	if len(submatches) != 1 {
		t.Fatalf("expected one visible submatch, got %#v", submatches)
	}
	submatch := submatches[0].(map[string]any)
	start := int(submatch["start"].(float64))
	end := int(submatch["end"].(float64))
	if start < 0 || end > len(line) || start >= end {
		t.Fatalf("submatch offsets outside returned line: start=%d end=%d len=%d line=%q", start, end, len(line), line)
	}
	if got := line[start:end]; got != "needle" {
		t.Fatalf("submatch byte offsets point to %q, want needle in line %q", got, line)
	}
}

func TestGrepDoesNotTruncateUnicodeLineUnderCharacterLimit(t *testing.T) {
	dir := t.TempDir()
	lineText := strings.Repeat("你", 200) + "needle"
	if err := os.WriteFile(filepath.Join(dir, "unicode.txt"), []byte(lineText), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	res, err := ts.searchContent(context.Background(), tc("grep", map[string]any{
		"pattern": "needle",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("grep failed: err=%v res=%+v", err, res)
	}
	env, ok := core.ParseToolEnvelope(res.ModelText)
	if !ok {
		t.Fatalf("parse envelope: %s", res.ModelText)
	}
	metrics := env.Data["metrics"].(map[string]any)
	if metrics["lines_truncated"] != false {
		t.Fatalf("line under character limit should not be truncated: %#v", metrics)
	}
	payload := env.Data["payload"].(map[string]any)
	matches := payload["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %#v", matches)
	}
	match := matches[0].(map[string]any)
	if got := match["line"].(string); got != lineText {
		t.Fatalf("line should be returned whole; got %q want %q", got, lineText)
	}
}

func TestTruncateGrepLineAroundMatchesDropsOutsideSubmatches(t *testing.T) {
	line := strings.Repeat("a", maxGrepLineChars+20) + "needle" + strings.Repeat("b", maxGrepLineChars+20)
	matches := []submatch{
		{Match: "needle", Start: maxGrepLineChars + 20, End: maxGrepLineChars + 26},
		{Match: "late", Start: len([]rune(line)) - 10, End: len([]rune(line)) - 6},
	}

	got, submatches, truncated := truncateGrepLineAroundMatches(line, matches)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(got, "needle") {
		t.Fatalf("snippet does not contain matched text: %q", got)
	}
	if len(submatches) != 1 || submatches[0].Match != "needle" {
		t.Fatalf("expected only visible submatch to remain, got %#v", submatches)
	}
	sm := submatches[0]
	if sm.Start < 0 || sm.End > len(got) || got[sm.Start:sm.End] != "needle" {
		t.Fatalf("adjusted submatch invalid: %#v in %q", sm, got)
	}
}

func TestTruncateGrepLineAroundMatchesUsesByteOffsetsForUnicode(t *testing.T) {
	line := strings.Repeat("你", maxGrepLineChars) + "needle" + strings.Repeat("界", maxGrepLineChars)
	start := len(strings.Repeat("你", maxGrepLineChars))
	got, submatches, truncated := truncateGrepLineAroundMatches(line, []submatch{
		{Match: "needle", Start: start, End: start + len("needle")},
	})
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(got, "needle") {
		t.Fatalf("snippet does not contain matched text: %q", got)
	}
	if len(submatches) != 1 {
		t.Fatalf("expected visible submatch, got %#v", submatches)
	}
	sm := submatches[0]
	if sm.Start < 0 || sm.End > len(got) || got[sm.Start:sm.End] != "needle" {
		t.Fatalf("adjusted byte offset invalid: %#v in %q", sm, got)
	}
}

func TestTruncateGrepLineAroundMatchesCapsOversizedMatch(t *testing.T) {
	line := strings.Repeat("a", maxGrepLineChars*4)
	got, submatches, truncated := truncateGrepLineAroundMatches(line, []submatch{
		{Match: line, Start: 0, End: len(line)},
	})
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len([]rune(got)) > maxGrepLineChars+len("...") {
		t.Fatalf("snippet is not capped: got %d chars", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("oversized match snippet should indicate omitted tail: %q", got)
	}
	if len(submatches) != 0 {
		t.Fatalf("oversized submatch should be omitted when not fully visible, got %#v", submatches)
	}
}

func TestGoSearchCapsOversizedMatchLine(t *testing.T) {
	dir := t.TempDir()
	longLine := strings.Repeat("a", maxGrepLineChars*4)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(longLine), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	matches, _, _, err := searchWithGo(".+", dir, "", defaultGrepLimit, nil)
	if err != nil {
		t.Fatalf("searchWithGo failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %d", len(matches))
	}
	m := matches[0]
	if len([]rune(m.Line)) > maxGrepLineChars+len("...") {
		t.Fatalf("line is not capped: got %d chars", len([]rune(m.Line)))
	}
	if !strings.HasSuffix(m.Line, "...") {
		t.Fatalf("truncated oversized match should show suffix: %q", m.Line)
	}
	if len(m.Submatches) != 0 {
		t.Fatalf("oversized submatch should be omitted when not fully visible, got %#v", m.Submatches)
	}
}

func TestGoSearchTruncatesLongLinesAroundMatch(t *testing.T) {
	dir := t.TempDir()
	longLine := strings.Repeat("a", maxGrepLineChars+50) + "needle" + strings.Repeat("b", maxGrepLineChars+50)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(longLine), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	matches, _, _, err := searchWithGo("needle", dir, "", defaultGrepLimit, nil)
	if err != nil {
		t.Fatalf("searchWithGo failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %d", len(matches))
	}
	m := matches[0]
	if !strings.Contains(m.Line, "needle") {
		t.Fatalf("truncated line does not contain needle: %q", m.Line)
	}
	if !strings.HasPrefix(m.Line, "...") {
		t.Fatalf("truncated line should start with ...: %q", m.Line)
	}
	if len(m.Submatches) != 1 {
		t.Fatalf("expected one submatch, got %d", len(m.Submatches))
	}
	sm := m.Submatches[0]
	if sm.Start < 0 || sm.End > len(m.Line) || sm.Start >= sm.End {
		t.Fatalf("submatch offsets outside line: start=%d end=%d len=%d", sm.Start, sm.End, len(m.Line))
	}
	if got := m.Line[sm.Start:sm.End]; got != "needle" {
		t.Fatalf("submatch offset points to %q, want needle in line %q", got, m.Line)
	}
}

func TestGoSearchTruncatesLongLinesBothEnds(t *testing.T) {
	dir := t.TempDir()
	// Match is in the middle far from both edges, so both ... prefix and suffix appear
	longLine := strings.Repeat("a", maxGrepLineChars+20) + "ZmiddleZ" + strings.Repeat("c", maxGrepLineChars+20)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(longLine), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	matches, _, _, err := searchWithGo("ZmiddleZ", dir, "", defaultGrepLimit, nil)
	if err != nil {
		t.Fatalf("searchWithGo failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one match, got %d", len(matches))
	}
	m := matches[0]
	if !strings.Contains(m.Line, "ZmiddleZ") {
		t.Fatalf("truncated line does not contain match: %q", m.Line)
	}
	if !strings.HasPrefix(m.Line, "...") {
		t.Fatalf("truncated line should start with ...: %q", m.Line)
	}
	if !strings.HasSuffix(m.Line, "...") {
		t.Fatalf("truncated line should end with ...: %q", m.Line)
	}
	if len(m.Submatches) != 1 {
		t.Fatalf("expected one submatch, got %d", len(m.Submatches))
	}
	sm := m.Submatches[0]
	if sm.Start < 0 || sm.End > len(m.Line) || sm.Start >= sm.End {
		t.Fatalf("submatch offsets outside line: start=%d end=%d len=%d", sm.Start, sm.End, len(m.Line))
	}
	if got := m.Line[sm.Start:sm.End]; got != "ZmiddleZ" {
		t.Fatalf("submatch offset points to %q, want ZmiddleZ", got)
	}
	// Verify total line length is reasonable
	if len([]rune(m.Line)) > maxGrepLineChars+len("......") {
		t.Fatalf("final line too long: %d chars", len([]rune(m.Line)))
	}
}

func TestShellRunBackgroundAndWait(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	startRes, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command":    "echo hello",
		"background": true,
	}))
	if err != nil || startRes.IsError() {
		t.Fatalf("shell_run background failed: err=%v res=%+v", err, startRes)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Payload struct {
				TaskID string `json:"task_id"`
			} `json:"payload"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(startRes.ModelText), &envelope); err != nil {
		t.Fatalf("unmarshal start result: %v", err)
	}
	if envelope.Data.Payload.TaskID == "" {
		t.Fatalf("expected task_id, got: %s", startRes.ModelText)
	}
	waitRes, err := ts.shellWait(context.Background(), tc("shell_wait", map[string]any{
		"task_id":    envelope.Data.Payload.TaskID,
		"timeout_ms": 5000,
	}))
	if err != nil || waitRes.IsError() {
		t.Fatalf("shell_wait failed: err=%v res=%+v", err, waitRes)
	}
	if !strings.Contains(waitRes.ModelText, "hello") {
		t.Fatalf("expected output in wait result: %s", waitRes.ModelText)
	}
}

func TestBackgroundShellTasksAndCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background shell command fixture uses POSIX sleep")
	}
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	startRes, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command":    "sleep 30",
		"background": true,
	}))
	if err != nil || startRes.IsError() {
		t.Fatalf("shell_run background failed: err=%v res=%+v", err, startRes)
	}
	taskID := toolsetBackgroundTaskID(t, startRes.ModelText)

	tasks := ts.BackgroundShellTasks()
	if len(tasks) != 1 || tasks[0].ID != taskID || tasks[0].Status != "running" || !strings.Contains(tasks[0].Command, "sleep 30") {
		t.Fatalf("unexpected background task snapshot: %+v", tasks)
	}
	running := ts.RunningBackgroundShellTasks()
	if len(running) != 1 || running[0].ID != taskID {
		t.Fatalf("unexpected running background task snapshot: %+v", running)
	}

	stopped, err := ts.CancelBackgroundShellTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("cancel background task: %v", err)
	}
	if stopped.ID != taskID || stopped.Status == "running" {
		t.Fatalf("unexpected stopped task: %+v", stopped)
	}
	if running := ts.RunningBackgroundShellTasks(); len(running) != 0 {
		t.Fatalf("canceled task should not be in running snapshot: %+v", running)
	}
	if all := ts.BackgroundShellTasks(); len(all) != 1 || all[0].ID != taskID {
		t.Fatalf("all task snapshot should retain canceled task history: %+v", all)
	}
}

func TestCancelAllBackgroundShellTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background shell command fixture uses POSIX sleep")
	}
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	first, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command":    "sleep 30",
		"background": true,
	}))
	if err != nil || first.IsError() {
		t.Fatalf("first shell_run background failed: err=%v res=%+v", err, first)
	}
	second, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command":    "sleep 31",
		"background": true,
	}))
	if err != nil || second.IsError() {
		t.Fatalf("second shell_run background failed: err=%v res=%+v", err, second)
	}

	stopped, err := ts.CancelAllBackgroundShellTasks(context.Background())
	if err != nil {
		t.Fatalf("cancel all background shell tasks: %v", err)
	}
	if len(stopped) != 2 {
		t.Fatalf("expected two stopped tasks, got %+v", stopped)
	}
	for _, task := range stopped {
		if task.Status == "running" {
			t.Fatalf("expected stopped task, got %+v", task)
		}
	}
	if again, err := ts.CancelAllBackgroundShellTasks(context.Background()); err != nil || len(again) != 0 {
		t.Fatalf("second cancel should be empty, got tasks=%+v err=%v", again, err)
	}
	if running := ts.RunningBackgroundShellTasks(); len(running) != 0 {
		t.Fatalf("cancel all should leave no running tasks, got %+v", running)
	}
}

func TestRunningBackgroundShellTasksExcludesTimedOutTasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background shell command fixture uses POSIX sleep")
	}
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	startRes, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command":    "sleep 1",
		"background": true,
		"timeout_ms": 1,
	}))
	if err != nil || startRes.IsError() {
		t.Fatalf("shell_run background failed: err=%v res=%+v", err, startRes)
	}
	taskID := toolsetBackgroundTaskID(t, startRes.ModelText)

	deadline := time.Now().Add(5 * time.Second)
	for {
		all := ts.BackgroundShellTasks()
		if len(all) == 1 && all[0].ID == taskID && all[0].Status == "timeout" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected timed out task %s, got %+v", taskID, all)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if running := ts.RunningBackgroundShellTasks(); len(running) != 0 {
		t.Fatalf("timed out task should not be in running snapshot: %+v", running)
	}
}

func TestShellRunBackgroundFinalWaitReleasesTask(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	startRes, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command":    "echo cleanup",
		"background": true,
	}))
	if err != nil || startRes.IsError() {
		t.Fatalf("shell_run background failed: err=%v res=%+v", err, startRes)
	}
	taskID := toolsetBackgroundTaskID(t, startRes.ModelText)

	waitRes, err := ts.shellWait(context.Background(), tc("shell_wait", map[string]any{
		"task_id":    taskID,
		"timeout_ms": 5000,
	}))
	if err != nil || waitRes.IsError() {
		t.Fatalf("shell_wait failed: err=%v res=%+v", err, waitRes)
	}
	if !strings.Contains(waitRes.ModelText, `"done":true`) {
		t.Fatalf("expected final wait result, got: %s", waitRes.ModelText)
	}

	ts.tasks.mu.RLock()
	taskCount := len(ts.tasks.tasks)
	_, stillTracked := ts.tasks.tasks[taskID]
	ts.tasks.mu.RUnlock()
	if stillTracked || taskCount != 0 {
		t.Fatalf("expected final shell_wait to release task %s, registry has %d tasks", taskID, taskCount)
	}
}

func TestShellRunBackgroundCompletionSchedulesRegistryCleanup(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}

	type scheduledCleanup struct {
		delay time.Duration
		fn    func()
	}
	scheduled := make(chan scheduledCleanup, 1)
	ts.tasks.scheduleCleanup = func(delay time.Duration, fn func()) {
		select {
		case scheduled <- scheduledCleanup{delay: delay, fn: fn}:
		default:
		}
	}

	startRes, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command":    "echo cleanup",
		"background": true,
	}))
	if err != nil || startRes.IsError() {
		t.Fatalf("shell_run background failed: err=%v res=%+v", err, startRes)
	}
	taskID := toolsetBackgroundTaskID(t, startRes.ModelText)

	var cleanup scheduledCleanup
	select {
	case cleanup = <-scheduled:
	case <-time.After(5 * time.Second):
		t.Fatal("expected background completion to schedule registry cleanup")
	}
	if cleanup.delay != shellTaskCompletedTTL {
		t.Fatalf("expected cleanup delay %s, got %s", shellTaskCompletedTTL, cleanup.delay)
	}

	ts.tasks.mu.RLock()
	task, stillTracked := ts.tasks.tasks[taskID]
	ts.tasks.mu.RUnlock()
	if !stillTracked {
		t.Fatalf("expected completed task %s to remain available before TTL cleanup", taskID)
	}

	expiredFinished := time.Now().Add(-shellTaskCompletedTTL - time.Second)
	task.mu.Lock()
	task.finishedAt = &expiredFinished
	task.mu.Unlock()

	cleanup.fn()

	ts.tasks.mu.RLock()
	_, stillTracked = ts.tasks.tasks[taskID]
	ts.tasks.mu.RUnlock()
	if stillTracked {
		t.Fatalf("expected scheduled cleanup to release expired task %s", taskID)
	}
}

func toolsetBackgroundTaskID(t *testing.T, content string) string {
	t.Helper()
	var envelope struct {
		Data struct {
			Payload struct {
				TaskID string `json:"task_id"`
			} `json:"payload"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatalf("unmarshal background result: %v", err)
	}
	if envelope.Data.Payload.TaskID == "" {
		t.Fatalf("missing task_id in background result: %s", content)
	}
	return envelope.Data.Payload.TaskID
}

func TestShellRunCWDStaysInsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command": "pwd",
		"cwd":     "sub",
	}))
	if err != nil || res.IsError() {
		t.Fatalf("shell_run cwd failed: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.ModelText, filepath.Join(dir, "sub")) || !strings.Contains(res.ModelText, `"cwd":"sub"`) {
		t.Fatalf("expected command to run in subdir with cwd metadata: %s", res.ModelText)
	}
	escaped, err := ts.shellRun(context.Background(), tc("shell_run", map[string]any{
		"command": "pwd",
		"cwd":     "../outside",
	}))
	if err != nil {
		t.Fatalf("shell_run escaped cwd returned dispatch error: %v", err)
	}
	if !escaped.IsError() || !strings.Contains(escaped.ModelText, "path escapes workspace") {
		t.Fatalf("expected escaped cwd to be rejected: %+v", escaped)
	}
}

func toolErrorMessage(t *testing.T, res core.ToolResult) string {
	t.Helper()
	if !res.IsError() {
		t.Fatalf("expected tool error, got %+v", res)
	}
	env, ok := core.ParseToolEnvelope(res.ModelText)
	if !ok {
		t.Fatalf("parse tool envelope: %s", res.ModelText)
	}
	if env.Message == "" {
		t.Fatalf("expected error message in envelope: %+v", env)
	}
	return env.Message
}

func toolErrorCode(t *testing.T, res core.ToolResult) string {
	t.Helper()
	if !res.IsError() {
		t.Fatalf("expected tool error, got %+v", res)
	}
	env, ok := core.ParseToolEnvelope(res.ModelText)
	if !ok {
		t.Fatalf("parse tool envelope: %s", res.ModelText)
	}
	if env.Code == "" {
		t.Fatalf("expected error code in envelope: %+v", env)
	}
	return env.Code
}

func toolRecoveryData(t *testing.T, res core.ToolResult) map[string]any {
	t.Helper()
	if !res.IsError() {
		t.Fatalf("expected tool error, got %+v", res)
	}
	env, ok := core.ParseToolEnvelope(res.ModelText)
	if !ok {
		t.Fatalf("parse tool envelope: %s", res.ModelText)
	}
	recovery, ok := env.Data["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("expected object recovery in envelope data, got %#v from %s", env.Data["recovery"], res.ModelText)
	}
	return recovery
}

func readFileData(t *testing.T, res core.ToolResult) map[string]any {
	t.Helper()
	// Registry-dispatched results carry the structured payload; direct
	// toolset results still serialize the envelope.
	if payload, ok := res.Payload.(map[string]any); ok {
		return payload
	}
	env, ok := core.ParseToolEnvelope(res.ModelText)
	if !ok {
		t.Fatalf("parse read_file envelope: %s", res.ModelText)
	}
	return env.Data
}

func rangeNumber(t *testing.T, data map[string]any, key string) int {
	t.Helper()
	payload, ok := data["payload"].(map[string]any)
	if !ok {
		t.Fatalf("missing payload in %#v", data)
	}
	rangeData, ok := payload["range"].(map[string]any)
	if !ok {
		t.Fatalf("missing range in %#v", payload)
	}
	v, ok := rangeData[key].(float64)
	if !ok {
		t.Fatalf("missing range %s in %#v", key, rangeData)
	}
	return int(v)
}

func readFileContent(t *testing.T, data map[string]any) string {
	t.Helper()
	payload, ok := data["payload"].(map[string]any)
	if !ok {
		t.Fatalf("missing payload in %#v", data)
	}
	content, ok := payload["content"].(string)
	if !ok {
		t.Fatalf("missing content in %#v", payload)
	}
	return content
}

func readFileFull(t *testing.T, ts *Toolset, path string) {
	t.Helper()
	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{
		"file_path": path,
	}))
	if err != nil || res.IsError() {
		t.Fatalf("read_file failed: err=%v res=%+v", err, res)
	}
	payload, ok := readFileData(t, res)["payload"].(map[string]any)
	if !ok {
		t.Fatalf("missing payload in read_file result: %s", res.ModelText)
	}
	if _, ok := payload["snapshot_id"]; ok {
		t.Fatalf("read_file should not expose snapshot_id: %#v", payload)
	}
}

func grepMatches(t *testing.T, data map[string]any) []any {
	t.Helper()
	payload, ok := data["payload"].(map[string]any)
	if !ok {
		t.Fatalf("missing payload in %#v", data)
	}
	matches, ok := payload["matches"].([]any)
	if !ok {
		t.Fatalf("missing matches in %#v", payload)
	}
	return matches
}

func searchFileItems(t *testing.T, data map[string]any) []any {
	t.Helper()
	payload, ok := data["payload"].(map[string]any)
	if !ok {
		t.Fatalf("missing payload in %#v", data)
	}
	items, ok := payload["items"].([]any)
	if !ok {
		t.Fatalf("missing items in %#v", payload)
	}
	return items
}

func metricNumber(t *testing.T, data map[string]any, key string) int {
	t.Helper()
	metrics, ok := data["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("missing metrics in %#v", data)
	}
	v, ok := metrics[key].(float64)
	if !ok {
		t.Fatalf("missing metric %s in %#v", key, metrics)
	}
	return int(v)
}

func metricBool(t *testing.T, data map[string]any, key string) bool {
	t.Helper()
	metrics, ok := data["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("missing metrics in %#v", data)
	}
	v, ok := metrics[key].(bool)
	if !ok {
		t.Fatalf("missing metric %s in %#v", key, metrics)
	}
	return v
}

func metricString(t *testing.T, data map[string]any, key string) string {
	t.Helper()
	metrics, ok := data["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("missing metrics in %#v", data)
	}
	v, ok := metrics[key].(string)
	if !ok {
		t.Fatalf("missing metric %s in %#v", key, metrics)
	}
	return v
}

func payloadBool(t *testing.T, data map[string]any, key string) bool {
	t.Helper()
	payload, ok := data["payload"].(map[string]any)
	if !ok {
		t.Fatalf("missing payload in %#v", data)
	}
	v, ok := payload[key].(bool)
	if !ok {
		t.Fatalf("missing payload bool %s in %#v", key, payload)
	}
	return v
}
