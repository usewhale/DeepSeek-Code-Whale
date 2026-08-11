#!/bin/sh
# Live smoke for DeepSeek Responses API server-side web search.
# Requires a working DEEPSEEK_API_KEY (and an account with deepseek-v4-flash
# Responses API access). Run from the repository root:
#   ./scripts/smoke/responses_web_search.sh
set -e

if [ -z "$DEEPSEEK_API_KEY" ]; then
  echo "DEEPSEEK_API_KEY is required" >&2
  exit 1
fi

BASE="${DEEPSEEK_BASE_URL:-https://api.deepseek.com}"

payload='{
  "model": "deepseek-v4-flash",
  "store": false,
  "stream": true,
  "reasoning": {"effort": "none"},
  "tools": [{"type": "web_search"}],
  "tool_choice": "auto",
  "input": [{"type": "message", "role": "user", "content": "2026年最新发布的编程语言是什么？请联网搜索后回答。"}]
}'

echo "POST $BASE/responses with web_search tool..."
resp=$(curl -sS -N -X POST "$BASE/responses" \
  -H "Authorization: Bearer $DEEPSEEK_API_KEY" \
  -H "Content-Type: application/json" \
  -d "$payload")

echo "$resp" | grep -o '"type":"[^"]*"' | sort | uniq -c
if echo "$resp" | grep -q 'response.completed'; then
  echo "OK: responses stream completed"
else
  echo "FAIL: no response.completed in stream" >&2
  exit 1
fi
if echo "$resp" | grep -q 'web_search_call'; then
  echo "OK: server-side web search executed"
else
  echo "WARN: no web_search_call observed (model may have answered without searching)"
fi
