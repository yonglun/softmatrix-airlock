#!/usr/bin/env bash
# 实测 LiteLLM 开源版管理端点在无 License 下的可用性。
# 每个端点打印：HTTP 状态码 + 响应体前 300 字符。
set -uo pipefail

BASE="${LITELLM_BASE:-http://localhost:4000}"
KEY="${LITELLM_MASTER_KEY:-sk-airlock-master-dev-only}"

call() {
  local label="$1" method="$2" path="$3" body="${4:-}"
  echo "════════════════════════════════════════════════"
  echo "▶ ${label}"
  echo "  ${method} ${path}"
  local out code
  if [ -n "$body" ]; then
    out=$(curl -sS -w '\n%{http_code}' -X "$method" "${BASE}${path}" \
      -H "Authorization: Bearer ${KEY}" \
      -H "Content-Type: application/json" \
      -d "$body" 2>&1)
  else
    out=$(curl -sS -w '\n%{http_code}' -X "$method" "${BASE}${path}" \
      -H "Authorization: Bearer ${KEY}" 2>&1)
  fi
  code=$(echo "$out" | tail -n1)
  echo "  HTTP ${code}"
  echo "  $(echo "$out" | sed '$d' | head -c 300)"
  echo
}

TS=$(date +%s)

call "组织：创建" POST /organization/new \
  "{\"organization_alias\":\"airlock-probe-${TS}\",\"max_budget\":100,\"budget_duration\":\"30d\"}"

call "组织：列表" GET /organization/list

call "团队：创建" POST /team/new \
  "{\"team_alias\":\"probe-team-${TS}\",\"max_budget\":50,\"budget_duration\":\"30d\",\"tpm_limit\":1000,\"rpm_limit\":60}"

call "用户：创建" POST /user/new \
  "{\"user_email\":\"probe-${TS}@example.com\",\"max_budget\":20,\"budget_duration\":\"30d\"}"

call "密钥：创建（带预算与模型白名单）" POST /key/generate \
  "{\"max_budget\":10,\"budget_duration\":\"30d\",\"models\":[\"deepseek-chat\"],\"tpm_limit\":500,\"rpm_limit\":30,\"max_parallel_requests\":5}"

call "支出：按标签聚合" GET "/spend/tags"
call "支出：日志" GET "/spend/logs"
call "模型：列表" GET /model/info
