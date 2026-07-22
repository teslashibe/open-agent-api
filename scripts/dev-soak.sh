#!/usr/bin/env bash
set -euo pipefail

api_url="${API_URL:-http://127.0.0.1:8088}"
api_url="${api_url%/}"
soak_label="${SOAK_LABEL:-dev}"
duration_input="${SOAK_DURATION:-30m}"
interval_seconds="${SOAK_INTERVAL_SECONDS:-5}"
request_timeout_seconds="${SOAK_REQUEST_TIMEOUT_SECONDS:-120}"
max_failures="${SOAK_MAX_FAILURES:-0}"
iteration_limit="${SOAK_ITERATIONS:-0}"
model="${SOAK_MODEL:-gpt-5.6-sol}"

case "$soak_label" in
  *[!A-Za-z0-9_.-]*|'')
    echo "soak configuration error: SOAK_LABEL contains unsupported characters" >&2
    exit 2
    ;;
esac
case "$model" in
  *[!A-Za-z0-9._:-]*|'')
    echo "soak configuration error: SOAK_MODEL contains unsupported characters" >&2
    exit 2
    ;;
esac

require_nonnegative_integer() {
  local name="$1"
  local value="$2"
  case "$value" in
    ''|*[!0-9]*)
      echo "soak configuration error: ${name} must be a non-negative integer" >&2
      exit 2
      ;;
  esac
}

duration_seconds() {
  local raw="$1"
  local magnitude
  local multiplier
  case "$raw" in
    *h) magnitude="${raw%h}"; multiplier=3600 ;;
    *m) magnitude="${raw%m}"; multiplier=60 ;;
    *s) magnitude="${raw%s}"; multiplier=1 ;;
    *) magnitude="$raw"; multiplier=1 ;;
  esac
  require_nonnegative_integer SOAK_DURATION "$magnitude"
  echo "$((magnitude * multiplier))"
}

duration_seconds_value="$(duration_seconds "$duration_input")"
require_nonnegative_integer SOAK_DURATION "$duration_seconds_value"
require_nonnegative_integer SOAK_INTERVAL_SECONDS "$interval_seconds"
require_nonnegative_integer SOAK_REQUEST_TIMEOUT_SECONDS "$request_timeout_seconds"
require_nonnegative_integer SOAK_MAX_FAILURES "$max_failures"
require_nonnegative_integer SOAK_ITERATIONS "$iteration_limit"

if [ "$duration_seconds_value" -eq 0 ] && [ "$iteration_limit" -eq 0 ]; then
  echo "soak configuration error: set a positive SOAK_DURATION or SOAK_ITERATIONS" >&2
  exit 2
fi
if [ "$request_timeout_seconds" -eq 0 ]; then
  echo "soak configuration error: SOAK_REQUEST_TIMEOUT_SECONDS must be positive" >&2
  exit 2
fi

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/codex-chat-api-soak.XXXXXX")"
cleanup() {
  rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

curl_config="$work_dir/curl.conf"
: > "$curl_config"
chmod 600 "$curl_config"
if [ -n "${GATEWAY_BEARER_SECRET_FILE:-}" ]; then
  if [ -n "${GATEWAY_BEARER_SECRET:-}" ]; then
    echo "soak configuration error: set only one gateway bearer source" >&2
    exit 2
  fi
  if [ ! -r "$GATEWAY_BEARER_SECRET_FILE" ]; then
    echo "soak configuration error: bearer secret file is not readable" >&2
    exit 2
  fi
  bearer_secret="$(<"$GATEWAY_BEARER_SECRET_FILE")"
elif [ -n "${GATEWAY_BEARER_SECRET:-}" ]; then
  bearer_secret="$GATEWAY_BEARER_SECRET"
else
  bearer_secret=""
fi
if [ -n "$bearer_secret" ]; then
  if [[ "$bearer_secret" == *$'\n'* || "$bearer_secret" == *$'\r'* || "$bearer_secret" == *'"'* || "$bearer_secret" == *'\'* ]]; then
    echo "soak configuration error: bearer secret contains unsupported characters" >&2
    exit 2
  fi
  printf 'header = "Authorization: Bearer %s"\n' "$bearer_secret" > "$curl_config"
fi
unset bearer_secret

http_request() {
  local method="$1"
  local path="$2"
  local request_body="${3:-}"
  local response_file="$4"
  local status_file="$5"
  local -a args=(
    --silent
    --show-error
    --max-time "$request_timeout_seconds"
    --output "$response_file"
    --write-out '%{http_code}'
    --request "$method"
  )
  if [ -s "$curl_config" ]; then
    args+=(--config "$curl_config")
  fi
  if [ -n "$request_body" ]; then
    args+=(--header 'content-type: application/json' --data "$request_body")
  fi
  if ! curl "${args[@]}" "${api_url}${path}" > "$status_file" 2>/dev/null; then
    return 1
  fi
  [ "$(<"$status_file")" = "200" ]
}

probe_health() {
  local endpoint="$1"
  http_request GET "$endpoint" "" "$work_dir/health.body" "$work_dir/health.status"
}

echo "soak_start target=${soak_label} duration=${duration_input} interval_seconds=${interval_seconds}"
if ! probe_health /health/live; then
  echo "soak_preflight_failed target=${soak_label} endpoint=live" >&2
  exit 1
fi
if ! probe_health /health/ready; then
  echo "soak_preflight_failed target=${soak_label} endpoint=ready" >&2
  exit 1
fi
echo "soak_preflight_passed target=${soak_label}"

start_epoch="$(date +%s)"
deadline_epoch="$((start_epoch + duration_seconds_value))"
iterations=0
nonstream_passes=0
stream_passes=0
failures=0

while :; do
  now_epoch="$(date +%s)"
  if [ "$iteration_limit" -gt 0 ]; then
    [ "$iterations" -lt "$iteration_limit" ] || break
  elif [ "$now_epoch" -ge "$deadline_epoch" ]; then
    break
  fi

  iterations="$((iterations + 1))"
  if ! probe_health /health/ready; then
    failures="$((failures + 1))"
    echo "soak_check_failed target=${soak_label} iteration=${iterations} check=readiness" >&2
  fi

  nonstream_body="{\"model\":\"${model}\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: soak ok\"}]}"
  if http_request POST /v1/chat/completions "$nonstream_body" "$work_dir/nonstream.body" "$work_dir/nonstream.status" \
    && grep -q '"choices"' "$work_dir/nonstream.body"; then
    nonstream_passes="$((nonstream_passes + 1))"
  else
    failures="$((failures + 1))"
    echo "soak_check_failed target=${soak_label} iteration=${iterations} check=completion" >&2
  fi

  stream_body="{\"model\":\"${model}\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: stream soak ok\"}]}"
  if http_request POST /v1/chat/completions "$stream_body" "$work_dir/stream.body" "$work_dir/stream.status" \
    && grep -q '^data: \[DONE\]' "$work_dir/stream.body"; then
    stream_passes="$((stream_passes + 1))"
  else
    failures="$((failures + 1))"
    echo "soak_check_failed target=${soak_label} iteration=${iterations} check=stream" >&2
  fi

  if [ "$failures" -gt "$max_failures" ]; then
    break
  fi
  if [ "$interval_seconds" -gt 0 ]; then
    sleep "$interval_seconds"
  fi
done

elapsed_seconds="$(( $(date +%s) - start_epoch ))"
echo "soak_summary target=${soak_label} elapsed_seconds=${elapsed_seconds} iterations=${iterations} completion_passes=${nonstream_passes} stream_passes=${stream_passes} failures=${failures}"
if [ "$failures" -gt "$max_failures" ]; then
  exit 1
fi
if [ "$iterations" -eq 0 ] || [ "$nonstream_passes" -eq 0 ] || [ "$stream_passes" -eq 0 ]; then
  exit 1
fi
echo "soak_passed target=${soak_label}"
