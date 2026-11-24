#!/usr/bin/env bash
# release-highlights.sh PREV_REF TAG_REF OUT
#
# A short summary of the commits PREV_REF..TAG_REF for the release notes,
# written by the newest "luna" model the OpenAI-compatible API at
# RELEASE_AI_BASE_URL lists (key: RELEASE_AI_KEY). The model list is read
# every time, so a newer luna model is picked up without a change here.
#
# Never fails the release: when the API is not configured or anything goes
# wrong, OUT is left empty and the script exits 0.
set -u

OUT="$3"
: > "$OUT"
warn() { echo "::warning::release highlights: $*"; exit 0; }

[ -n "${RELEASE_AI_KEY:-}" ] && [ -n "${RELEASE_AI_BASE_URL:-}" ] || { echo "release highlights: not configured"; exit 0; }
BASE="${RELEASE_AI_BASE_URL%/}"
# The API's address stays private (the log of a public repository is
# public): nothing here prints it, and curl runs without its own error
# messages, which would name the host.

LOG="$(git log "$1..$2" --no-merges --format='- %s%n%b' | grep -v '^[[:space:]]*$' | head -c 60000)"
[ -n "$LOG" ] || { echo "release highlights: no commits"; exit 0; }

MODELS="$(curl -fs --max-time 30 -H "Authorization: Bearer ${RELEASE_AI_KEY}" "${BASE}/models")" \
  || warn "could not list the models"
# the newest plain "...-luna" (a suffixed variant such as -mini only when
# there is no plain one)
IDS="$(printf '%s' "$MODELS" | jq -r '.data[].id' 2>/dev/null)"
MODEL="$(printf '%s\n' "$IDS" | grep -i 'luna$' | sort -V | tail -n1)"
[ -n "$MODEL" ] || MODEL="$(printf '%s\n' "$IDS" | grep -i 'luna' | sort -V | tail -n1)"
[ -n "$MODEL" ] || warn "no luna model listed"

SYSTEM='You write the "Highlights" section of the release notes of hy2scale, a Hysteria 2 mesh relay network with a web UI.
The input is the commit messages since the previous release.
Write 3 to 6 Markdown bullet points, each starting with "- ", for the people who run hy2scale: new features, changes in behaviour, notable fixes.
Rules:
- Use only what the commit messages state. Do not infer causes, settings, environments or scenarios they do not describe; keep their meaning exact. When a message is unclear, leave it out.
- One change per point, written as one short sentence of at most 20 words.
- Leave out releases, version bumps, web bundles, documentation, tests, CI, changes to how releases are built or described, and refactoring with no visible effect.
- Plain, factual wording: no marketing, no emojis, no commit hashes, no file, function or type names.
Output only the bullet points.'

BODY="$(jq -n --arg m "$MODEL" --arg s "$SYSTEM" --arg u "$LOG" \
  '{model: $m, messages: [{role: "system", content: $s}, {role: "user", content: $u}]}')"
RESP="$(curl -fs --max-time 300 -H "Authorization: Bearer ${RELEASE_AI_KEY}" \
  -H 'Content-Type: application/json' -d "$BODY" "${BASE}/chat/completions")" \
  || warn "the request to ${MODEL} failed"

# keep only the bullet points
printf '%s\n' "$RESP" | jq -r '.choices[0].message.content // empty' 2>/dev/null \
  | grep -E '^[-*] ' | sed 's/^\* /- /' | head -n 10 > "$OUT"
[ -s "$OUT" ] || warn "${MODEL} returned no bullet points"
echo "release highlights: $(wc -l < "$OUT") points from ${MODEL}"
