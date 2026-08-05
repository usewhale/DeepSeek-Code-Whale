# 260805 handoff: whale-acp root-cause fixes — implemented, uncommitted, interrupted

Continue on branch `fix/acp-rootcause-fixes` in
`.local/worktrees/acp-rootcause-fixes/` (base: `main` `df6ddc2`). Plan:
`260805-go-whale-acp-rootcause-fixes.md` (at repo root of this worktree —
copied from Hilfe; also in `~/.whale/sessions-acp/archive/acp-rootcause-fixes-20260805/`
alongside the dead session transcript `acp-e1127ae0449c15f0.jsonl`).

**How this session died:** RC2, live — the whale-acp binary still runs the old
code (`WithMaxToolIters(100)`; the fix is uncommitted, so the running binary
hasn't been rebuilt). The turn hit the 100-tool-iteration cap while
implementing the fix. The transcript is healthy; work is intact and
**uncommitted** in this worktree. Do not reset/stash.

**Snapshot:** `~/.whale/sessions-acp/archive/acp-rootcause-fixes-20260805/`
(transcript + `uncommitted-tracked.patch` 1033 lines + new test files).

## What is implemented (uncommitted, in worktree)

17 files modified, 731 insertions; 2 new test files (`internal/agent/agent_cap_test.go`,
`internal/defaults/defaults_test.go`). The dead session's own summary:

1. **Phase 1.1** — `deepseek.WithModel("deepseek-chat")` deleted from
   `cmd/whale-acp/main.go`. New `modelFromEnv()`: `WHALE_MODEL` validated via
   `IsSupportedModel` (else error), fallback `defaults.DefaultModel`. One name
   feeds both provider and window.
2. **Phase 1b** — explicit API transport: `deepseek.API`
   (`APIAuto`/`APIResponses`/`APIChatCompletions`), strict `NormalizeAPI`
   (rejects `completions`/`chat` aliases), `WithAPI`, `Client.api` field,
   `responsesEnabled()` consults `api` first (explicit beats model heuristic),
   `ResolveTransport` compat rules (server→implies responses;
   chat_completions+server degrades web_search to local with warning; never
   refuse-to-start). Config: `[providers.deepseek] api` + `WHALE_API` env
   (env wins), wired through provider.go, runtime.go, task_runtime.go,
   app_runtime_init.go.
3. **Phase 1.2** — ACP agent now gets `WithAutoCompact(true, compactThresh,
   contextWindow)` with threshold passed EXPLICITLY
   (`defaults.DefaultAutoCompactThreshold` 0.85 = CLI parity, not the 0.90
   agent default).
4. **Phase 2** — `defaults.DefaultMaxToolIters = 300`, `WHALE_MAX_TOOL_ITERS`
   env, replaces hardcoded 100. Zero/negative rejected — cap stays finite
   (mutating-arg-loop blind spot).
5. **Phase 3** — env knobs: WHALE_MODEL, WHALE_API, WHALE_COMPACT_THRESHOLD,
   WHALE_CONTEXT_WINDOW, WHALE_MAX_TOOL_ITERS. Read once at startup.
6. **adapter.go** — `AgentEventTypeContextCompacted` explicitly dropped in
   `translateEvent` (no panic, no stray chunk).

**Verified before death:** `go build ./...`, `go vet ./...`, gofmt clean;
`-race` green on defaults, compact, acp, cmd/whale-acp, llm/deepseek, agent,
internal/app (non-service). Test list per plan matrix items 1-4 (incl. the
mutating-arg loop cap test and healthy-160-round completion).

## Continuation (2026-08-05, second session) — items 1-5 done, 6 open

1. **Full suite re-run: DONE.** `go test ./...` is green everywhere except two
   failures, both reproduced on untouched base `df6ddc2` in a clean scratch
   extraction — not ours:
   - `internal/tools.TestRunShellBackgroundDoesNotPanic` — hangs (runtime
     scheduler stall at a nil-field read, tasks.go:432) in this environment;
     hangs identically on base. Pre-existing/environmental.
   - `internal/app/service` `TestResumeMenuStartupOpensSessionPickerBeforeHydration`
     — pre-existing DATA RACE, as the dead session already documented.
   `-race` re-run green on defaults, compact, acp, cmd/whale-acp,
   llm/deepseek, agent, and the app config/transport tests.
2. **Docs: DONE.** `docs/configuration.en.md` + `docs/configuration.md`
   (Chinese): `web_search` section rewritten to "where search runs" (transport
   decoupled); new `api` section (`responses | chat_completions | auto`, strict
   grammar, compat rules incl. the chat_completions+server degrade);
   `[providers.deepseek]` api/web_search added to the reference block; env table
   extended with `WHALE_API` (CLI+ACP) and the ACP knobs (WHALE_MODEL,
   WHALE_COMPACT_THRESHOLD, WHALE_CONTEXT_WINDOW, WHALE_MAX_TOOL_ITERS) with a
   read-once-at-startup note; E2 note (cache hits don't exempt from the window).
3. **Adversarial codemap review pass (E1-E8, R1-R4): DELIVERED** — verified
   against the code:
   - E1 alias: gone (modelFromEnv validates via IsSupportedModel; default
     deepseek-v4-flash). E2: compaction is token-count based (turn_loop.go
     :173-175); docs note added. E3: 0.85 passed explicitly, tunable
     (WHALE_COMPACT_THRESHOLD). E4: cap 300 finite, 0/negative rejected. E5:
     lower-bound test added (compact_test.go). E6: ReadOnly is tool policy,
     orthogonal to compaction (adapter.go). E7: per-agent fields. E8: unchanged.
   - R1: compaction at loop top, same goroutine as tool dispatch — sequential.
     R2/R3: no new shared state. R4: env read once at startup, documented.
4. **Changelog callouts (R2/R3) — capture in the commit/PR "User-visible
   impact" (repo has no CHANGELOG file; releases are GitHub Releases):**
   - Auto-compaction is now enabled on the ACP path (`whale-acp`): sessions on
     `deepseek-v4-flash` auto-compact at 85% of the 1M window instead of
     overflowing into a provider error. Context-window and threshold are
     derived from the model and can be overridden via env vars.
   - The ACP per-turn tool-iteration cap is raised from a hardcoded 100 to a
     configurable 300 (`WHALE_MAX_TOOL_ITERS`): legitimate long turns that used
     to be truncated by a forced summary now complete; the forced-summary
     banner now only appears at 300 iterations.
   - Transport is now explicit: `WHALE_API`/`[providers.deepseek] api` select
     the Responses API or chat completions; the retired `deepseek-chat` model
     alias is rejected (`WHALE_MODEL` validates against the supported set).
5. **COMMIT: DONE** — implementation + this handoff committed on
   `fix/acp-rootcause-fixes` (51be16c, 22 files, +1113/−17).
6. **ACP smoke test: DONE** — `DEEPSEEK_API_KEY` was available; built the
   binary and ran the repo's ACP smoke harness (`internal/acp/smoke_test.go`):
   - `TestSmokeACP` default (api auto → Responses API): PASS — 44 streaming
     updates, stopReason=end_turn.
   - `TestSmokeACP` with `WHALE_API=chat_completions`: PASS — 48 updates,
     end_turn; startup log confirmed `api="chat_completions"`.
   - `TestSmokeACPCancel` (tool-calling turn "list files, write summary"):
     PASS — tool dispatch worked, stopReason=cancelled after 7 updates.
   Startup log on every run: `model=deepseek-v4-flash api=...
   context_window=1000000 compact_threshold=0.85 max_tool_iters=300` — the
   root-cause wiring is live. The two remaining matrix-5 scenarios (grow past
   threshold → ContextCompacted; multi-edit >150 iters) are impractical to
   drive live (need ~850K injected tokens) and are covered by unit tests:
   the turn-loop trigger (turn_loop.go:173-175), the estimator lower bound
   (compact_test.go), and the adapter's ContextCompacted drop
   (handler_test.go) + the 160-round agent test.

## Key design decisions (do not revisit without a new adversarial pass)

- `WHALE_API` grammar is exactly `responses|chat_completions|auto`, **no
  aliases** — `completions` has no referent (only `/chat/completions` and
  `/responses` exist); aliasing re-introduces the retired-alias smell.
- Env > config > model heuristic for transport (mirrors `DEEPSEEK_BASE_URL`).
- Cap is 300, not removed — the dynamic guards cannot see mutating-arg loops.
- Threshold 0.85 passed explicitly; agent default 0.90 is a trap.
- Streaming is not a knob; non-streaming is only an automatic soft-degrade for
  endpoints that reject `stream:true` (not implemented yet — flag if you add it).

## Where the plan file lives

The plan was copied into this worktree root as
`260805-go-whale-acp-rootcause-fixes.md` (from Hilfe
`docs/plans/`). It contains the full adversarial review, appendices
(context-window derivation map, DeepSeek model slugs) and the review findings
(threshold trap, mutating-loop blind spot, web_search compat) — all applied.
