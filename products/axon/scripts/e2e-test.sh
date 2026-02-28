#!/usr/bin/env bash
set -euo pipefail

# OpenOva Axon — E2E Test Suite (OpenAI-compatible)
# Prerequisites: Axon running on localhost:3000, Redis on localhost:6379

BASE_URL="${AXON_TEST_URL:-http://localhost:3000}"
API_KEY="${AXON_TEST_KEY:-sk-dev-test}"
MODEL="${AXON_TEST_MODEL:-claude-sonnet-4-6}"

PASS=0
FAIL=0
CONV_ID=""
STREAM_CONV_ID=""

# ── Helpers ──────────────────────────────────────────────────────────

red()   { printf "\033[31m%s\033[0m" "$1"; }
green() { printf "\033[32m%s\033[0m" "$1"; }
bold()  { printf "\033[1m%s\033[0m" "$1"; }

assert_http() {
  local label="$1" expected_code="$2" actual_code="$3" body="$4"
  if [[ "$actual_code" == "$expected_code" ]]; then
    PASS=$((PASS + 1))
    echo "  $(green PASS) $label (HTTP $actual_code)"
  else
    FAIL=$((FAIL + 1))
    echo "  $(red FAIL) $label — expected HTTP $expected_code, got $actual_code"
    echo "       Body: ${body:0:200}"
  fi
}

assert_contains() {
  local label="$1" haystack="$2" needle="$3"
  if echo "$haystack" | grep -qF "$needle"; then
    PASS=$((PASS + 1))
    echo "  $(green PASS) $label"
  else
    FAIL=$((FAIL + 1))
    echo "  $(red FAIL) $label — expected to contain: $needle"
    echo "       Got: ${haystack:0:200}"
  fi
}

assert_not_empty() {
  local label="$1" value="$2"
  if [[ -n "$value" ]]; then
    PASS=$((PASS + 1))
    echo "  $(green PASS) $label"
  else
    FAIL=$((FAIL + 1))
    echo "  $(red FAIL) $label — value is empty"
  fi
}

assert_eq() {
  local label="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then
    PASS=$((PASS + 1))
    echo "  $(green PASS) $label"
  else
    FAIL=$((FAIL + 1))
    echo "  $(red FAIL) $label — expected: $expected, got: $actual"
  fi
}

assert_gte() {
  local label="$1" expected="$2" actual="$3"
  if [[ "$actual" -ge "$expected" ]]; then
    PASS=$((PASS + 1))
    echo "  $(green PASS) $label"
  else
    FAIL=$((FAIL + 1))
    echo "  $(red FAIL) $label — expected >= $expected, got: $actual"
  fi
}

json_field() {
  python3 -c "
import json, sys
data = json.loads(sys.stdin.read())
keys = '$1'.split('.')
for k in keys:
    if isinstance(data, list):
        data = data[int(k)]
    else:
        data = data[k]
print(data if data is not None else 'null')
" 2>/dev/null
}

# Extract first JSON object from potentially dirty output (e.g. proxy noise)
extract_json() {
  python3 -c "
import sys
raw = sys.stdin.read()
depth = 0
for i, c in enumerate(raw):
    if c == '{': depth += 1
    elif c == '}':
        depth -= 1
        if depth == 0:
            print(raw[:i+1])
            break
" 2>/dev/null
}

post_chat() {
  local data="$1"
  local raw
  raw=$(curl -s "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "$data" 2>/dev/null)
  echo "$raw" | extract_json
}

post_chat_with_code() {
  local data="$1"
  curl -s -w "\n%{http_code}" "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "$data" 2>/dev/null
}

# ── Pre-flight ───────────────────────────────────────────────────────

echo ""
bold "OpenOva Axon — E2E Test Suite (OpenAI-compatible)"
echo ""
echo "  Target:  $BASE_URL"
echo "  Model:   $MODEL"
echo ""

if ! curl -s "$BASE_URL/health" > /dev/null 2>&1; then
  echo "$(red ERROR): Axon not reachable at $BASE_URL"
  exit 1
fi

# ═══════════════════════════════════════════════════════════════════════
# SECTION A: Infrastructure
# ═══════════════════════════════════════════════════════════════════════

echo "$(bold 'A1. Health endpoint')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/health" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
assert_http "/health returns 200" "200" "$HTTP" "$BODY"
assert_contains "status:ok" "$BODY" '"status":"ok"'

echo ""
echo "$(bold 'A2. Stats endpoint')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/stats" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
assert_http "/stats returns 200" "200" "$HTTP" "$BODY"
assert_contains "Has sessions" "$BODY" '"sessions"'
assert_contains "Has conversations" "$BODY" '"conversations"'

echo ""
echo "$(bold 'A3. Models endpoint')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/models" \
  -H "Authorization: Bearer $API_KEY" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
assert_http "/v1/models returns 200" "200" "$HTTP" "$BODY"
assert_contains "object: list" "$BODY" '"object":"list"'
assert_contains "Has claude-sonnet-4-6" "$BODY" "claude-sonnet-4-6"
assert_contains "Has claude-opus-4-6" "$BODY" "claude-opus-4-6"
assert_contains "Has claude-haiku-4-5" "$BODY" "claude-haiku-4-5"

# ═══════════════════════════════════════════════════════════════════════
# SECTION B: Auth errors
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "$(bold 'B1. Missing Authorization header')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"x\"}]}" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
assert_http "Returns 401" "401" "$HTTP" ""

echo ""
echo "$(bold 'B2. Invalid API key')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer wrong-key-12345" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"x\"}]}" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
assert_http "Returns 401" "401" "$HTTP" ""

# ═══════════════════════════════════════════════════════════════════════
# SECTION C: Validation errors
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "$(bold 'C1. Empty messages')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"messages\":[]}" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
assert_http "Returns 400" "400" "$HTTP" ""

echo ""
echo "$(bold 'C2. Invalid conversation_id')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"conversation_id\":\"conv-nonexistent\",\"messages\":[{\"role\":\"user\",\"content\":\"x\"}]}" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
assert_http "Returns 404" "404" "$HTTP" ""

echo ""
echo "$(bold 'C3. GET nonexistent conversation')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/conversations/conv-doesnt-exist" \
  -H "Authorization: Bearer $API_KEY" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
assert_http "Returns 404" "404" "$HTTP" ""

# ═══════════════════════════════════════════════════════════════════════
# SECTION D: OpenAI response shape (non-streaming)
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "$(bold 'D1. Response shape — all required OpenAI fields')"
BODY=$(post_chat "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Say exactly: shape test\"}],\"max_tokens\":50}")

assert_contains "Has id field" "$BODY" '"id"'
assert_contains "id starts with chatcmpl-" "$BODY" '"chatcmpl-'
assert_contains "Has object: chat.completion" "$BODY" '"object":"chat.completion"'
assert_contains "Has created" "$BODY" '"created"'
assert_contains "Has model" "$BODY" '"model"'
assert_contains "Has system_fingerprint" "$BODY" '"system_fingerprint"'
assert_contains "Has choices array" "$BODY" '"choices"'
assert_contains "Has message.role: assistant" "$BODY" '"role":"assistant"'
assert_contains "Has message.content" "$BODY" '"content"'
assert_contains "Has message.refusal" "$BODY" '"refusal"'
assert_contains "Has logprobs: null" "$BODY" '"logprobs":null'
assert_contains "Has finish_reason" "$BODY" '"finish_reason"'
assert_contains "Has usage" "$BODY" '"usage"'
assert_contains "Has prompt_tokens" "$BODY" '"prompt_tokens"'
assert_contains "Has completion_tokens" "$BODY" '"completion_tokens"'
assert_contains "Has total_tokens" "$BODY" '"total_tokens"'
assert_contains "Has conversation_id (Axon ext)" "$BODY" '"conversation_id"'
assert_contains "conversation_id starts with conv-" "$BODY" '"conv-'

# ═══════════════════════════════════════════════════════════════════════
# SECTION E: Conversation (Ralph loop)
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "$(bold 'E1. New conversation — Turn 1 (introduce name)')"
BODY=$(post_chat "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello, my name is Ralph. Remember this name.\"}]}")
CONV_ID=$(echo "$BODY" | json_field "conversation_id")
CONTENT=$(echo "$BODY" | json_field "choices.0.message.content")
assert_not_empty "Has conversation_id" "$CONV_ID"
assert_contains "conv_id starts with conv-" "$CONV_ID" "conv-"
assert_not_empty "Has assistant response" "$CONTENT"
echo "       Conv ID:   $CONV_ID"
echo "       Assistant: $CONTENT"

echo ""
echo "$(bold 'E2. Continue conversation — Turn 2 (name recall)')"
BODY=$(post_chat "{\"model\":\"$MODEL\",\"conversation_id\":\"$CONV_ID\",\"messages\":[{\"role\":\"user\",\"content\":\"What is my name? Reply with just the name.\"}]}")
RESP_CONV=$(echo "$BODY" | json_field "conversation_id")
CONTENT=$(echo "$BODY" | json_field "choices.0.message.content")
assert_eq "Same conversation_id" "$CONV_ID" "$RESP_CONV"
assert_contains "Claude recalls Ralph" "$CONTENT" "Ralph"
echo "       Assistant: $CONTENT"

echo ""
echo "$(bold 'E3. Full context chain — Turn 3 (summarize)')"
BODY=$(post_chat "{\"model\":\"$MODEL\",\"conversation_id\":\"$CONV_ID\",\"messages\":[{\"role\":\"user\",\"content\":\"Summarize our conversation in one sentence.\"}]}")
CONTENT=$(echo "$BODY" | json_field "choices.0.message.content")
assert_contains "Summary mentions Ralph" "$CONTENT" "Ralph"
echo "       Assistant: $CONTENT"

echo ""
echo "$(bold 'E4. GET /v1/conversations/:id')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/conversations/$CONV_ID" \
  -H "Authorization: Bearer $API_KEY" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d' | extract_json)
assert_http "Returns 200" "200" "$HTTP" "$BODY"
MSG_COUNT=$(echo "$BODY" | json_field "message_count")
assert_eq "Has 6 messages" "6" "$MSG_COUNT"

# ═══════════════════════════════════════════════════════════════════════
# SECTION F: OpenAI request parameters
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "$(bold 'F1. Kitchen sink — all OpenAI params accepted')"
RESP=$(post_chat_with_code '{
  "model": "'"$MODEL"'",
  "messages": [
    {"role": "system", "content": "You are helpful."},
    {"role": "user", "content": "Say: params ok"}
  ],
  "temperature": 0.5,
  "top_p": 0.95,
  "n": 1,
  "stop": ["\n\n"],
  "max_tokens": 100,
  "max_completion_tokens": 100,
  "presence_penalty": 0.1,
  "frequency_penalty": 0.1,
  "logit_bias": {"123": 1},
  "logprobs": false,
  "top_logprobs": null,
  "seed": 12345,
  "response_format": {"type": "text"},
  "tools": [{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],
  "tool_choice": "none",
  "parallel_tool_calls": false,
  "user": "test-user",
  "store": false,
  "metadata": {"test": "true"}
}')
HTTP=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d' | extract_json)
assert_http "All params accepted, returns 200" "200" "$HTTP" "$BODY"
assert_contains "Has valid response" "$BODY" '"finish_reason":"stop"'

echo ""
echo "$(bold 'F2. response_format: json_object')"
BODY=$(post_chat "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Return JSON: {name, age, city} with made-up values\"}],\"response_format\":{\"type\":\"json_object\"},\"max_tokens\":200}")
CONTENT=$(echo "$BODY" | json_field "choices.0.message.content")
# Verify it's parseable JSON
VALID=$(python3 -c "import json; json.loads('''$CONTENT'''); print('yes')" 2>/dev/null || echo "no")
assert_eq "Content is valid JSON" "yes" "$VALID"
echo "       Content: $CONTENT"

echo ""
echo "$(bold 'F3. System message')"
BODY=$(post_chat "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"system\",\"content\":\"Always reply in exactly 3 words.\"},{\"role\":\"user\",\"content\":\"What is rain?\"}],\"max_tokens\":50}")
CONTENT=$(echo "$BODY" | json_field "choices.0.message.content")
assert_not_empty "Has response" "$CONTENT"
echo "       Assistant: $CONTENT"

echo ""
echo "$(bold 'F4. max_completion_tokens alias')"
RESP=$(post_chat_with_code "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Say: alias works\"}],\"max_completion_tokens\":50}")
HTTP=$(echo "$RESP" | tail -1)
assert_http "max_completion_tokens accepted" "200" "$HTTP" ""

echo ""
echo "$(bold 'F5. OpenAI model name mapping (gpt-4o)')"
RESP=$(post_chat_with_code "{\"model\":\"gpt-4o\",\"messages\":[{\"role\":\"user\",\"content\":\"Say: mapped\"}],\"max_tokens\":20}")
HTTP=$(echo "$RESP" | tail -1)
assert_http "gpt-4o mapped to claude, returns 200" "200" "$HTTP" ""

# ═══════════════════════════════════════════════════════════════════════
# SECTION G: Streaming
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "$(bold 'G1. Streaming — response shape')"
STREAM_OUTPUT=$(curl -sN "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Say exactly: pineapple\"}]}" 2>/dev/null)

assert_contains "Has data: prefix" "$STREAM_OUTPUT" "data: "
assert_contains "Has [DONE] marker" "$STREAM_OUTPUT" "data: [DONE]"
assert_contains "Has object: chat.completion.chunk" "$STREAM_OUTPUT" '"object":"chat.completion.chunk"'
assert_contains "Has role:assistant chunk" "$STREAM_OUTPUT" '"role":"assistant"'
assert_contains "Has finish_reason:stop" "$STREAM_OUTPUT" '"finish_reason":"stop"'
assert_contains "Has system_fingerprint" "$STREAM_OUTPUT" '"system_fingerprint"'
assert_contains "Has logprobs:null" "$STREAM_OUTPUT" '"logprobs":null'
assert_contains "Has chatcmpl- id" "$STREAM_OUTPUT" '"chatcmpl-'

STREAM_CONV_ID=$(echo "$STREAM_OUTPUT" | head -1 | sed 's/^data: //' | json_field "conversation_id")
assert_not_empty "First chunk has conversation_id" "$STREAM_CONV_ID"
echo "       Stream Conv ID: $STREAM_CONV_ID"

echo ""
echo "$(bold 'G2. Streaming — continue conversation')"
cat > /tmp/axon-e2e-stream.json << ENDJSON
{"model":"$MODEL","stream":true,"conversation_id":"$STREAM_CONV_ID","messages":[{"role":"user","content":"What word did I ask you to say?"}]}
ENDJSON
STREAM_OUTPUT=$(curl -sN "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d @/tmp/axon-e2e-stream.json 2>/dev/null)
assert_contains "Has [DONE]" "$STREAM_OUTPUT" "data: [DONE]"

STREAM_TEXT=$(echo "$STREAM_OUTPUT" | grep '^data: {' | sed 's/^data: //' | python3 -c "
import json, sys
text = ''
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    try:
        d = json.loads(line)
        c = d.get('choices',[{}])[0].get('delta',{}).get('content','')
        if c: text += c
    except: pass
print(text)
" 2>/dev/null)
assert_not_empty "Stream has content" "$STREAM_TEXT"
echo "       Assistant: $STREAM_TEXT"

STREAM_RESP_ID=$(echo "$STREAM_OUTPUT" | head -1 | sed 's/^data: //' | json_field "conversation_id")
assert_eq "Same conversation_id in stream" "$STREAM_CONV_ID" "$STREAM_RESP_ID"
rm -f /tmp/axon-e2e-stream.json

echo ""
echo "$(bold 'G3. Streaming — stream_options.include_usage')"
STREAM_OUTPUT=$(curl -sN "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"stream\":true,\"stream_options\":{\"include_usage\":true},\"messages\":[{\"role\":\"user\",\"content\":\"Say: usage test\"}]}" 2>/dev/null)
assert_contains "Has usage chunk" "$STREAM_OUTPUT" '"prompt_tokens"'
assert_contains "Usage chunk has empty choices" "$STREAM_OUTPUT" '"choices":[]'

echo ""
echo "$(bold 'G4. Streaming conversation stored in Valkey')"
RESP=$(curl -s -w "\n%{http_code}" "$BASE_URL/v1/conversations/$STREAM_CONV_ID" \
  -H "Authorization: Bearer $API_KEY" 2>/dev/null)
HTTP=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d' | extract_json)
assert_http "Stream conversation retrievable" "200" "$HTTP" "$BODY"
MSG_COUNT=$(echo "$BODY" | json_field "message_count")
assert_gte "Stream conversation has >= 4 messages" 4 "$MSG_COUNT"

# ═══════════════════════════════════════════════════════════════════════
# SECTION H: Session pool integrity
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "$(bold 'H1. Session reuse — pool not killed')"
STATS=$(curl -s "$BASE_URL/stats" 2>/dev/null)
IDLE=$(echo "$STATS" | json_field "sessions.idle")
BUSY=$(echo "$STATS" | json_field "sessions.busy")
ACQUIRES=$(echo "$STATS" | json_field "pool.counters.acquire")
RELEASES=$(echo "$STATS" | json_field "pool.counters.release")

assert_gte "Idle sessions >= 1" 1 "$IDLE"
assert_eq "No busy sessions" "0" "$BUSY"
assert_eq "Acquires == Releases" "$ACQUIRES" "$RELEASES"

CONV_COUNT=$(echo "$STATS" | json_field "conversations")
assert_gte "Multiple conversations tracked" 2 "$CONV_COUNT"

echo "       Stats: idle=$IDLE, busy=$BUSY, acquires=$ACQUIRES, releases=$RELEASES, conversations=$CONV_COUNT"

# ═══════════════════════════════════════════════════════════════════════
# Summary
# ═══════════════════════════════════════════════════════════════════════

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
TOTAL=$((PASS + FAIL))
if [[ "$FAIL" -eq 0 ]]; then
  echo "  $(green "ALL $TOTAL TESTS PASSED")"
else
  echo "  $(green "$PASS passed"), $(red "$FAIL failed") out of $TOTAL"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

exit "$FAIL"
