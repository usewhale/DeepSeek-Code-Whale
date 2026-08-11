#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
HELPER="$ROOT/scripts/swebench/instance_tools.py"

INSTANCE_ID=""
SWE_BENCH_DIR="${SWE_BENCH_DIR:-$ROOT/../SWE-bench}"
DATASET_NAME="${SWE_BENCH_DATASET:-SWE-bench/SWE-bench_Lite}"
DATASET_SPLIT="${SWE_BENCH_SPLIT:-test}"
MODEL="${WHALE_MODEL:-deepseek-v4-pro}"
EFFORT="${WHALE_REASONING_EFFORT:-high}"
TIMEOUT_SEC="${WHALE_TIMEOUT_SEC:-1800}"
MAX_WORKERS="${SWE_BENCH_MAX_WORKERS:-1}"
WORK_ROOT="${WHALE_SWEBENCH_WORK_ROOT:-$ROOT/tmp/swebench}"
RUN_ID=""
PREPARE_ONLY=0
SKIP_EVAL=0
EVAL_NAMESPACE="${SWE_BENCH_NAMESPACE-}"
WHALE_BIN="${WHALE_BIN:-$ROOT/bin/whale}"

usage() {
  cat <<'EOF'
Usage: scripts/swebench/run_instance.sh --instance-id <id> [options]

Options:
  --instance-id <id>      SWE-bench instance id to run.
  --swebench-dir <path>   Local SWE-bench checkout. Defaults to ../SWE-bench.
  --dataset-name <name>   Dataset name. Defaults to SWE-bench/SWE-bench_Lite.
  --split <name>          Dataset split. Defaults to test.
  --model <name>          Whale model. Defaults to deepseek-v4-pro.
  --effort <name>         Whale reasoning effort. Defaults to high.
  --timeout-sec <n>       Whale timeout in seconds. Defaults to 1800.
  --max-workers <n>       SWE-bench harness max workers. Defaults to 1.
  --work-root <path>      Output root. Defaults to whale/tmp/swebench.
  --run-id <id>           Explicit SWE-bench run id. Default is timestamped.
  --prepare-only          Stop after prompt + checkout preparation.
  --skip-eval             Stop after writing predictions.json.
  --namespace <value>     Pass through to SWE-bench --namespace.
  --whale-bin <path>      Use an existing whale binary instead of go build.
  -h, --help              Show this help.

Environment:
  DEEPSEEK_API_KEY        Required unless --prepare-only is used.
  SWE_BENCH_DIR           Alternate way to point at the SWE-bench checkout.
  SWE_BENCH_NAMESPACE     Alternate way to pass SWE-bench --namespace.
EOF
}

fail() {
  echo "[swebench] $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --instance-id)
      INSTANCE_ID="${2:-}"
      shift 2
      ;;
    --swebench-dir)
      SWE_BENCH_DIR="${2:-}"
      shift 2
      ;;
    --dataset-name)
      DATASET_NAME="${2:-}"
      shift 2
      ;;
    --split)
      DATASET_SPLIT="${2:-}"
      shift 2
      ;;
    --model)
      MODEL="${2:-}"
      shift 2
      ;;
    --effort)
      EFFORT="${2:-}"
      shift 2
      ;;
    --timeout-sec)
      TIMEOUT_SEC="${2:-}"
      shift 2
      ;;
    --max-workers)
      MAX_WORKERS="${2:-}"
      shift 2
      ;;
    --work-root)
      WORK_ROOT="${2:-}"
      shift 2
      ;;
    --run-id)
      RUN_ID="${2:-}"
      shift 2
      ;;
    --prepare-only)
      PREPARE_ONLY=1
      shift
      ;;
    --skip-eval)
      SKIP_EVAL=1
      shift
      ;;
    --namespace)
      EVAL_NAMESPACE="${2-}"
      shift 2
      ;;
    --whale-bin)
      WHALE_BIN="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ -n "$INSTANCE_ID" ]] || fail "--instance-id is required"

need_cmd git
need_cmd python3
[[ -f "$HELPER" ]] || fail "missing helper script: $HELPER"
[[ -d "$SWE_BENCH_DIR" ]] || fail "missing SWE-bench checkout: $SWE_BENCH_DIR"
[[ -x "$SWE_BENCH_DIR/.venv/bin/python" ]] || fail "missing SWE-bench venv python: $SWE_BENCH_DIR/.venv/bin/python"
if [[ $PREPARE_ONLY -eq 0 ]]; then
  need_cmd rg
  [[ -n "${DEEPSEEK_API_KEY:-}" ]] || fail "DEEPSEEK_API_KEY is required"
  if [[ ! -x "$WHALE_BIN" ]]; then
    need_cmd go
  fi
fi

if [[ -z "$RUN_ID" ]]; then
  stamp="$(date -u +%Y%m%d-%H%M%S)"
  RUN_ID="whale-${INSTANCE_ID//__/-}-$stamp"
fi

RUN_ROOT="$WORK_ROOT/$RUN_ID"
ARTIFACT_DIR="$RUN_ROOT/artifacts"
CHECKOUT_DIR="$RUN_ROOT/repo"
PROMPT_FILE="$ARTIFACT_DIR/prompt.txt"
METADATA_FILE="$ARTIFACT_DIR/metadata.json"
WHALE_STDOUT="$ARTIFACT_DIR/whale.stdout"
WHALE_STDERR="$ARTIFACT_DIR/whale.stderr"
PATCH_FILE="$ARTIFACT_DIR/model.patch"
PREDICTIONS_FILE="$ARTIFACT_DIR/predictions.json"
WHALE_DATA_DIR="$ARTIFACT_DIR/whale-data"
mkdir -p "$ARTIFACT_DIR" "$ROOT/bin" "$ROOT/.gocache"

echo "[swebench] rendering prompt + metadata for $INSTANCE_ID"
"$SWE_BENCH_DIR/.venv/bin/python" "$HELPER" prepare \
  --dataset-name "$DATASET_NAME" \
  --split "$DATASET_SPLIT" \
  --instance-id "$INSTANCE_ID" \
  --prompt-out "$PROMPT_FILE" \
  --metadata-out "$METADATA_FILE"

REPO_URL="$("$SWE_BENCH_DIR/.venv/bin/python" - <<'PY' "$METADATA_FILE"
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["repo_url"])
PY
)"
BASE_COMMIT="$("$SWE_BENCH_DIR/.venv/bin/python" - <<'PY' "$METADATA_FILE"
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["base_commit"])
PY
)"

echo "[swebench] cloning $REPO_URL at $BASE_COMMIT"
git clone --filter=blob:none --no-checkout "$REPO_URL" "$CHECKOUT_DIR" >/dev/null
git -C "$CHECKOUT_DIR" checkout --quiet "$BASE_COMMIT"

if [[ $PREPARE_ONLY -eq 1 ]]; then
  cat <<EOF
[swebench] prepare complete
run_id=$RUN_ID
run_root=$RUN_ROOT
metadata=$METADATA_FILE
prompt=$PROMPT_FILE
repo=$CHECKOUT_DIR
EOF
  exit 0
fi

echo "[swebench] building whale"
if [[ ! -x "$WHALE_BIN" ]]; then
  (
    cd "$ROOT"
    GOCACHE="$ROOT/.gocache" go build -o "$WHALE_BIN" ./cmd/whale
  )
else
  echo "[swebench] using existing whale binary: $WHALE_BIN"
fi

echo "[swebench] running whale in $CHECKOUT_DIR"
# The strict session gate requires the session file to exist before exec can
# resume it. Seed an empty session file so the first round of a fresh run id
# passes the gate; exec then appends the round to the same session.
mkdir -p "$WHALE_DATA_DIR/sessions"
: >"$WHALE_DATA_DIR/sessions/$RUN_ID.jsonl"
(
  cd "$CHECKOUT_DIR"
  DEEPSEEK_API_KEY="$DEEPSEEK_API_KEY" \
    "$WHALE_BIN" \
    --data-dir "$WHALE_DATA_DIR" \
    --approval-mode never-ask \
    --memory-enabled=false \
    --model "$MODEL" \
    --config "model_reasoning_effort=$EFFORT" \
    exec --json --timeout-sec "$TIMEOUT_SEC" \
      --session "$RUN_ID" \
      --mode agent \
    >"$WHALE_STDOUT" 2>"$WHALE_STDERR"
) <"$PROMPT_FILE"

git -C "$CHECKOUT_DIR" diff --binary >"$PATCH_FILE"
[[ -s "$PATCH_FILE" ]] || fail "whale produced an empty patch: $PATCH_FILE"

echo "[swebench] writing predictions file"
"$SWE_BENCH_DIR/.venv/bin/python" "$HELPER" prediction \
  --instance-id "$INSTANCE_ID" \
  --model-name "whale-$MODEL" \
  --patch-file "$PATCH_FILE" \
  --out "$PREDICTIONS_FILE"

if [[ $SKIP_EVAL -eq 1 ]]; then
  cat <<EOF
[swebench] predictions ready
run_id=$RUN_ID
run_root=$RUN_ROOT
predictions=$PREDICTIONS_FILE
patch=$PATCH_FILE
whale_stdout=$WHALE_STDOUT
whale_stderr=$WHALE_STDERR
EOF
  exit 0
fi

echo "[swebench] running SWE-bench harness"
EVAL_CMD=(
  "$SWE_BENCH_DIR/.venv/bin/python"
  -m swebench.harness.run_evaluation
  --dataset_name "$DATASET_NAME"
  --predictions_path "$PREDICTIONS_FILE"
  --max_workers "$MAX_WORKERS"
  --instance_ids "$INSTANCE_ID"
  --run_id "$RUN_ID"
)
if [[ -n "$EVAL_NAMESPACE" ]]; then
  EVAL_CMD+=(--namespace "$EVAL_NAMESPACE")
fi
(
  cd "$SWE_BENCH_DIR"
  "${EVAL_CMD[@]}"
)

REPORT_PATH="$SWE_BENCH_DIR/logs/run_evaluation/$RUN_ID/whale-$MODEL/$INSTANCE_ID/report.json"

cat <<EOF
[swebench] run complete
run_id=$RUN_ID
run_root=$RUN_ROOT
repo=$CHECKOUT_DIR
prompt=$PROMPT_FILE
metadata=$METADATA_FILE
whale_stdout=$WHALE_STDOUT
whale_stderr=$WHALE_STDERR
patch=$PATCH_FILE
predictions=$PREDICTIONS_FILE
report=$REPORT_PATH
EOF
