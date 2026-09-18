#!/usr/bin/env bash
set -euo pipefail

release="${1:?release id is required}"
root="${CONTENT_AGENT_DEPLOY_ROOT:-/home/jinzhikang/content-agent-demo}"
release_dir="$root/releases/$release"
backup="$root/backups/content_agent-pre-$release.db"
runtime_env="$root/config/sdk-runtime.env"
old_release="$(readlink -f "$root/current" || true)"

for path in \
  "$release_dir/content-agent-server" \
  "$release_dir/sidecar/scripts/serve.py" \
  "$release_dir/serve-test-frontend.mjs" \
  "$backup" \
  "$root/config/demo.env"; do
  test -e "$path"
done

if [ ! -f "$runtime_env" ]; then
  token="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
  umask 077
  printf 'CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN=%s\n' "$token" > "$runtime_env"
fi

set -a
# Existing provider credentials remain on the test machine and never enter a release archive.
source "$root/config/demo.env"
source "$runtime_env"
set +a

export CONTENT_AGENT_CONTROL_MODEL_ENDPOINT="https://api.example.com/v1"
export CONTENT_AGENT_CONTROL_MODEL_MODEL="gpt-5p6-terra"
export CONTENT_AGENT_CONTROL_MODEL_MAX_OUTPUT_TOKENS="65536"
export CONTENT_AGENT_SIDECAR_MAX_OUTPUT_TOKENS="65536"
export CONTENT_AGENT_SIDECAR_RUN_TIMEOUT_SECONDS="420"
export CONTENT_AGENT_SIDECAR_VIDEO_TASK_TIMEOUT_SECONDS="1200"
export CONTENT_AGENT_BACKEND_BASE_URL="http://127.0.0.1:8850"
export CONTENT_AGENT_BACKEND_URL="http://127.0.0.1:8850"
export CONTENT_AGENT_OPENAI_SIDECAR_URL="http://127.0.0.1:8871"
export CONTENT_AGENT_OPENAI_SIDECAR_TIMEOUT_SECONDS="420"
export CONTENT_AGENT_SDK_AGENT_ENABLED="true"
export CONTENT_AGENT_SDK_CANARY_WORKSPACES="${CONTENT_AGENT_SDK_CANARY_WORKSPACES:-}"
export CONTENT_AGENT_RELEASE_ID="$release"
export OPENAI_AGENTS_TRACE_INCLUDE_SENSITIVE_DATA="0"
export OPENAI_AGENTS_DONT_LOG_MODEL_DATA="1"
export OPENAI_AGENTS_DONT_LOG_TOOL_DATA="1"
export CONTENT_AGENT_SIDECAR_PORT="8871"
export CONTENT_AGENT_SIDECAR_SESSION_DB_PATH="$root/data/openai-agents-sessions.db"
export CONTENT_AGENT_SIDECAR_TASK_WORKER_ENABLED="true"
export CONTENT_AGENT_SIDECAR_TASK_WORKER_CONCURRENCY="4"
export CONTENT_AGENT_SIDECAR_TASK_WORKER_LEASE_SECONDS="1500"
export CONTENT_AGENT_FFMPEG_COMMAND="$root/tools/ffmpeg-static/ffmpeg"
export CONTENT_AGENT_FFPROBE_COMMAND="$root/tools/ffmpeg-static/ffprobe"
export PYTHONPATH="$release_dir/sidecar/src"
export NO_PROXY="127.0.0.1,localhost"
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy

sidecar_pid=""
backend_pid=""
frontend_pid=""
old_backend_stopped="false"
old_sidecar_stopped="false"
old_frontend_stopped="false"

stop_pid() {
  local pid="${1:-}"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 20); do
      kill -0 "$pid" 2>/dev/null || return 0
      sleep 0.25
    done
    kill -9 "$pid" 2>/dev/null || true
  fi
}

restore_old_backend() {
  stop_pid "$frontend_pid"
  stop_pid "$backend_pid"
  stop_pid "$sidecar_pid"
  if [ "$old_backend_stopped" = "true" ] && [ -x "$old_release/content-agent-server" ]; then
    python3 - "$backup" "$root/data/content_agent.db" <<'PY'
import sqlite3, sys
source, target = sys.argv[1:]
with sqlite3.connect(source) as src, sqlite3.connect(target) as dst:
    src.backup(dst)
PY
    nohup "$old_release/content-agent-server" \
      -project-root "$old_release" \
      -database "$root/data/content_agent.db" \
      -listen 127.0.0.1:8850 \
      >> "$root/logs/server.log" 2>&1 &
    printf '%s\n' "$!" > "$root/server.pid"
  fi
  if [ "$old_sidecar_stopped" = "true" ] && [ -f "$old_release/sidecar/scripts/serve.py" ]; then
    PYTHONPATH="$old_release/sidecar/src" nohup "$root/venv-sdk/bin/python" "$old_release/sidecar/scripts/serve.py" \
      >> "$root/logs/sidecar-sdk.log" 2>&1 &
    printf '%s\n' "$!" > "$root/sidecar-sdk.pid"
  fi
  if [ "$old_frontend_stopped" = "true" ] && [ -f "$old_release/serve-test-frontend.mjs" ]; then
    CONTENT_AGENT_FRONTEND_DIST="$old_release/frontend-dist" nohup node "$old_release/serve-test-frontend.mjs" \
      >> "$root/logs/frontend-sdk.log" 2>&1 &
    printf '%s\n' "$!" > "$root/frontend-sdk.pid"
  fi
}
trap restore_old_backend ERR

old_sidecar_pid="$(ss -ltnp 2>/dev/null | grep ':8871' | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -n1)"
if [ -n "$old_sidecar_pid" ]; then
  old_sidecar_cmd="$(tr '\0' ' ' < "/proc/$old_sidecar_pid/cmdline" 2>/dev/null || true)"
  case "$old_sidecar_cmd" in
    *"$root"/releases/*/sidecar/scripts/serve.py*) ;;
    *) echo "Refusing to stop unexpected 8871 process: $old_sidecar_cmd" >&2; exit 1 ;;
  esac
  stop_pid "$old_sidecar_pid"
  old_sidecar_stopped="true"
fi

nohup "$root/venv-sdk/bin/python" "$release_dir/sidecar/scripts/serve.py" \
  >> "$root/logs/sidecar-sdk.log" 2>&1 &
sidecar_pid="$!"
printf '%s\n' "$sidecar_pid" > "$root/sidecar-sdk.pid"

for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8871/healthz | grep -q '"status":"ok"'; then break; fi
  sleep 0.5
done
curl -fsS http://127.0.0.1:8871/healthz | grep -q '"write_tools_enabled":true'

old_backend_pid="$(ss -ltnp 2>/dev/null | grep '127.0.0.1:8850' | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -n1)"
if [ -n "$old_backend_pid" ]; then
  old_exe="$(readlink -f "/proc/$old_backend_pid/exe" || true)"
  case "$old_exe" in
    "$root"/releases/*/content-agent-server) ;;
    *) echo "Refusing to stop unexpected 8850 process: $old_exe" >&2; exit 1 ;;
  esac
  stop_pid "$old_backend_pid"
  old_backend_stopped="true"
fi

nohup "$release_dir/content-agent-server" \
  -project-root "$release_dir" \
  -database "$root/data/content_agent.db" \
  -listen 127.0.0.1:8850 \
  >> "$root/logs/server-sdk.log" 2>&1 &
backend_pid="$!"
printf '%s\n' "$backend_pid" > "$root/server.pid"

for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8850/healthz | grep -q '"status":"ok"'; then break; fi
  sleep 0.5
done
curl -fsS http://127.0.0.1:8850/healthz | grep -q '"runtime_available":true'

export CONTENT_AGENT_FRONTEND_HOST="0.0.0.0"
export CONTENT_AGENT_FRONTEND_PORT="8860"
export CONTENT_AGENT_FRONTEND_DIST="$release_dir/frontend-dist"
old_frontend_pid="$(ss -ltnp 2>/dev/null | grep ':8860' | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -n1)"
if [ -n "$old_frontend_pid" ]; then
  old_frontend_cmd="$(tr '\0' ' ' < "/proc/$old_frontend_pid/cmdline" 2>/dev/null || true)"
  case "$old_frontend_cmd" in
    *"$root"/releases/*/serve-test-frontend.mjs*) ;;
    *) echo "Refusing to stop unexpected 8860 process: $old_frontend_cmd" >&2; exit 1 ;;
  esac
  stop_pid "$old_frontend_pid"
  old_frontend_stopped="true"
fi
nohup node "$release_dir/serve-test-frontend.mjs" \
  >> "$root/logs/frontend-sdk.log" 2>&1 &
frontend_pid="$!"
printf '%s\n' "$frontend_pid" > "$root/frontend-sdk.pid"

for _ in $(seq 1 40); do
  if curl -fsS http://127.0.0.1:8860/ >/dev/null; then break; fi
  sleep 0.5
done
curl -fsS http://127.0.0.1:8860/ >/dev/null
curl -fsS http://127.0.0.1:8860/healthz | grep -q '"status":"ok"'

ln -sfn "$release_dir" "$root/current"
trap - ERR
printf 'DEPLOYED release=%s sidecar=%s backend=%s frontend=%s\n' \
  "$release" "$sidecar_pid" "$backend_pid" "$frontend_pid"
