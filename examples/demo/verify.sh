#!/usr/bin/env bash
# Runs the demo and fails if the sequence of verdicts changes.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
want='NO DENY DENY DENY ASK DENY ASK '
output=$("$here/demo.sh") || {
  rc=$?
  printf '%s\n' "$output"
  exit "$rc"
}
printf '%s\n' "$output"

got=$(printf '%s\n' "$output" | grep -E '^(DENY|ASK|NO DECISION)' | cut -d' ' -f1 | cut -d: -f1 | tr '\n' ' ')
if [ "$got" != "$want" ]; then
  echo "verdicts: got '$got', want '$want'" >&2
  exit 1
fi
echo "PASS: verdicts match"
