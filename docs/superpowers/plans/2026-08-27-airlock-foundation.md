# Airlock P0 + P1.1 地基实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 先用实测替代推断确认 LiteLLM 开源版的真实能力边界，再交付 Airlock 的 Go 单仓骨架与「透明直通 Edge」——用 `ak-` 密钥鉴权、转发（含流式）到 LiteLLM、按自有定价表核算成本、把用量与审计写入 ClickHouse。

**Architecture:** Edge 是 Go 写的内联反向代理，P1.1 阶段只做直通：校验 `ak-` 密钥 → 从 Postgres 取出映射的上游 LiteLLM 密钥 → 替换 Authorization 头转发 → 从响应中提取 usage → 按定价表算成本 → 异步批量写 ClickHouse。LiteLLM 以容器运行，只做供应商适配，持有唯一 master key。定价、密钥、加密、用量各自独立成包，互不依赖对方的内部实现。

**Tech Stack:** Go 1.24（Edge + 控制面）、PostgreSQL 17、ClickHouse 25.3、LiteLLM `main-stable`、goose（迁移）、pgx/v5、clickhouse-go/v2、testify、Docker Compose。

**分两部分：**
- **Part A（Task 1–5）· P0 可行性验证** — 调研性质，产出结论文档而非生产代码，**不走 TDD**。
- **Part B（Task 6–18）· P1.1 骨架与透明直通 Edge** — 生产代码，**严格 TDD**。

Part B 不依赖 Part A 的结论（P0 的发现影响的是 P1.3 配额映射，不影响骨架与直通 Edge）。但 Part A 必须先做完，因为它可能触发 Roadmap 的分支决策。

---

## 文件结构

本计划创建/修改的全部文件及其职责：

```
airlock/
├── go.mod                              module github.com/softmatrix/airlock
├── Makefile                            build / test / lint / migrate 入口
├── .env.example                        环境变量样例
├── cmd/
│   └── edge/main.go                    Edge 进程入口：装配依赖、启动 HTTP
├── internal/
│   ├── config/config.go                环境变量加载，无第三方依赖
│   ├── cryptobox/                      对称加密（上游密钥落库前加密）
│   │   ├── cipher.go                   AES-256-GCM 封装
│   │   └── cipher_test.go
│   ├── apikey/                         Airlock 自有的 ak- 密钥
│   │   ├── key.go                      生成 / 哈希 / 前缀 / 格式校验
│   │   ├── key_test.go
│   │   ├── store.go                    Postgres 查询：按哈希取密钥
│   │   └── store_test.go
│   ├── pricing/                        定价模型与成本核算
│   │   ├── model.go                    Micro / Tier / ModelPrice / Usage 类型与 Cost()
│   │   ├── model_test.go
│   │   ├── table.go                    按 (provider, model, 时刻) 查生效价格
│   │   └── table_test.go
│   ├── usage/                          用量记录与写入
│   │   ├── record.go                   Record 类型
│   │   ├── batch.go                    异步批量写入器
│   │   ├── batch_test.go
│   │   └── clickhouse.go               ClickHouse Sink 实现
│   ├── openai/                         OpenAI 协议报文解析
│   │   ├── usage.go                    从响应体 / SSE 帧提取 usage
│   │   └── usage_test.go
│   └── edge/
│       ├── auth.go                     Bearer 解析与密钥校验中间件
│       ├── auth_test.go
│       ├── proxy.go                    非流式转发
│       ├── proxy_test.go
│       ├── stream.go                   SSE 流式转发、usage 注入与剥离、TTFT
│       ├── stream_test.go
│       ├── server.go                   路由装配、/healthz
│       └── server_test.go
├── migrations/
│   ├── embed.go                        embed.FS + goose 运行器
│   └── 20260827000001_init.sql         orgs / api_keys / model_prices
├── deploy/
│   ├── docker-compose.yml              postgres + clickhouse + litellm
│   ├── clickhouse/init/01_usage.sql    ClickHouse 建表
│   └── litellm/config.yaml             三家供应商
├── spike/                              P0 的一次性验证代码，验收后保留作证据
│   ├── litellm_endpoints.sh            管理端点可用性实测脚本
│   └── streambuffer/main.go            流式缓冲窗口延迟原型
└── docs/superpowers/findings/
    ├── 2026-08-27-litellm-oss-capability.md    P0 任务 3 结论
    ├── 2026-08-27-org-tree-flattening.md       P0 任务 4 结论
    └── 2026-08-27-stream-buffer-latency.md     P0 任务 5 结论
```

**边界设计要点：**
- `pricing` 不知道数据库存在，只接受纯数据；`table.go` 负责从外部加载。这样成本计算逻辑可以脱离任何基础设施单测。
- `openai` 包只做协议报文解析，不知道 Airlock 的任何概念。
- `usage.BatchWriter` 依赖 `Sink` 接口而非 ClickHouse 具体实现，测试用内存 Sink。
- `edge` 依赖 `apikey.Store`、`pricing.Table`、`usage.Writer` 三个接口，全部可替换。

---

## 类型契约（跨任务必须一致）

后续任务反复引用这些签名，实现时不得改名：

```go
// internal/pricing
type Micro int64                        // 1 Micro = 1e-6 元，全程整数运算，禁止 float

type Tier struct {
    MaxInputTokens   int64  // 该档位适用的输入 token 上限；0 表示无上限
    InputPer1M       Micro
    CachedInputPer1M Micro
    OutputPer1M      Micro
    ReasoningPer1M   Micro
}

type ModelPrice struct {
    Provider      string
    Model         string
    EffectiveFrom time.Time
    Currency      string
    Tiers         []Tier   // 按 MaxInputTokens 升序，最后一档可为 0（无上限）
}

type Usage struct {
    InputTokens       int64
    CachedInputTokens int64  // 是 InputTokens 的子集
    OutputTokens      int64
    ReasoningTokens   int64  // 是 OutputTokens 的子集
}

func (p ModelPrice) SelectTier(inputTokens int64) (Tier, error)
func (p ModelPrice) Cost(u Usage) (Micro, error)

type Table interface {
    Lookup(provider, model string, at time.Time) (ModelPrice, error)
}

// internal/apikey
type Key struct {
    ID          string
    Prefix      string
    OrgID       string
    UserID      string
    UpstreamKey string      // 已解密
    Status      string      // "active" | "revoked"
    ExpiresAt   *time.Time
}

func Generate() (plaintext, hash, prefix string, err error)
func Hash(plaintext string) string
func ValidateFormat(plaintext string) error

type Store interface {
    ByHash(ctx context.Context, hash string) (*Key, error)
}

// internal/cryptobox
type Cipher struct{ /* unexported */ }
func NewCipher(key []byte) (*Cipher, error)      // key 必须 32 字节
func (c *Cipher) Encrypt(plain string) (string, error)   // 返回 base64
func (c *Cipher) Decrypt(encoded string) (string, error)

// internal/openai
type Usage struct {                      // 协议层原样结构，与 pricing.Usage 分离
    PromptTokens     int64
    CompletionTokens int64
    CachedTokens     int64
    ReasoningTokens  int64
}
func ExtractUsage(body []byte) (Usage, string, error)     // 返回 usage 与 model
func ParseSSEData(line []byte) (data []byte, ok bool)

// internal/usage
type Record struct {
    RequestID  string
    Timestamp  time.Time
    OrgID      string
    UserID     string
    KeyID      string
    Provider   string
    Model      string
    Usage      pricing.Usage
    CostMicro  pricing.Micro
    StatusCode int
    LatencyMS  int
    TTFTMS     int
    Stream     bool
    ErrorType  string
}

type Sink interface {
    InsertBatch(ctx context.Context, records []Record) error
}

type Writer interface {
    Write(r Record)
}
```

---

# Part A · P0 可行性验证

> Part A 是调研，产出结论文档。**不写测试、不走 TDD。** 每个任务的验收是「文档里有实测得到的结论」，不是「测试通过」。

---

### Task 1: 环境准备与仓库骨架

**Files:**
- Create: `.env.example`
- Create: `deploy/litellm/config.yaml`
- Create: `deploy/docker-compose.yml`

- [ ] **Step 1: 安装 Go**

```bash
brew install go
```

- [ ] **Step 2: 验证 Go 可用且版本 ≥ 1.24**

```bash
go version
```

预期输出形如 `go version go1.24.x darwin/arm64` 或更高。若低于 1.24，执行 `brew upgrade go`。

- [ ] **Step 3: 创建环境变量样例**

创建 `.env.example`：

```bash
# LiteLLM 数据面
LITELLM_MASTER_KEY=sk-airlock-master-dev-only
LITELLM_PORT=4000

# 供应商密钥（P0/P1.1 至少填一个可用的）
DEEPSEEK_API_KEY=
DASHSCOPE_API_KEY=
OPENAI_API_KEY=

# PostgreSQL
POSTGRES_USER=airlock
POSTGRES_PASSWORD=airlock_dev
POSTGRES_DB=airlock
POSTGRES_PORT=5432

# ClickHouse
CLICKHOUSE_USER=airlock
CLICKHOUSE_PASSWORD=airlock_dev
CLICKHOUSE_DB=airlock
CLICKHOUSE_PORT=9000

# Airlock Edge
EDGE_LISTEN_ADDR=:8080
EDGE_UPSTREAM_BASE_URL=http://localhost:4000
# 32 字节的 base64；生产环境必须替换
AIRLOCK_ENCRYPTION_KEY=YWlybG9jay1kZXYtb25seS0zMmJ5dGUta2V5ISEhISE=
```

- [ ] **Step 4: 创建 LiteLLM 配置（三家供应商）**

创建 `deploy/litellm/config.yaml`：

```yaml
model_list:
  - model_name: deepseek-chat
    litellm_params:
      model: deepseek/deepseek-chat
      api_key: os.environ/DEEPSEEK_API_KEY

  - model_name: qwen-plus
    litellm_params:
      model: openai/qwen-plus
      api_base: https://dashscope.aliyuncs.com/compatible-mode/v1
      api_key: os.environ/DASHSCOPE_API_KEY

  - model_name: gpt-4o-mini
    litellm_params:
      model: openai/gpt-4o-mini
      api_key: os.environ/OPENAI_API_KEY

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
  database_url: postgresql://airlock:airlock_dev@postgres:5432/litellm
```

> 通义走 OpenAI 兼容端点而非 `dashscope/` 前缀，因为兼容端点的行为最稳定、不依赖 LiteLLM 的供应商适配版本。

- [ ] **Step 5: 创建 docker-compose**

创建 `deploy/docker-compose.yml`：

```yaml
services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB}
    ports:
      - "${POSTGRES_PORT}:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./postgres/init:/docker-entrypoint-initdb.d
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER}"]
      interval: 3s
      timeout: 3s
      retries: 20

  clickhouse:
    image: clickhouse/clickhouse-server:25.3
    environment:
      CLICKHOUSE_USER: ${CLICKHOUSE_USER}
      CLICKHOUSE_PASSWORD: ${CLICKHOUSE_PASSWORD}
      CLICKHOUSE_DB: ${CLICKHOUSE_DB}
      CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: "1"
    ports:
      - "${CLICKHOUSE_PORT}:9000"
      - "8123:8123"
    volumes:
      - chdata:/var/lib/clickhouse
      - ./clickhouse/init:/docker-entrypoint-initdb.d
    healthcheck:
      test: ["CMD", "clickhouse-client", "--query", "SELECT 1"]
      interval: 3s
      timeout: 3s
      retries: 20

  litellm:
    image: ghcr.io/berriai/litellm:main-stable
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      LITELLM_MASTER_KEY: ${LITELLM_MASTER_KEY}
      DEEPSEEK_API_KEY: ${DEEPSEEK_API_KEY}
      DASHSCOPE_API_KEY: ${DASHSCOPE_API_KEY}
      OPENAI_API_KEY: ${OPENAI_API_KEY}
    command: ["--config", "/app/config.yaml", "--port", "4000"]
    ports:
      - "${LITELLM_PORT}:4000"
    volumes:
      - ./litellm/config.yaml:/app/config.yaml:ro

volumes:
  pgdata:
  chdata:
```

创建 `deploy/postgres/init/01_litellm_db.sql`（LiteLLM 需要自己的库）：

```sql
CREATE DATABASE litellm;
```

- [ ] **Step 6: 提交**

```bash
git add .env.example deploy/
git commit -m "chore: 环境变量样例、LiteLLM 三供应商配置与 compose 编排"
```

---

### Task 2: 启动 LiteLLM 开源容器（无 License）

**Files:**
- 无新增文件

- [ ] **Step 1: 准备本地 .env**

```bash
cp .env.example .env
```

然后编辑 `.env`，至少填入一个可用的供应商密钥（推荐 `DEEPSEEK_API_KEY`，最便宜）。

- [ ] **Step 2: 启动 postgres 与 litellm**

```bash
docker compose --env-file .env -f deploy/docker-compose.yml up -d postgres litellm
```

- [ ] **Step 3: 确认 LiteLLM 健康且未加载任何 License**

```bash
curl -s http://localhost:4000/health/liveliness
```

预期输出：`"I'm alive!"`

```bash
docker compose -f deploy/docker-compose.yml logs litellm 2>&1 | grep -i -E "premium|enterprise|license" || echo "日志中无 License 相关记录"
```

**记录这条输出**，Task 3 的结论文档需要引用它来证明测试是在无 License 状态下进行的。

- [ ] **Step 4: 确认能正常推理**

```bash
curl -s http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer sk-airlock-master-dev-only" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"说一个字"}],"max_tokens":10}' | jq .
```

预期：返回带 `choices` 与 `usage` 的正常响应。若失败，先解决供应商密钥问题再继续——**后续所有任务都依赖这一步成立**。

---

### Task 3: 实测管理端点在无 License 下的可用性

这是整个 Roadmap 的地基验证。**结论必须来自实测，不得引用文档。**

**Files:**
- Create: `spike/litellm_endpoints.sh`
- Create: `docs/superpowers/findings/2026-08-27-litellm-oss-capability.md`

- [ ] **Step 1: 编写实测脚本**

创建 `spike/litellm_endpoints.sh`：

```bash
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
```

```bash
chmod +x spike/litellm_endpoints.sh
```

- [ ] **Step 2: 运行实测**

```bash
./spike/litellm_endpoints.sh 2>&1 | tee /tmp/litellm-probe.txt
```

- [ ] **Step 3: 逐个端点判定并记录**

对每个端点，按输出归为三类之一：
- **可用** — HTTP 2xx 且返回了预期资源
- **被门禁** — 响应中出现 `premium`、`enterprise`、`license` 字样，或 HTTP 401/403 且提示需要企业授权
- **其他失败** — 参数错误、依赖缺失等，需区别于门禁

创建 `docs/superpowers/findings/2026-08-27-litellm-oss-capability.md`：

```markdown
# P0 结论：LiteLLM 开源版管理端点可用性实测

| 项目 | 内容 |
|---|---|
| 日期 | 2026-08-27 |
| LiteLLM 镜像 | ghcr.io/berriai/litellm:main-stable（记录 `docker inspect` 得到的具体 digest） |
| License 状态 | 未配置任何 License（证据见下方启动日志） |
| 实测脚本 | `spike/litellm_endpoints.sh` |
| 原始输出 | `/tmp/litellm-probe.txt` |

## 无 License 状态的证据

（粘贴 Task 2 Step 3 中 grep License 关键字的输出）

## 端点可用性

| 端点 | HTTP | 判定 | 备注 |
|---|---|---|---|
| POST /organization/new | | 可用 / 被门禁 / 其他失败 | |
| GET /organization/list | | | |
| POST /team/new | | | |
| POST /user/new | | | |
| POST /key/generate（带预算、白名单、限流） | | | |
| GET /spend/tags | | | |
| GET /spend/logs | | | |
| GET /model/info | | | |

## 结论与分支决策

依据 Roadmap P0 的分支决策规则：

- 全部可用 → 按 Roadmap 执行，P1.3 使用 LiteLLM 原生预算
- 部分被门禁 → P1 配额范围收缩为「仅 Key 级预算」，P2 优先级提到 P1 之后立即执行
- 大面积被门禁 → P2 提前并入 P1

**本次判定：**（填写实际结论与理由）

**对 PRD §3.3 能力归属表的影响：**（若有需要修正的行，在此列出）
```

**把实测结果填进去。** 表格里不允许留空行。

- [ ] **Step 4: 记录镜像 digest（保证结论可复现）**

```bash
docker inspect --format='{{index .RepoDigests 0}}' ghcr.io/berriai/litellm:main-stable
```

把输出填入结论文档的表头。

- [ ] **Step 5: 提交**

```bash
git add spike/litellm_endpoints.sh docs/superpowers/findings/2026-08-27-litellm-oss-capability.md
git commit -m "docs: P0 实测 LiteLLM 开源版管理端点在无 License 下的可用性"
```

---

### Task 4: 组织树拍平方案验证

Airlock 侧是任意深度组织树，LiteLLM 只有 `Organization → Team → User` 三层。本任务确定映射方案与信息损失。

**Files:**
- Create: `docs/superpowers/findings/2026-08-27-org-tree-flattening.md`

- [ ] **Step 1: 构造一棵五层测试树并映射**

以这棵树为样本（对应真实企业的常见形态）：

```
集团
└── 研发中心
    └── 平台产品部
        └── 网关组
            └── 数据面小组
```

在 LiteLLM 中按「组织 = 树根下第一层、团队 = 叶子节点」的方案创建：

```bash
KEY=sk-airlock-master-dev-only
BASE=http://localhost:4000

curl -sS -X POST "$BASE/organization/new" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"organization_alias":"研发中心","max_budget":1000,"budget_duration":"30d"}' | jq .

curl -sS -X POST "$BASE/team/new" \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"team_alias":"数据面小组","max_budget":100,"budget_duration":"30d"}' | jq .
```

- [ ] **Step 2: 验证中间层预算能否强制执行**

关键问题：「平台产品部」和「网关组」这两个中间层的预算，在 LiteLLM 侧有没有承载体。

尝试为中间层也建 team，观察 LiteLLM 是否支持 team 嵌套：

```bash
curl -sS "$BASE/team/list" -H "Authorization: Bearer $KEY" | jq '.[0] | keys'
```

检查返回的字段里是否存在表达父子关系的字段（如 `parent_team_id`）。**记录实际字段列表。**

- [ ] **Step 3: 测量信息损失**

回答并记录这三个问题：
1. 五层树映射到 LiteLLM 后，有几层的预算能被**强制执行**？
2. 无法强制执行的层，Airlock 侧能否通过聚合子节点用量做到**准确展示**？
3. 如果客户给「平台产品部」设了预算，超限时会发生什么？（LiteLLM 不知道这层，所以不会拦截——确认这一点）

- [ ] **Step 4: 写结论文档**

创建 `docs/superpowers/findings/2026-08-27-org-tree-flattening.md`：

```markdown
# P0 结论：组织树拍平方案

| 项目 | 内容 |
|---|---|
| 日期 | 2026-08-27 |
| 背景 | Airlock 支持任意深度组织树，LiteLLM 固定为 Organization → Team → User 三层 |

## LiteLLM 团队实体的实际字段

（粘贴 `/team/list` 返回的字段列表，说明是否存在父子关系字段）

## 采用的映射方案

（描述最终方案，例如：Airlock 组织树的根下第一层映射为 LiteLLM Organization，
每个持有 Key 的叶子节点映射为 LiteLLM Team，中间层不在 LiteLLM 侧建实体）

## 信息损失

| 树的层级 | 预算能否强制执行 | Airlock 能否准确展示 | 说明 |
|---|---|---|---|
| 第 1 层（集团） | | | |
| 第 2 层（研发中心） | | | |
| 第 3 层（平台产品部） | | | |
| 第 4 层（网关组） | | | |
| 第 5 层（数据面小组） | | | |

## 对客户沟通的影响

P1 阶段面对「我们部门树有五层」这个问题时的标准回答：（写出实际话术）

## 对 P2 的输入

P2 自建配额引擎时，Edge 需要承担的判定职责：（列出）
```

- [ ] **Step 5: 提交**

```bash
git add docs/superpowers/findings/2026-08-27-org-tree-flattening.md
git commit -m "docs: P0 组织树拍平方案与信息损失评估"
```

---

### Task 5: 流式缓冲窗口延迟原型

出向护栏（P4）要在 SSE 流上做内容检测，必然引入缓冲。本任务提前测出缓冲对首字延迟的真实影响，避免 P4 才发现方案不可行。

**Files:**
- Create: `spike/streambuffer/main.go`
- Create: `docs/superpowers/findings/2026-08-27-stream-buffer-latency.md`

- [ ] **Step 1: 初始化 Go 模块**

```bash
go mod init github.com/softmatrix/airlock
go mod edit -go=1.24
```

- [ ] **Step 2: 编写延迟测量原型**

创建 `spike/streambuffer/main.go`：

```go
// 测量 SSE 流式响应在不同缓冲窗口下的首字延迟（TTFT）。
// 缓冲窗口 = 在向下游吐出内容前，先攒够多少个字符。
// 用法：go run ./spike/streambuffer -windows 0,32,128,512 -runs 5
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	base := flag.String("base", "http://localhost:4000", "LiteLLM 地址")
	key := flag.String("key", "sk-airlock-master-dev-only", "master key")
	model := flag.String("model", "deepseek-chat", "模型名")
	windowsArg := flag.String("windows", "0,32,128,512", "缓冲窗口字符数，逗号分隔")
	runs := flag.Int("runs", 5, "每个窗口重复次数")
	flag.Parse()

	var windows []int
	for _, s := range strings.Split(*windowsArg, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			fmt.Fprintf(os.Stderr, "无效的窗口值 %q: %v\n", s, err)
			os.Exit(1)
		}
		windows = append(windows, n)
	}

	fmt.Printf("%-10s %-10s %-10s %-10s %-10s\n", "窗口(字符)", "样本数", "TTFT-p50", "TTFT-p95", "TTFT-max")
	for _, w := range windows {
		var samples []time.Duration
		for i := 0; i < *runs; i++ {
			d, err := measure(*base, *key, *model, w)
			if err != nil {
				fmt.Fprintf(os.Stderr, "窗口 %d 第 %d 次失败: %v\n", w, i+1, err)
				continue
			}
			samples = append(samples, d)
		}
		if len(samples) == 0 {
			fmt.Printf("%-10d %-10s\n", w, "全部失败")
			continue
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		fmt.Printf("%-10d %-10d %-10s %-10s %-10s\n",
			w, len(samples),
			samples[pct(len(samples), 50)].Round(time.Millisecond),
			samples[pct(len(samples), 95)].Round(time.Millisecond),
			samples[len(samples)-1].Round(time.Millisecond))
	}
}

func pct(n, p int) int {
	i := n * p / 100
	if i >= n {
		i = n - 1
	}
	return i
}

// measure 发起一次流式请求，返回「攒够 window 个字符」所耗的时间。
// window = 0 表示不缓冲，第一个内容字符到达即计时结束。
func measure(base, key, model string, window int) (time.Duration, error) {
	reqBody := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "用两百字介绍一下长江。"}},
		"stream":     true,
		"max_tokens": 400,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := make([]byte, 300)
		n, _ := resp.Body.Read(body)
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body[:n])
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	accumulated := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			accumulated += len([]rune(c.Delta.Content))
		}
		if accumulated > window {
			return time.Since(start), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("流结束时仍未攒够 %d 个字符（实际 %d）", window, accumulated)
}
```

- [ ] **Step 3: 运行测量**

```bash
go run ./spike/streambuffer -windows 0,32,128,512 -runs 5 2>&1 | tee /tmp/streambuffer.txt
```

预期输出是一张四行的表，窗口越大 TTFT 越高。

- [ ] **Step 4: 写结论文档**

创建 `docs/superpowers/findings/2026-08-27-stream-buffer-latency.md`：

```markdown
# P0 结论：流式缓冲窗口对首字延迟的影响

| 项目 | 内容 |
|---|---|
| 日期 | 2026-08-27 |
| 原型 | `spike/streambuffer/main.go` |
| 模型 | （填实测用的模型） |
| 原始输出 | `/tmp/streambuffer.txt` |

## 实测数据

| 缓冲窗口（字符） | 样本数 | TTFT p50 | TTFT p95 | TTFT max |
|---|---|---|---|---|
| 0（不缓冲） | | | | |
| 32 | | | | |
| 128 | | | | |
| 512 | | | | |

## 结论

1. 相对不缓冲的基线，各窗口引入的额外首字延迟分别是：（填数值）
2. 在保持可接受体验的前提下，缓冲窗口的上限约为：（填数值）个字符
3. 该窗口大小对出向检测的影响：（说明这么小的窗口能否检出跨块的敏感内容）

## 对 P4 的输入

- 推荐的默认缓冲窗口：（填）
- 需要在 P4 进一步验证的问题：（列出，例如「窗口内检不出的跨块模式如何处理」）
- 若客户对 TTFT 极度敏感，可提供的降级选项：（例如仅对非流式请求开启出向护栏）
```

- [ ] **Step 5: 提交**

```bash
git add go.mod spike/streambuffer/main.go docs/superpowers/findings/2026-08-27-stream-buffer-latency.md
git commit -m "docs: P0 流式缓冲窗口延迟实测与 P4 输入"
```

---

### Part A 关卡

- [ ] **确认三份结论文档都已写完且无空白单元格**

```bash
ls -1 docs/superpowers/findings/
grep -nE "（填|（粘贴|（描述|（列出|（写出" docs/superpowers/findings/*.md \
  && echo "⚠️ 仍有未填写的占位符" || echo "✅ 无占位符"
```

- [ ] **确认 Task 3 的分支决策已明确写出**

若结论是「部分被门禁」或「大面积被门禁」，**停下来**，先按 Roadmap P0 的分支规则调整 Roadmap，再继续 Part B。Part B 本身不受影响，但 P1.3 的排期需要改。

---

# Part B · P1.1 骨架与透明直通 Edge

> 从这里开始**严格 TDD**：先写失败的测试，跑一次确认它失败，再写最小实现，跑一次确认通过，然后提交。

---

### Task 6: Go 模块骨架、依赖与 Makefile

**Files:**
- Modify: `go.mod`
- Create: `Makefile`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/config/config_test.go`：

```go
package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadUsesDefaultsWhenUnset(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.EdgeListenAddr)
	require.Equal(t, "http://localhost:4000", cfg.UpstreamBaseURL)
}

func TestLoadReadsEnv(t *testing.T) {
	env := map[string]string{
		"EDGE_LISTEN_ADDR":       ":9090",
		"EDGE_UPSTREAM_BASE_URL": "http://litellm:4000",
		"POSTGRES_DSN":           "postgres://u:p@h:5432/db",
		"CLICKHOUSE_DSN":         "clickhouse://u:p@h:9000/db",
		"AIRLOCK_ENCRYPTION_KEY": "YWlybG9jay1kZXYtb25seS0zMmJ5dGUta2V5ISEhISE=",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	require.NoError(t, err)
	require.Equal(t, ":9090", cfg.EdgeListenAddr)
	require.Equal(t, "http://litellm:4000", cfg.UpstreamBaseURL)
	require.Equal(t, "postgres://u:p@h:5432/db", cfg.PostgresDSN)
	require.Len(t, cfg.EncryptionKey, 32)
}

func TestLoadRejectsWrongLengthEncryptionKey(t *testing.T) {
	env := map[string]string{"AIRLOCK_ENCRYPTION_KEY": "c2hvcnQ="} // "short"
	_, err := Load(func(k string) string { return env[k] })
	require.Error(t, err)
	require.Contains(t, err.Error(), "32")
}
```

- [ ] **Step 2: 拉依赖并运行测试，确认失败**

```bash
go get github.com/stretchr/testify@latest
go test ./internal/config/ -v
```

预期：编译失败，`undefined: Load`。

- [ ] **Step 3: 写最小实现**

创建 `internal/config/config.go`：

```go
// Package config 从环境变量加载 Airlock 的运行配置。
// 不依赖任何第三方库，便于在测试中注入取值函数。
package config

import (
	"encoding/base64"
	"fmt"
)

// Getenv 是取环境变量的函数，测试可注入内存实现。
type Getenv func(key string) string

type Config struct {
	EdgeListenAddr  string
	UpstreamBaseURL string
	PostgresDSN     string
	ClickHouseDSN   string
	EncryptionKey   []byte
}

const encryptionKeyLen = 32

func Load(getenv Getenv) (Config, error) {
	cfg := Config{
		EdgeListenAddr:  or(getenv("EDGE_LISTEN_ADDR"), ":8080"),
		UpstreamBaseURL: or(getenv("EDGE_UPSTREAM_BASE_URL"), "http://localhost:4000"),
		PostgresDSN:     getenv("POSTGRES_DSN"),
		ClickHouseDSN:   getenv("CLICKHOUSE_DSN"),
	}

	if raw := getenv("AIRLOCK_ENCRYPTION_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return Config{}, fmt.Errorf("AIRLOCK_ENCRYPTION_KEY 不是合法的 base64: %w", err)
		}
		if len(key) != encryptionKeyLen {
			return Config{}, fmt.Errorf("AIRLOCK_ENCRYPTION_KEY 解码后必须是 %d 字节，实际 %d 字节", encryptionKeyLen, len(key))
		}
		cfg.EncryptionKey = key
	}

	return cfg, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go mod tidy
go test ./internal/config/ -v
```

预期：三个测试全部 PASS。

- [ ] **Step 5: 创建 Makefile**

创建 `Makefile`：

```makefile
.PHONY: test build lint up down migrate

test:
	go test ./... -race -count=1

build:
	go build -o bin/edge ./cmd/edge

lint:
	go vet ./...

up:
	docker compose --env-file .env -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down

migrate:
	go run ./cmd/edge -migrate-only
```

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum Makefile internal/config/
git commit -m "feat: Go 模块骨架、配置加载与 Makefile"
```

---

### Task 7: 上游密钥加密（AES-256-GCM）

上游 LiteLLM 密钥落库前必须加密——PRD 非功能需求中的「供应商凭证加密存储」。

**Files:**
- Create: `internal/cryptobox/cipher.go`
- Test: `internal/cryptobox/cipher_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/cryptobox/cipher_test.go`：

```go
package cryptobox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	plain := "sk-litellm-upstream-abc123"
	enc, err := c.Encrypt(plain)
	require.NoError(t, err)
	require.NotEqual(t, plain, enc)

	got, err := c.Decrypt(enc)
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestEncryptProducesDifferentCiphertextEachTime(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	a, err := c.Encrypt("same-input")
	require.NoError(t, err)
	b, err := c.Encrypt("same-input")
	require.NoError(t, err)

	require.NotEqual(t, a, b, "随机 nonce 应使同一明文每次产出不同密文")
}

func TestNewCipherRejectsWrongKeyLength(t *testing.T) {
	_, err := NewCipher([]byte("too-short"))
	require.Error(t, err)
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	enc, err := c.Encrypt("secret")
	require.NoError(t, err)

	tampered := "A" + enc[1:]
	_, err = c.Decrypt(tampered)
	require.Error(t, err)
}

func TestDecryptRejectsTooShortInput(t *testing.T) {
	c, err := NewCipher(testKey())
	require.NoError(t, err)

	_, err = c.Decrypt("YWJj") // "abc"，短于 nonce 长度
	require.Error(t, err)
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/cryptobox/ -v
```

预期：编译失败，`undefined: NewCipher`。

- [ ] **Step 3: 写最小实现**

创建 `internal/cryptobox/cipher.go`：

```go
// Package cryptobox 提供对称加密，用于把上游密钥等敏感值加密后落库。
package cryptobox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const keyLen = 32 // AES-256

type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("密钥必须是 %d 字节，实际 %d 字节", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建 AES cipher 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt 返回 base64(nonce || ciphertext)。
func (c *Cipher) Encrypt(plain string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成 nonce 失败: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (c *Cipher) Decrypt(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("密文不是合法的 base64: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足，无法取出 nonce")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("解密失败（密文可能被篡改）: %w", err)
	}
	return string(plain), nil
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/cryptobox/ -v
```

预期：五个测试全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/cryptobox/
git commit -m "feat: AES-256-GCM 对称加密，用于上游密钥落库"
```

---

### Task 8: ak- 密钥生成、哈希与格式校验

**Files:**
- Create: `internal/apikey/key.go`
- Test: `internal/apikey/key_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/apikey/key_test.go`：

```go
package apikey

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateProducesValidKey(t *testing.T) {
	plain, hash, prefix, err := Generate()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(plain, "ak-"), "明文必须以 ak- 开头，实际 %q", plain)
	require.Len(t, plain, 3+43, "ak- 加 32 字节 base64url 无填充后应为 46 字符")
	require.NoError(t, ValidateFormat(plain))

	require.Len(t, hash, 64, "sha256 十六进制应为 64 字符")
	require.Equal(t, Hash(plain), hash)

	require.Equal(t, plain[:12], prefix, "前缀取明文前 12 字符用于展示")
}

func TestGenerateProducesUniqueKeys(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		plain, _, _, err := Generate()
		require.NoError(t, err)
		require.False(t, seen[plain], "生成了重复密钥: %s", plain)
		seen[plain] = true
	}
}

func TestHashIsStable(t *testing.T) {
	require.Equal(t, Hash("ak-example"), Hash("ak-example"))
	require.NotEqual(t, Hash("ak-example"), Hash("ak-different"))
}

func TestValidateFormat(t *testing.T) {
	valid, _, _, err := Generate()
	require.NoError(t, err)

	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"合法密钥", valid, false},
		{"缺少前缀", strings.TrimPrefix(valid, "ak-"), true},
		{"错误前缀", "sk-" + strings.TrimPrefix(valid, "ak-"), true},
		{"长度不足", "ak-tooshort", true},
		{"空串", "", true},
		{"含非法字符", "ak-" + strings.Repeat("!", 43), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFormat(tc.input)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/apikey/ -v
```

预期：编译失败，`undefined: Generate`。

- [ ] **Step 3: 写最小实现**

创建 `internal/apikey/key.go`：

```go
// Package apikey 负责 Airlock 自有虚拟密钥（ak- 前缀）的生成、哈希与格式校验。
// 明文只在签发时返回一次，数据库只存 sha256 哈希。
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// Prefix 是所有 Airlock 密钥的固定前缀。
	Prefix = "ak-"
	// randomBytes 是密钥随机部分的字节数。
	randomBytes = 32
	// bodyLen 是 32 字节经 base64url 无填充编码后的长度。
	bodyLen = 43
	// PrefixDisplayLen 是存库用于展示的前缀长度（含 "ak-"）。
	PrefixDisplayLen = 12
)

var ErrMalformed = errors.New("密钥格式非法")

// Generate 生成一个新密钥，返回明文、sha256 哈希与展示前缀。
func Generate() (plaintext, hash, prefix string, err error) {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("读取随机数失败: %w", err)
	}
	plaintext = Prefix + base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, Hash(plaintext), plaintext[:PrefixDisplayLen], nil
}

// Hash 返回密钥明文的 sha256 十六进制摘要。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// ValidateFormat 在查库之前先做廉价的格式校验，挡掉明显非法的输入。
func ValidateFormat(plaintext string) error {
	if !strings.HasPrefix(plaintext, Prefix) {
		return fmt.Errorf("%w: 缺少 %q 前缀", ErrMalformed, Prefix)
	}
	body := strings.TrimPrefix(plaintext, Prefix)
	if len(body) != bodyLen {
		return fmt.Errorf("%w: 主体长度应为 %d，实际 %d", ErrMalformed, bodyLen, len(body))
	}
	if _, err := base64.RawURLEncoding.DecodeString(body); err != nil {
		return fmt.Errorf("%w: 主体不是合法的 base64url", ErrMalformed)
	}
	return nil
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/apikey/ -v
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/apikey/
git commit -m "feat: ak- 虚拟密钥的生成、哈希与格式校验"
```

---

### Task 9: 定价模型——档位选择与成本计算

这是 P1.1 最重要的一件事。国产模型的计价规则复杂，模型定错会让全部历史账单失真。

**Files:**
- Create: `internal/pricing/model.go`
- Test: `internal/pricing/model_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/pricing/model_test.go`：

```go
package pricing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// flatPrice 构造一个单档位价格：输入 2 元/百万，输出 8 元/百万，
// 缓存命中 0.5 元/百万，思考 token 与输出同价。
func flatPrice() ModelPrice {
	return ModelPrice{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Currency:      "CNY",
		Tiers: []Tier{
			{
				MaxInputTokens:   0, // 无上限
				InputPer1M:       2_000_000,
				CachedInputPer1M: 500_000,
				OutputPer1M:      8_000_000,
				ReasoningPer1M:   8_000_000,
			},
		},
	}
}

// tieredPrice 构造按输入长度分档的价格，模拟通义等国产模型的阶梯定价。
func tieredPrice() ModelPrice {
	return ModelPrice{
		Provider:      "dashscope",
		Model:         "qwen-plus",
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Currency:      "CNY",
		Tiers: []Tier{
			{MaxInputTokens: 128_000, InputPer1M: 800_000, CachedInputPer1M: 160_000, OutputPer1M: 2_000_000, ReasoningPer1M: 2_000_000},
			{MaxInputTokens: 0, InputPer1M: 2_400_000, CachedInputPer1M: 480_000, OutputPer1M: 6_000_000, ReasoningPer1M: 6_000_000},
		},
	}
}

func TestCostSimpleInputOutput(t *testing.T) {
	p := flatPrice()
	// 100 万输入 + 100 万输出 = 2 元 + 8 元 = 10 元 = 10_000_000 Micro
	got, err := p.Cost(Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	require.NoError(t, err)
	require.Equal(t, Micro(10_000_000), got)
}

func TestCostChargesCachedTokensAtCachedRate(t *testing.T) {
	p := flatPrice()
	// 100 万输入中 60 万命中缓存：
	//   未命中 40 万 * 2元/百万 = 0.8 元
	//   命中   60 万 * 0.5元/百万 = 0.3 元
	//   合计 1.1 元 = 1_100_000 Micro
	got, err := p.Cost(Usage{InputTokens: 1_000_000, CachedInputTokens: 600_000})
	require.NoError(t, err)
	require.Equal(t, Micro(1_100_000), got)
}

func TestCostChargesReasoningTokensSeparately(t *testing.T) {
	p := flatPrice()
	p.Tiers[0].ReasoningPer1M = 16_000_000 // 思考 token 单独定价：16 元/百万

	// 100 万输出中 25 万是思考 token：
	//   普通输出 75 万 * 8元/百万  = 6 元
	//   思考     25 万 * 16元/百万 = 4 元
	//   合计 10 元
	got, err := p.Cost(Usage{OutputTokens: 1_000_000, ReasoningTokens: 250_000})
	require.NoError(t, err)
	require.Equal(t, Micro(10_000_000), got)
}

func TestCostZeroUsageIsZero(t *testing.T) {
	got, err := flatPrice().Cost(Usage{})
	require.NoError(t, err)
	require.Equal(t, Micro(0), got)
}

func TestSelectTierPicksByInputTokens(t *testing.T) {
	p := tieredPrice()

	low, err := p.SelectTier(100_000)
	require.NoError(t, err)
	require.Equal(t, Micro(800_000), low.InputPer1M)

	boundary, err := p.SelectTier(128_000)
	require.NoError(t, err)
	require.Equal(t, Micro(800_000), boundary.InputPer1M, "边界值应落在第一档（<=）")

	high, err := p.SelectTier(128_001)
	require.NoError(t, err)
	require.Equal(t, Micro(2_400_000), high.InputPer1M)
}

func TestCostUsesSelectedTier(t *testing.T) {
	p := tieredPrice()
	// 20 万输入落在第二档：20 万 * 2.4元/百万 = 0.48 元 = 480_000 Micro
	got, err := p.Cost(Usage{InputTokens: 200_000})
	require.NoError(t, err)
	require.Equal(t, Micro(480_000), got)
}

func TestSelectTierFailsWhenNoTierMatches(t *testing.T) {
	p := ModelPrice{
		Tiers: []Tier{{MaxInputTokens: 1000, InputPer1M: 1}},
	}
	_, err := p.SelectTier(5000)
	require.Error(t, err)
}

func TestCostFailsWhenNoTiersDefined(t *testing.T) {
	_, err := ModelPrice{}.Cost(Usage{InputTokens: 1})
	require.Error(t, err)
}

func TestCostRejectsCachedExceedingInput(t *testing.T) {
	_, err := flatPrice().Cost(Usage{InputTokens: 100, CachedInputTokens: 200})
	require.Error(t, err)
	require.Contains(t, err.Error(), "缓存")
}

func TestCostRejectsReasoningExceedingOutput(t *testing.T) {
	_, err := flatPrice().Cost(Usage{OutputTokens: 100, ReasoningTokens: 200})
	require.Error(t, err)
	require.Contains(t, err.Error(), "思考")
}

func TestCostRejectsNegativeTokens(t *testing.T) {
	_, err := flatPrice().Cost(Usage{InputTokens: -1})
	require.Error(t, err)
}

func TestMicroString(t *testing.T) {
	require.Equal(t, "0.000000", Micro(0).String())
	require.Equal(t, "1.500000", Micro(1_500_000).String())
	require.Equal(t, "-0.000001", Micro(-1).String())
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/pricing/ -v
```

预期：编译失败，`undefined: ModelPrice`。

- [ ] **Step 3: 写最小实现**

创建 `internal/pricing/model.go`：

```go
// Package pricing 定义定价数据模型与成本核算。
//
// 全程整数运算，禁止使用 float——浮点误差会在千万级调用记录上累积成
// 账单对不上的问题。金额单位统一为 Micro（1e-6 元）。
package pricing

import (
	"errors"
	"fmt"
	"time"
)

// Micro 是金额单位，1 Micro = 1e-6 元。
type Micro int64

// String 以元为单位格式化，保留六位小数。
func (m Micro) String() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%06d", sign, v/1_000_000, v%1_000_000)
}

// Tier 是一个价格档位。国产模型普遍按「本次请求的输入长度」分档计价。
type Tier struct {
	// MaxInputTokens 是本档适用的输入 token 上限（含）。0 表示无上限。
	MaxInputTokens int64
	// 以下四项均为「每百万 token 的价格」，单位 Micro。
	InputPer1M       Micro
	CachedInputPer1M Micro
	OutputPer1M      Micro
	ReasoningPer1M   Micro
}

// ModelPrice 是某个模型在某个生效时间点之后的价格。
type ModelPrice struct {
	Provider      string
	Model         string
	EffectiveFrom time.Time
	Currency      string
	// Tiers 按 MaxInputTokens 升序排列，最后一档可为 0（无上限）。
	// 定价平坦的模型只有一档，MaxInputTokens 为 0。
	Tiers []Tier
}

// Usage 是一次调用的 token 用量。
// CachedInputTokens 是 InputTokens 的子集，ReasoningTokens 是 OutputTokens 的子集，
// 这与 OpenAI 兼容协议中 *_tokens_details 的语义一致。
type Usage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
}

var ErrNoTier = errors.New("没有匹配的价格档位")

// SelectTier 按本次请求的输入 token 数选出适用档位。
func (p ModelPrice) SelectTier(inputTokens int64) (Tier, error) {
	if len(p.Tiers) == 0 {
		return Tier{}, fmt.Errorf("%w: %s/%s 未定义任何档位", ErrNoTier, p.Provider, p.Model)
	}
	for _, t := range p.Tiers {
		if t.MaxInputTokens == 0 || inputTokens <= t.MaxInputTokens {
			return t, nil
		}
	}
	return Tier{}, fmt.Errorf("%w: %s/%s 输入 %d tokens 超出所有档位上限",
		ErrNoTier, p.Provider, p.Model, inputTokens)
}

// Cost 计算一次调用的成本。
//
// 公式对四类 token 一视同仁，没有条件分支：
//
//	(输入 - 缓存命中) * 输入价 + 缓存命中 * 缓存价
//	  + (输出 - 思考) * 输出价 + 思考 * 思考价
//
// 供应商若不单独为缓存或思考定价，把对应价格设为与输入/输出相同即可。
func (p ModelPrice) Cost(u Usage) (Micro, error) {
	if err := u.validate(); err != nil {
		return 0, err
	}
	tier, err := p.SelectTier(u.InputTokens)
	if err != nil {
		return 0, err
	}

	billableInput := u.InputTokens - u.CachedInputTokens
	billableOutput := u.OutputTokens - u.ReasoningTokens

	total := perMillion(billableInput, tier.InputPer1M) +
		perMillion(u.CachedInputTokens, tier.CachedInputPer1M) +
		perMillion(billableOutput, tier.OutputPer1M) +
		perMillion(u.ReasoningTokens, tier.ReasoningPer1M)

	return total, nil
}

func (u Usage) validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedInputTokens < 0 || u.ReasoningTokens < 0 {
		return fmt.Errorf("token 数不能为负: %+v", u)
	}
	if u.CachedInputTokens > u.InputTokens {
		return fmt.Errorf("缓存命中 token（%d）不能超过输入 token（%d）", u.CachedInputTokens, u.InputTokens)
	}
	if u.ReasoningTokens > u.OutputTokens {
		return fmt.Errorf("思考 token（%d）不能超过输出 token（%d）", u.ReasoningTokens, u.OutputTokens)
	}
	return nil
}

// perMillion 计算 tokens 个 token 按 pricePer1M 的价格所需金额。
// 整数除法向零取整，误差最大 1 Micro（1e-6 元），可忽略。
func perMillion(tokens int64, pricePer1M Micro) Micro {
	return Micro(tokens * int64(pricePer1M) / 1_000_000)
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/pricing/ -v
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/pricing/model.go internal/pricing/model_test.go
git commit -m "feat: 定价模型——阶梯档位、缓存与思考 token 分别计价、整数金额"
```

---

### Task 10: 定价表——按生效时间查价

**Files:**
- Create: `internal/pricing/table.go`
- Test: `internal/pricing/table_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/pricing/table_test.go`：

```go
package pricing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func jan(day int) time.Time {
	return time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)
}

func priceAt(from time.Time, input Micro) ModelPrice {
	return ModelPrice{
		Provider:      "deepseek",
		Model:         "deepseek-chat",
		EffectiveFrom: from,
		Currency:      "CNY",
		Tiers:         []Tier{{MaxInputTokens: 0, InputPer1M: input, OutputPer1M: input * 4}},
	}
}

func TestMemoryTableReturnsLatestEffectivePrice(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{
		priceAt(jan(1), 2_000_000),
		priceAt(jan(10), 1_000_000), // 1 月 10 日降价
	})

	early, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.NoError(t, err)
	require.Equal(t, Micro(2_000_000), early.Tiers[0].InputPer1M, "1 月 5 日应适用旧价")

	late, err := tbl.Lookup("deepseek", "deepseek-chat", jan(15))
	require.NoError(t, err)
	require.Equal(t, Micro(1_000_000), late.Tiers[0].InputPer1M, "1 月 15 日应适用新价")
}

func TestMemoryTableBoundaryUsesNewPrice(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{
		priceAt(jan(1), 2_000_000),
		priceAt(jan(10), 1_000_000),
	})
	got, err := tbl.Lookup("deepseek", "deepseek-chat", jan(10))
	require.NoError(t, err)
	require.Equal(t, Micro(1_000_000), got.Tiers[0].InputPer1M, "生效当刻应适用新价")
}

func TestMemoryTableIgnoresInsertionOrder(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{
		priceAt(jan(10), 1_000_000),
		priceAt(jan(1), 2_000_000), // 乱序插入
	})
	got, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.NoError(t, err)
	require.Equal(t, Micro(2_000_000), got.Tiers[0].InputPer1M)
}

func TestMemoryTableFailsBeforeAnyPriceTakesEffect(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{priceAt(jan(10), 1_000_000)})
	_, err := tbl.Lookup("deepseek", "deepseek-chat", jan(5))
	require.ErrorIs(t, err, ErrPriceNotFound)
}

func TestMemoryTableFailsForUnknownModel(t *testing.T) {
	tbl := NewMemoryTable([]ModelPrice{priceAt(jan(1), 1_000_000)})
	_, err := tbl.Lookup("openai", "gpt-4o", jan(5))
	require.ErrorIs(t, err, ErrPriceNotFound)
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/pricing/ -run TestMemoryTable -v
```

预期：编译失败，`undefined: NewMemoryTable`。

- [ ] **Step 3: 写最小实现**

创建 `internal/pricing/table.go`：

```go
package pricing

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrPriceNotFound = errors.New("未找到生效价格")

// Table 按 (供应商, 模型, 时刻) 查询当时生效的价格。
type Table interface {
	Lookup(provider, model string, at time.Time) (ModelPrice, error)
}

// MemoryTable 是内存实现。价格数据量很小（几百条），全量常驻内存即可，
// 避免每次调用都查库。价格变更时重新构造一个 Table 替换。
type MemoryTable struct {
	// byModel 的 value 按 EffectiveFrom 升序排列。
	byModel map[string][]ModelPrice
}

func NewMemoryTable(prices []ModelPrice) *MemoryTable {
	byModel := make(map[string][]ModelPrice)
	for _, p := range prices {
		k := modelKey(p.Provider, p.Model)
		byModel[k] = append(byModel[k], p)
	}
	for k := range byModel {
		versions := byModel[k]
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].EffectiveFrom.Before(versions[j].EffectiveFrom)
		})
		byModel[k] = versions
	}
	return &MemoryTable{byModel: byModel}
}

// Lookup 返回 at 时刻生效的价格，即 EffectiveFrom <= at 中最晚的那条。
func (t *MemoryTable) Lookup(provider, model string, at time.Time) (ModelPrice, error) {
	versions, ok := t.byModel[modelKey(provider, model)]
	if !ok {
		return ModelPrice{}, fmt.Errorf("%w: 无 %s/%s 的任何价格记录", ErrPriceNotFound, provider, model)
	}
	var chosen *ModelPrice
	for i := range versions {
		if versions[i].EffectiveFrom.After(at) {
			break
		}
		chosen = &versions[i]
	}
	if chosen == nil {
		return ModelPrice{}, fmt.Errorf("%w: %s/%s 在 %s 尚无生效价格",
			ErrPriceNotFound, provider, model, at.Format(time.RFC3339))
	}
	return *chosen, nil
}

func modelKey(provider, model string) string {
	return provider + "/" + model
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/pricing/ -v
```

预期：Task 9 与 Task 10 的测试全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/pricing/table.go internal/pricing/table_test.go
git commit -m "feat: 定价表按生效时间查价，支持供应商调价而历史账单不变"
```

---

### Task 11: OpenAI 协议 usage 解析

**Files:**
- Create: `internal/openai/usage.go`
- Test: `internal/openai/usage_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/openai/usage_test.go`：

```go
package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractUsageFromNonStreamResponse(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-1",
		"model": "deepseek-chat",
		"choices": [{"message": {"content": "hi"}}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150
		}
	}`)

	u, model, err := ExtractUsage(body)
	require.NoError(t, err)
	require.Equal(t, "deepseek-chat", model)
	require.Equal(t, int64(100), u.PromptTokens)
	require.Equal(t, int64(50), u.CompletionTokens)
	require.Equal(t, int64(0), u.CachedTokens)
	require.Equal(t, int64(0), u.ReasoningTokens)
}

func TestExtractUsageReadsTokenDetails(t *testing.T) {
	body := []byte(`{
		"model": "qwen-plus",
		"usage": {
			"prompt_tokens": 1000,
			"completion_tokens": 400,
			"prompt_tokens_details": {"cached_tokens": 600},
			"completion_tokens_details": {"reasoning_tokens": 120}
		}
	}`)

	u, model, err := ExtractUsage(body)
	require.NoError(t, err)
	require.Equal(t, "qwen-plus", model)
	require.Equal(t, int64(1000), u.PromptTokens)
	require.Equal(t, int64(600), u.CachedTokens)
	require.Equal(t, int64(400), u.CompletionTokens)
	require.Equal(t, int64(120), u.ReasoningTokens)
}

func TestExtractUsageWhenAbsentReturnsZero(t *testing.T) {
	body := []byte(`{"model": "gpt-4o-mini", "choices": []}`)
	u, model, err := ExtractUsage(body)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", model)
	require.Equal(t, Usage{}, u)
}

func TestExtractUsageFailsOnInvalidJSON(t *testing.T) {
	_, _, err := ExtractUsage([]byte(`{not json`))
	require.Error(t, err)
}

func TestParseSSEData(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		wantData string
		wantOK   bool
	}{
		{"标准数据行", `data: {"a":1}`, `{"a":1}`, true},
		{"无空格", `data:{"a":1}`, `{"a":1}`, true},
		{"结束标记", `data: [DONE]`, `[DONE]`, true},
		{"空行", ``, ``, false},
		{"事件行", `event: message`, ``, false},
		{"注释行", `: keep-alive`, ``, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, ok := ParseSSEData([]byte(tc.line))
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.wantData, string(data))
			}
		})
	}
}

func TestIsDone(t *testing.T) {
	require.True(t, IsDone([]byte("[DONE]")))
	require.False(t, IsDone([]byte(`{"a":1}`)))
}

func TestIsUsageOnlyChunk(t *testing.T) {
	usageOnly := []byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	require.True(t, IsUsageOnlyChunk(usageOnly))

	contentChunk := []byte(`{"choices":[{"delta":{"content":"hi"}}]}`)
	require.False(t, IsUsageOnlyChunk(contentChunk))

	contentWithUsage := []byte(`{"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10}}`)
	require.False(t, IsUsageOnlyChunk(contentWithUsage), "带内容的块不算 usage-only")
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/openai/ -v
```

预期：编译失败，`undefined: ExtractUsage`。

- [ ] **Step 3: 写最小实现**

创建 `internal/openai/usage.go`：

```go
// Package openai 解析 OpenAI 兼容协议的报文。
// 本包不知道 Airlock 的任何概念，只做协议层的读取。
package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Usage 是协议层原样的 token 用量。
// 注意：CachedTokens 是 PromptTokens 的子集，ReasoningTokens 是 CompletionTokens 的子集。
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	ReasoningTokens  int64
}

type wireResponse struct {
	Model string `json:"model"`
	Usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// ExtractUsage 从一个完整的（非流式）响应体中提取 usage 与模型名。
// 响应中没有 usage 字段时返回零值，不视为错误——有些错误响应就是这样。
func ExtractUsage(body []byte) (Usage, string, error) {
	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return Usage{}, "", fmt.Errorf("解析响应体失败: %w", err)
	}
	if wire.Usage == nil {
		return Usage{}, wire.Model, nil
	}
	u := Usage{
		PromptTokens:     wire.Usage.PromptTokens,
		CompletionTokens: wire.Usage.CompletionTokens,
	}
	if d := wire.Usage.PromptTokensDetails; d != nil {
		u.CachedTokens = d.CachedTokens
	}
	if d := wire.Usage.CompletionTokensDetails; d != nil {
		u.ReasoningTokens = d.ReasoningTokens
	}
	return u, wire.Model, nil
}

var (
	ssePrefix = []byte("data:")
	doneMark  = []byte("[DONE]")
)

// ParseSSEData 从一行 SSE 文本中取出 data 负载。
// 非 data 行（空行、event:、注释）返回 ok=false。
func ParseSSEData(line []byte) ([]byte, bool) {
	if !bytes.HasPrefix(line, ssePrefix) {
		return nil, false
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, ssePrefix))
	if len(payload) == 0 {
		return nil, false
	}
	return payload, true
}

// IsDone 判断 SSE 负载是否为结束标记。
func IsDone(payload []byte) bool {
	return bytes.Equal(payload, doneMark)
}

// IsUsageOnlyChunk 判断这是否是只携带 usage、不含任何内容的块。
// 请求中带 stream_options.include_usage=true 时，上游会在末尾多发这样一块。
// 若该选项是 Edge 自行注入的，这一块需要在转发给客户端前剥掉。
func IsUsageOnlyChunk(payload []byte) bool {
	var probe struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   json.RawMessage   `json:"usage"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return len(probe.Choices) == 0 && len(probe.Usage) > 0
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/openai/ -v
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/openai/
git commit -m "feat: OpenAI 协议的 usage 提取与 SSE 帧解析"
```

---

### Task 12: 用量记录与异步批量写入器

**Files:**
- Create: `internal/usage/record.go`
- Create: `internal/usage/batch.go`
- Test: `internal/usage/batch_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/usage/batch_test.go`：

```go
package usage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memSink struct {
	mu      sync.Mutex
	batches [][]Record
}

func (s *memSink) InsertBatch(_ context.Context, records []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Record, len(records))
	copy(cp, records)
	s.batches = append(s.batches, cp)
	return nil
}

func (s *memSink) all() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, b := range s.batches {
		out = append(out, b...)
	}
	return out
}

func (s *memSink) batchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

func rec(id string) Record {
	return Record{RequestID: id, Timestamp: time.Now(), Model: "deepseek-chat"}
}

func TestBatchWriterFlushesWhenBatchFull(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 3, time.Hour) // 靠数量触发，不靠时间
	defer w.Close()

	w.Write(rec("a"))
	w.Write(rec("b"))
	w.Write(rec("c"))

	require.Eventually(t, func() bool { return sink.batchCount() == 1 }, time.Second, 10*time.Millisecond)
	require.Len(t, sink.all(), 3)
}

func TestBatchWriterFlushesOnInterval(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 1000, 50*time.Millisecond) // 靠时间触发
	defer w.Close()

	w.Write(rec("a"))

	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, time.Second, 10*time.Millisecond)
}

func TestCloseFlushesPendingRecords(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 1000, time.Hour)

	w.Write(rec("a"))
	w.Write(rec("b"))
	require.NoError(t, w.Close())

	require.Len(t, sink.all(), 2, "Close 必须把未满的批次刷出去")
}

func TestWriteAfterCloseDoesNotPanic(t *testing.T) {
	sink := &memSink{}
	w := NewBatchWriter(sink, 10, time.Hour)
	require.NoError(t, w.Close())

	require.NotPanics(t, func() { w.Write(rec("late")) })
}

func TestBatchWriterDropsWhenQueueFullRatherThanBlocking(t *testing.T) {
	// sink 阻塞不返回，模拟 ClickHouse 挂掉。
	// 写入必须继续返回，不能拖垮推理链路。
	blocked := make(chan struct{})
	sink := blockingSink{release: blocked}
	w := NewBatchWriter(sink, 1, time.Hour)
	defer func() { close(blocked); _ = w.Close() }()

	done := make(chan struct{})
	go func() {
		for i := 0; i < queueCapacity*2; i++ {
			w.Write(rec("x"))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write 在下游阻塞时被卡住了，必须丢弃而非阻塞")
	}
	require.Positive(t, w.Dropped(), "应记录被丢弃的条数")
}

type blockingSink struct{ release chan struct{} }

func (s blockingSink) InsertBatch(_ context.Context, _ []Record) error {
	<-s.release
	return nil
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/usage/ -v
```

预期：编译失败，`undefined: NewBatchWriter`。

- [ ] **Step 3: 写最小实现**

创建 `internal/usage/record.go`：

```go
// Package usage 定义调用用量记录及其写入管线。
package usage

import (
	"context"
	"time"

	"github.com/softmatrix/airlock/internal/pricing"
)

// Record 是一次调用的完整用量与审计元数据。
type Record struct {
	RequestID  string
	Timestamp  time.Time
	OrgID      string
	UserID     string
	KeyID      string
	Provider   string
	Model      string
	Usage      pricing.Usage
	CostMicro  pricing.Micro
	StatusCode int
	LatencyMS  int
	TTFTMS     int
	Stream     bool
	ErrorType  string
}

// Sink 是记录的最终去处，由 ClickHouse 等实现。
type Sink interface {
	InsertBatch(ctx context.Context, records []Record) error
}

// Writer 接收单条记录。实现必须是非阻塞的——推理链路不能被写入拖慢。
type Writer interface {
	Write(r Record)
}
```

创建 `internal/usage/batch.go`：

```go
package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// queueCapacity 是内存队列容量。队列满时新记录被丢弃并计数，
// 绝不阻塞调用方——丢几条用量记录，好过拖垮线上推理。
const queueCapacity = 4096

// flushTimeout 是单次批量写入的超时。
const flushTimeout = 10 * time.Second

// BatchWriter 把记录攒批后异步写入 Sink。
type BatchWriter struct {
	sink          Sink
	queue         chan Record
	batchSize     int
	flushInterval time.Duration

	dropped atomic.Int64

	closeOnce sync.Once
	closed    atomic.Bool
	done      chan struct{}
}

func NewBatchWriter(sink Sink, batchSize int, flushInterval time.Duration) *BatchWriter {
	w := &BatchWriter{
		sink:          sink,
		queue:         make(chan Record, queueCapacity),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		done:          make(chan struct{}),
	}
	go w.run()
	return w
}

// Write 是非阻塞的。队列满或已关闭时丢弃记录并计数。
func (w *BatchWriter) Write(r Record) {
	if w.closed.Load() {
		w.dropped.Add(1)
		return
	}
	select {
	case w.queue <- r:
	default:
		w.dropped.Add(1)
	}
}

// Dropped 返回累计被丢弃的记录条数，用于暴露成监控指标。
func (w *BatchWriter) Dropped() int64 {
	return w.dropped.Load()
}

// Close 停止接收新记录，把队列中剩余的刷出去后返回。
func (w *BatchWriter) Close() error {
	w.closeOnce.Do(func() {
		w.closed.Store(true)
		close(w.queue)
		<-w.done
	})
	return nil
}

func (w *BatchWriter) run() {
	defer close(w.done)

	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

	batch := make([]Record, 0, w.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		if err := w.sink.InsertBatch(ctx, batch); err != nil {
			w.dropped.Add(int64(len(batch)))
			slog.Error("用量记录批量写入失败", "count", len(batch), "err", err)
		}
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case r, ok := <-w.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, r)
			if len(batch) >= w.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/usage/ -v -race
```

预期：全部 PASS，无数据竞争告警。

- [ ] **Step 5: 提交**

```bash
git add internal/usage/
git commit -m "feat: 用量记录类型与非阻塞异步批量写入器"
```

---

### Task 13: 数据库迁移框架与初始 schema

**Files:**
- Create: `migrations/embed.go`
- Create: `migrations/20260827000001_init.sql`
- Create: `deploy/clickhouse/init/01_usage.sql`

- [ ] **Step 1: 拉取 goose 依赖**

```bash
go get github.com/pressly/goose/v3@latest
go get github.com/jackc/pgx/v5/stdlib@latest
```

- [ ] **Step 2: 写初始迁移**

创建 `migrations/20260827000001_init.sql`：

```sql
-- +goose Up
CREATE TABLE organizations (
    id          TEXT PRIMARY KEY,
    parent_id   TEXT REFERENCES organizations(id) ON DELETE RESTRICT,
    name        TEXT NOT NULL,
    -- path 是从根到本节点的 id 序列，用 / 分隔，便于按前缀查子树。
    path        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX organizations_parent_idx ON organizations(parent_id);
CREATE INDEX organizations_path_idx ON organizations(path text_pattern_ops);

CREATE TABLE api_keys (
    id                    TEXT PRIMARY KEY,
    key_hash              TEXT NOT NULL UNIQUE,
    key_prefix            TEXT NOT NULL,
    org_id                TEXT NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
    user_id               TEXT NOT NULL,
    name                  TEXT NOT NULL DEFAULT '',
    -- 上游 LiteLLM 密钥，AES-256-GCM 加密后的 base64
    upstream_key_enc      TEXT NOT NULL,
    status                TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'revoked')),
    expires_at            TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX api_keys_org_idx ON api_keys(org_id);

CREATE TABLE model_prices (
    id                 TEXT PRIMARY KEY,
    provider           TEXT NOT NULL,
    model              TEXT NOT NULL,
    effective_from     TIMESTAMPTZ NOT NULL,
    currency           TEXT NOT NULL DEFAULT 'CNY',
    -- tiers 是 pricing.Tier 的 JSON 数组，按 max_input_tokens 升序。
    -- 金额单位为 Micro（1e-6 元）。
    tiers              JSONB NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, model, effective_from)
);

CREATE INDEX model_prices_lookup_idx ON model_prices(provider, model, effective_from DESC);

-- +goose Down
DROP TABLE model_prices;
DROP TABLE api_keys;
DROP TABLE organizations;
```

- [ ] **Step 3: 写 embed 运行器**

创建 `migrations/embed.go`：

```go
// Package migrations 内嵌 SQL 迁移文件并提供运行入口。
// 内嵌而非依赖外部 CLI，是为了让私有化交付时只需分发单个二进制。
package migrations

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var fs embed.FS

// Up 把数据库迁移到最新版本。
func Up(db *sql.DB) error {
	goose.SetBaseFS(fs)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("设置 goose 方言失败: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("执行迁移失败: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: 写 ClickHouse 建表脚本**

创建 `deploy/clickhouse/init/01_usage.sql`：

```sql
CREATE DATABASE IF NOT EXISTS airlock;

CREATE TABLE IF NOT EXISTS airlock.usage_records
(
    request_id          String,
    ts                  DateTime64(3),
    org_id              String,
    user_id             String,
    key_id              String,
    provider            LowCardinality(String),
    model               LowCardinality(String),
    input_tokens        Int64,
    cached_input_tokens Int64,
    output_tokens       Int64,
    reasoning_tokens    Int64,
    cost_micro          Int64,
    status_code         UInt16,
    latency_ms          UInt32,
    ttft_ms             UInt32,
    stream              UInt8,
    error_type          LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org_id, ts, request_id);
```

- [ ] **Step 5: 启动数据库并验证迁移可运行**

```bash
docker compose --env-file .env -f deploy/docker-compose.yml up -d postgres clickhouse
```

写一个临时验证程序确认迁移能跑通：

```bash
cat > /tmp/migrate_check.go <<'EOF'
package main

import (
	"database/sql"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/softmatrix/airlock/migrations"
)

func main() {
	db, err := sql.Open("pgx", "postgres://airlock:airlock_dev@localhost:5432/airlock?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := migrations.Up(db); err != nil {
		log.Fatal(err)
	}
	log.Println("迁移成功")
}
EOF
go run /tmp/migrate_check.go
```

预期输出：goose 的迁移日志 + `迁移成功`。

验证表已建好：

```bash
docker compose -f deploy/docker-compose.yml exec postgres \
  psql -U airlock -d airlock -c "\dt"
```

预期：看到 `organizations`、`api_keys`、`model_prices`、`goose_db_version` 四张表。

验证 ClickHouse 表已建好：

```bash
docker compose -f deploy/docker-compose.yml exec clickhouse \
  clickhouse-client --query "DESCRIBE airlock.usage_records" | head -5
```

预期：输出前几个字段定义。

- [ ] **Step 6: 提交**

```bash
rm /tmp/migrate_check.go
git add go.mod go.sum migrations/ deploy/clickhouse/
git commit -m "feat: goose 内嵌迁移框架、Postgres 初始 schema 与 ClickHouse 用量表"
```

---

### Task 14: 密钥存储——按哈希查密钥

**Files:**
- Create: `internal/apikey/store.go`
- Test: `internal/apikey/store_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/apikey/store_test.go`：

```go
package apikey

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryStoreReturnsActiveKey(t *testing.T) {
	k := &Key{ID: "k1", Prefix: "ak-abcdefgh", OrgID: "o1", UserID: "u1",
		UpstreamKey: "sk-up", Status: StatusActive}
	s := NewMemoryStore(map[string]*Key{"hash1": k})

	got, err := s.ByHash(context.Background(), "hash1")
	require.NoError(t, err)
	require.Equal(t, "k1", got.ID)
	require.Equal(t, "sk-up", got.UpstreamKey)
}

func TestMemoryStoreUnknownHash(t *testing.T) {
	s := NewMemoryStore(nil)
	_, err := s.ByHash(context.Background(), "nope")
	require.ErrorIs(t, err, ErrKeyNotFound)
}

func TestValidateRejectsRevokedKey(t *testing.T) {
	k := &Key{ID: "k1", Status: StatusRevoked}
	require.ErrorIs(t, k.Validate(time.Now()), ErrKeyRevoked)
}

func TestValidateRejectsExpiredKey(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	k := &Key{ID: "k1", Status: StatusActive, ExpiresAt: &past}
	require.ErrorIs(t, k.Validate(time.Now()), ErrKeyExpired)
}

func TestValidateAcceptsUnexpiredActiveKey(t *testing.T) {
	future := time.Now().Add(time.Hour)
	k := &Key{ID: "k1", Status: StatusActive, ExpiresAt: &future}
	require.NoError(t, k.Validate(time.Now()))
}

func TestValidateAcceptsKeyWithoutExpiry(t *testing.T) {
	k := &Key{ID: "k1", Status: StatusActive}
	require.NoError(t, k.Validate(time.Now()))
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/apikey/ -run 'Store|Validate' -v
```

预期：编译失败，`undefined: NewMemoryStore`。

- [ ] **Step 3: 写最小实现**

创建 `internal/apikey/store.go`：

```go
package apikey

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

var (
	ErrKeyNotFound = errors.New("密钥不存在")
	ErrKeyRevoked  = errors.New("密钥已吊销")
	ErrKeyExpired  = errors.New("密钥已过期")
)

// Key 是一个已解密、可直接使用的 Airlock 虚拟密钥。
type Key struct {
	ID          string
	Prefix      string
	OrgID       string
	UserID      string
	UpstreamKey string // 已解密的上游 LiteLLM 密钥
	Status      string
	ExpiresAt   *time.Time
}

// Validate 检查密钥在 now 时刻是否可用。
func (k *Key) Validate(now time.Time) error {
	if k.Status != StatusActive {
		return ErrKeyRevoked
	}
	if k.ExpiresAt != nil && !k.ExpiresAt.After(now) {
		return ErrKeyExpired
	}
	return nil
}

// Store 按哈希查询密钥。
type Store interface {
	ByHash(ctx context.Context, hash string) (*Key, error)
}

// MemoryStore 是测试与本地开发用的内存实现。
type MemoryStore struct {
	mu   sync.RWMutex
	keys map[string]*Key
}

func NewMemoryStore(keys map[string]*Key) *MemoryStore {
	if keys == nil {
		keys = make(map[string]*Key)
	}
	return &MemoryStore{keys: keys}
}

func (s *MemoryStore) ByHash(_ context.Context, hash string) (*Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[hash]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return k, nil
}

// Put 供测试与本地开发写入密钥。
func (s *MemoryStore) Put(hash string, k *Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[hash] = k
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/apikey/ -v -race
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/apikey/store.go internal/apikey/store_test.go
git commit -m "feat: 密钥存储接口、状态校验与内存实现"
```

---

### Task 15: Edge 鉴权中间件

**Files:**
- Create: `internal/edge/auth.go`
- Test: `internal/edge/auth_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/edge/auth_test.go`：

```go
package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/stretchr/testify/require"
)

func storeWith(t *testing.T) (*apikey.MemoryStore, string) {
	t.Helper()
	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)

	s := apikey.NewMemoryStore(nil)
	s.Put(hash, &apikey.Key{
		ID: "k1", Prefix: prefix, OrgID: "org1", UserID: "user1",
		UpstreamKey: "sk-upstream", Status: apikey.StatusActive,
	})
	return s, plain
}

// echoKey 是被中间件包裹的处理器，把上下文里的密钥写回响应，便于断言。
func echoKey(w http.ResponseWriter, r *http.Request) {
	k, ok := KeyFromContext(r.Context())
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(k.OrgID + "/" + k.UserID))
}

func TestAuthAcceptsValidKey(t *testing.T) {
	store, plain := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "org1/user1", rec.Body.String())
}

func TestAuthRejectsMissingHeader(t *testing.T) {
	store, _ := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "missing_api_key")
}

func TestAuthRejectsNonBearerScheme(t *testing.T) {
	store, plain := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Basic "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthRejectsMalformedKeyWithoutHittingStore(t *testing.T) {
	h := Authenticate(apikey.NewMemoryStore(nil))(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-wrong-prefix")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_api_key")
}

func TestAuthRejectsUnknownKey(t *testing.T) {
	other, _, _, err := apikey.Generate()
	require.NoError(t, err)

	store, _ := storeWith(t)
	h := Authenticate(store)(http.HandlerFunc(echoKey))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+other)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthRejectsRevokedKey(t *testing.T) {
	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)
	store := apikey.NewMemoryStore(nil)
	store.Put(hash, &apikey.Key{ID: "k1", Prefix: prefix, Status: apikey.StatusRevoked})

	h := Authenticate(store)(http.HandlerFunc(echoKey))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "key_revoked")
}

func TestAuthRejectsExpiredKey(t *testing.T) {
	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)
	past := time.Now().Add(-time.Hour)
	store := apikey.NewMemoryStore(nil)
	store.Put(hash, &apikey.Key{ID: "k1", Prefix: prefix, Status: apikey.StatusActive, ExpiresAt: &past})

	h := Authenticate(store)(http.HandlerFunc(echoKey))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "key_expired")
}

func TestKeyFromContextAbsent(t *testing.T) {
	_, ok := KeyFromContext(context.Background())
	require.False(t, ok)
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/edge/ -v
```

预期：编译失败，`undefined: Authenticate`。

- [ ] **Step 3: 写最小实现**

创建 `internal/edge/auth.go`：

```go
package edge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/softmatrix/airlock/internal/apikey"
)

type ctxKey int

const keyCtxKey ctxKey = iota

// KeyFromContext 取出鉴权阶段放入上下文的密钥。
func KeyFromContext(ctx context.Context) (*apikey.Key, bool) {
	k, ok := ctx.Value(keyCtxKey).(*apikey.Key)
	return k, ok
}

const bearerPrefix = "Bearer "

// Authenticate 返回一个中间件：校验 ak- 密钥并把它放进请求上下文。
func Authenticate(store apikey.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			if raw == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing_api_key", "请求缺少 Authorization 头")
				return
			}
			if !strings.HasPrefix(raw, bearerPrefix) {
				writeAuthError(w, http.StatusUnauthorized, "invalid_api_key", "Authorization 头必须使用 Bearer 方案")
				return
			}
			plain := strings.TrimSpace(strings.TrimPrefix(raw, bearerPrefix))

			// 先做廉价的格式校验，挡掉明显非法的输入，避免无谓查库。
			if err := apikey.ValidateFormat(plain); err != nil {
				writeAuthError(w, http.StatusUnauthorized, "invalid_api_key", "密钥格式非法")
				return
			}

			key, err := store.ByHash(r.Context(), apikey.Hash(plain))
			if err != nil {
				if errors.Is(err, apikey.ErrKeyNotFound) {
					writeAuthError(w, http.StatusUnauthorized, "invalid_api_key", "密钥不存在")
					return
				}
				writeAuthError(w, http.StatusInternalServerError, "internal_error", "校验密钥时发生内部错误")
				return
			}

			switch err := key.Validate(time.Now()); {
			case errors.Is(err, apikey.ErrKeyRevoked):
				writeAuthError(w, http.StatusForbidden, "key_revoked", "密钥已吊销")
				return
			case errors.Is(err, apikey.ErrKeyExpired):
				writeAuthError(w, http.StatusForbidden, "key_expired", "密钥已过期")
				return
			case err != nil:
				writeAuthError(w, http.StatusForbidden, "key_unusable", "密钥不可用")
				return
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCtxKey, key)))
		})
	}
}

// writeAuthError 以 OpenAI 兼容的错误结构返回，客户端 SDK 能直接识别。
func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/edge/ -v -race
```

预期：全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/edge/auth.go internal/edge/auth_test.go
git commit -m "feat: Edge 鉴权中间件，OpenAI 兼容的错误结构"
```

---

### Task 16: Edge 非流式转发与用量记账

**Files:**
- Create: `internal/edge/proxy.go`
- Test: `internal/edge/proxy_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/edge/proxy_test.go`：

```go
package edge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
	"github.com/stretchr/testify/require"
)

type recordingWriter struct {
	mu      sync.Mutex
	records []usage.Record
}

func (w *recordingWriter) Write(r usage.Record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.records = append(w.records, r)
}

func (w *recordingWriter) get() []usage.Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]usage.Record, len(w.records))
	copy(out, w.records)
	return out
}

func testTable() pricing.Table {
	return pricing.NewMemoryTable([]pricing.ModelPrice{{
		Provider:      "litellm",
		Model:         "deepseek-chat",
		EffectiveFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Currency:      "CNY",
		Tiers: []pricing.Tier{{
			MaxInputTokens:   0,
			InputPer1M:       2_000_000,
			CachedInputPer1M: 500_000,
			OutputPer1M:      8_000_000,
			ReasoningPer1M:   8_000_000,
		}},
	}})
}

func testKey() *apikey.Key {
	return &apikey.Key{ID: "k1", OrgID: "org1", UserID: "user1",
		UpstreamKey: "sk-upstream", Status: apikey.StatusActive}
}

// withKey 把密钥塞进请求上下文，模拟鉴权中间件已经跑过。
func withKey(r *http.Request, k *apikey.Key) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), keyCtxKey, k))
}

func TestProxyForwardsAndSwapsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"deepseek-chat","usage":{"prompt_tokens":100,"completion_tokens":50}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "Bearer sk-upstream", gotAuth, "必须换成上游密钥，不能透传 ak-")
	require.JSONEq(t, body, gotBody, "请求体必须原样转发")
}

func TestProxyRecordsUsageAndCost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"deepseek-chat",
			"usage":{
				"prompt_tokens":1000000,
				"completion_tokens":1000000,
				"prompt_tokens_details":{"cached_tokens":0},
				"completion_tokens_details":{"reasoning_tokens":0}
			}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)

	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	p.ServeHTTP(httptest.NewRecorder(), req)

	records := w.get()
	require.Len(t, records, 1)
	r := records[0]
	require.Equal(t, "org1", r.OrgID)
	require.Equal(t, "user1", r.UserID)
	require.Equal(t, "k1", r.KeyID)
	require.Equal(t, "deepseek-chat", r.Model)
	require.Equal(t, int64(1_000_000), r.Usage.InputTokens)
	require.Equal(t, int64(1_000_000), r.Usage.OutputTokens)
	// 100 万输入 * 2元/百万 + 100 万输出 * 8元/百万 = 10 元
	require.Equal(t, pricing.Micro(10_000_000), r.CostMicro)
	require.Equal(t, http.StatusOK, r.StatusCode)
	require.False(t, r.Stream)
	require.NotEmpty(t, r.RequestID)
}

func TestProxyReturnsUpstreamBodyUnchanged(t *testing.T) {
	payload := `{"model":"deepseek-chat","choices":[{"message":{"content":"你好"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.JSONEq(t, payload, rec.Body.String())
}

func TestProxyRecordsUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, http.StatusTooManyRequests, records[0].StatusCode)
	require.Equal(t, "upstream_error", records[0].ErrorType)
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
}

func TestProxyRecordsZeroCostWhenPriceMissing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"unknown-model","usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"unknown-model"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "缺价格不能影响客户拿到响应")
	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
	require.Equal(t, "price_not_found", records[0].ErrorType)
}

func TestProxyFailsWithoutKeyInContext(t *testing.T) {
	p := NewProxy("http://unused", testTable(), &recordingWriter{})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestProxySetsRequestIDHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"deepseek-chat","usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.NotEmpty(t, rec.Header().Get("X-Airlock-Request-Id"))
}

func TestIsStreamRequest(t *testing.T) {
	require.True(t, isStreamRequest([]byte(`{"stream":true}`)))
	require.False(t, isStreamRequest([]byte(`{"stream":false}`)))
	require.False(t, isStreamRequest([]byte(`{}`)))
	require.False(t, isStreamRequest([]byte(`not json`)))
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/edge/ -run TestProxy -v
```

预期：编译失败，`undefined: NewProxy`。

- [ ] **Step 3: 写最小实现**

```bash
go get github.com/google/uuid@latest
```

创建 `internal/edge/proxy.go`：

```go
package edge

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/softmatrix/airlock/internal/openai"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
)

// upstreamProvider 是记账时标注的供应商名。
// P1.1 阶段所有流量都经 LiteLLM，故统一标为 "litellm"；
// P2 起 Edge 会知道真实供应商，届时改为从模型目录解析。
const upstreamProvider = "litellm"

// Proxy 把请求透明转发到上游 LiteLLM，并记录用量与成本。
type Proxy struct {
	upstreamBaseURL string
	prices          pricing.Table
	usage           usage.Writer
	client          *http.Client
}

func NewProxy(upstreamBaseURL string, prices pricing.Table, w usage.Writer) *Proxy {
	return &Proxy{
		upstreamBaseURL: upstreamBaseURL,
		prices:          prices,
		usage:           w,
		client: &http.Client{
			// 不设总超时：长文本生成可能持续数分钟，由客户端与上游自行决定。
			Timeout: 0,
		},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, ok := KeyFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusInternalServerError, "internal_error", "上下文中缺少密钥，鉴权中间件未生效")
		return
	}

	requestID := uuid.NewString()
	w.Header().Set("X-Airlock-Request-Id", requestID)

	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid_request", "读取请求体失败")
		return
	}

	rec := usage.Record{
		RequestID: requestID,
		Timestamp: time.Now(),
		OrgID:     key.OrgID,
		UserID:    key.UserID,
		KeyID:     key.ID,
		Provider:  upstreamProvider,
		Stream:    isStreamRequest(reqBody),
	}

	start := time.Now()
	defer func() {
		rec.LatencyMS = int(time.Since(start).Milliseconds())
		p.usage.Write(rec)
	}()

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method,
		p.upstreamBaseURL+r.URL.Path, bytes.NewReader(reqBody))
	if err != nil {
		rec.StatusCode = http.StatusInternalServerError
		rec.ErrorType = "build_request_failed"
		writeAuthError(w, http.StatusInternalServerError, "internal_error", "构造上游请求失败")
		return
	}
	copyRequestHeaders(upstreamReq.Header, r.Header)
	upstreamReq.Header.Set("Authorization", "Bearer "+key.UpstreamKey)

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		rec.StatusCode = http.StatusBadGateway
		rec.ErrorType = "upstream_unreachable"
		writeAuthError(w, http.StatusBadGateway, "upstream_unreachable", "无法连接上游服务")
		return
	}
	defer resp.Body.Close()

	rec.StatusCode = resp.StatusCode
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		rec.ErrorType = "read_response_failed"
		return
	}
	if _, err := w.Write(respBody); err != nil {
		slog.Warn("向客户端写响应失败", "request_id", requestID, "err", err)
	}

	if resp.StatusCode != http.StatusOK {
		rec.ErrorType = "upstream_error"
		return
	}

	protoUsage, model, err := openai.ExtractUsage(respBody)
	if err != nil {
		rec.ErrorType = "usage_parse_failed"
		return
	}
	rec.Model = model
	rec.Usage = toPricingUsage(protoUsage)
	rec.CostMicro = p.cost(&rec)
}

// cost 计算成本。任何失败都只记录 ErrorType 并返回 0，绝不影响客户拿到响应。
func (p *Proxy) cost(rec *usage.Record) pricing.Micro {
	price, err := p.prices.Lookup(rec.Provider, rec.Model, rec.Timestamp)
	if err != nil {
		rec.ErrorType = "price_not_found"
		slog.Warn("未找到生效价格", "provider", rec.Provider, "model", rec.Model, "err", err)
		return 0
	}
	cost, err := price.Cost(rec.Usage)
	if err != nil {
		rec.ErrorType = "cost_calc_failed"
		slog.Warn("成本计算失败", "model", rec.Model, "err", err)
		return 0
	}
	return cost
}

func toPricingUsage(u openai.Usage) pricing.Usage {
	return pricing.Usage{
		InputTokens:       u.PromptTokens,
		CachedInputTokens: u.CachedTokens,
		OutputTokens:      u.CompletionTokens,
		ReasoningTokens:   u.ReasoningTokens,
	}
}

// isStreamRequest 判断请求体是否要求流式响应。
func isStreamRequest(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream
}

// hopByHopHeaders 不应被代理转发。
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func copyRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		// Authorization 由调用方随后覆盖为上游密钥，这里先不拷贝。
		if http.CanonicalHeaderKey(k) == "Authorization" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go mod tidy
go test ./internal/edge/ -v -race
```

预期：Task 15 与 Task 16 的测试全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add go.mod go.sum internal/edge/proxy.go internal/edge/proxy_test.go
git commit -m "feat: Edge 非流式转发、密钥替换与用量记账"
```

---

### Task 17: Edge 流式转发（SSE）

流式是 Edge 最容易出错的部分：要边转发边攒 usage，还要处理 `stream_options.include_usage` 的注入与剥离。

**Files:**
- Create: `internal/edge/stream.go`
- Modify: `internal/edge/proxy.go`（在 ServeHTTP 中分流到流式路径）
- Test: `internal/edge/stream_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/edge/stream_test.go`：

```go
package edge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/stretchr/testify/require"
)

func TestEnsureIncludeUsageInjectsWhenAbsent(t *testing.T) {
	body, injected, err := ensureIncludeUsage([]byte(`{"model":"m","stream":true}`))
	require.NoError(t, err)
	require.True(t, injected)
	require.Contains(t, string(body), `"include_usage":true`)
}

func TestEnsureIncludeUsageLeavesExistingOptionAlone(t *testing.T) {
	original := `{"model":"m","stream":true,"stream_options":{"include_usage":true}}`
	body, injected, err := ensureIncludeUsage([]byte(original))
	require.NoError(t, err)
	require.False(t, injected, "客户端自己要了 usage，不能算作我们注入")
	require.JSONEq(t, original, string(body))
}

func TestEnsureIncludeUsageRespectsExplicitFalse(t *testing.T) {
	original := `{"model":"m","stream":true,"stream_options":{"include_usage":false}}`
	body, injected, err := ensureIncludeUsage([]byte(original))
	require.NoError(t, err)
	require.False(t, injected, "客户端显式关掉了，我们不覆盖")
	require.JSONEq(t, original, string(body))
}

// sseUpstream 构造一个吐固定 SSE 帧的上游。
// 每帧前故意停 2ms：否则本机回环太快，TTFT 取整到毫秒会是 0，
// 断言「首字延迟被测出来」就会随机失败。
func sseUpstream(frames ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, f := range frames {
			time.Sleep(2 * time.Millisecond)
			_, _ = w.Write([]byte(f + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
}

func TestStreamForwardsContentAndCapturesUsage(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"你"}}]}`,
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"好"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000}}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)

	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	out := rec.Body.String()
	require.Contains(t, out, `"content":"你"`)
	require.Contains(t, out, `"content":"好"`)
	require.Contains(t, out, "[DONE]")

	records := w.get()
	require.Len(t, records, 1)
	require.True(t, records[0].Stream)
	require.Equal(t, int64(1_000_000), records[0].Usage.InputTokens)
	require.Equal(t, pricing.Micro(10_000_000), records[0].CostMicro)
	require.Positive(t, records[0].TTFTMS, "首字延迟必须被测出来")
}

func TestStreamStripsInjectedUsageChunk(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	// 客户端没有要 usage，Edge 自行注入，因此那一块必须被剥掉
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.NotContains(t, rec.Body.String(), `"usage"`,
		"Edge 自行注入 include_usage 时，usage 块不得下发给客户端")
	require.Contains(t, rec.Body.String(), "[DONE]")
}

func TestStreamKeepsUsageChunkWhenClientAskedForIt(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	p := NewProxy(upstream.URL, testTable(), &recordingWriter{})
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true,"stream_options":{"include_usage":true}}`)), testKey())
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Contains(t, rec.Body.String(), `"usage"`,
		"客户端自己要了 usage，必须原样下发")
}

func TestStreamRecordsWhenUpstreamSendsNoUsage(t *testing.T) {
	upstream := sseUpstream(
		`data: {"model":"deepseek-chat","choices":[{"delta":{"content":"hi"}}]}`,
		`data: [DONE]`,
	)
	defer upstream.Close()

	w := &recordingWriter{}
	p := NewProxy(upstream.URL, testTable(), w)
	req := withKey(httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","stream":true}`)), testKey())
	p.ServeHTTP(httptest.NewRecorder(), req)

	records := w.get()
	require.Len(t, records, 1)
	require.Equal(t, "usage_missing", records[0].ErrorType)
	require.Equal(t, pricing.Micro(0), records[0].CostMicro)
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/edge/ -run TestStream -v
```

预期：编译失败，`undefined: ensureIncludeUsage`。

- [ ] **Step 3: 写流式实现**

创建 `internal/edge/stream.go`：

```go
package edge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/softmatrix/airlock/internal/openai"
)

// sseScanBufferMax 是单个 SSE 帧的最大长度。
// 默认 bufio.Scanner 上限只有 64KB，长上下文的首帧可能超过。
const sseScanBufferMax = 4 * 1024 * 1024

// streamOutcome 是流式转发结束后的结果。
type streamOutcome struct {
	usage *openai.Usage
	model string
	ttft  time.Duration
}

// ensureIncludeUsage 保证请求带上 stream_options.include_usage=true，
// 否则上游不会在流末尾发送 usage，Edge 就无从记账。
//
// 返回 injected=true 表示这个选项是 Edge 加的，此时末尾的 usage 块
// 不属于客户端预期的输出，转发时必须剥掉。
func ensureIncludeUsage(body []byte) (out []byte, injected bool, err error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("解析请求体失败: %w", err)
	}

	if raw, ok := payload["stream_options"]; ok {
		var opts map[string]json.RawMessage
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, false, fmt.Errorf("解析 stream_options 失败: %w", err)
		}
		// 客户端已经表过态（无论 true 还是 false），一律尊重，不覆盖。
		if _, exists := opts["include_usage"]; exists {
			return body, false, nil
		}
		opts["include_usage"] = json.RawMessage("true")
		merged, err := json.Marshal(opts)
		if err != nil {
			return nil, false, fmt.Errorf("序列化 stream_options 失败: %w", err)
		}
		payload["stream_options"] = merged
	} else {
		payload["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}

	out, err = json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("序列化请求体失败: %w", err)
	}
	return out, true, nil
}

// pipeStream 逐帧转发 SSE，同时抓取 usage、模型名与首字延迟。
// stripUsageChunk 为 true 时，只含 usage 的那一块不下发给客户端。
func pipeStream(w http.ResponseWriter, body io.Reader, stripUsageChunk bool, start time.Time) streamOutcome {
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), sseScanBufferMax)

	var outcome streamOutcome
	var ttftSet bool

	for scanner.Scan() {
		line := scanner.Bytes()

		payload, isData := openai.ParseSSEData(line)
		if !isData {
			// 空行、event:、注释一律原样透传，保持 SSE 语义完整。
			writeLine(w, flusher, line)
			continue
		}

		if openai.IsDone(payload) {
			writeLine(w, flusher, line)
			continue
		}

		if openai.IsUsageOnlyChunk(payload) {
			if u, model, err := openai.ExtractUsage(payload); err == nil {
				outcome.usage = &u
				if model != "" {
					outcome.model = model
				}
			}
			if stripUsageChunk {
				continue // 这是 Edge 自己要来的，不下发
			}
			writeLine(w, flusher, line)
			continue
		}

		// 内容块：记录首字延迟与模型名
		if !ttftSet {
			outcome.ttft = time.Since(start)
			ttftSet = true
		}
		if outcome.model == "" {
			if _, model, err := openai.ExtractUsage(payload); err == nil && model != "" {
				outcome.model = model
			}
		}
		writeLine(w, flusher, line)
	}

	return outcome
}

func writeLine(w http.ResponseWriter, flusher http.Flusher, line []byte) {
	_, _ = w.Write(line)
	_, _ = w.Write([]byte("\n"))
	if flusher != nil {
		flusher.Flush()
	}
}
```

- [ ] **Step 4: 在 Proxy 中分流到流式路径**

修改 `internal/edge/proxy.go` 的 `ServeHTTP`：从 `upstreamReq, err := http.NewRequestWithContext(...)` 这一行开始，一直到函数结尾的右大括号，**整段删除**，替换为下面的代码（它在原逻辑前面新增了 include_usage 注入，中间新增了 `stream` 分支）：

```go
	// 流式请求需要注入 include_usage，否则上游不会回传 usage。
	forwardBody := reqBody
	stripUsageChunk := false
	if rec.Stream {
		injectedBody, injected, err := ensureIncludeUsage(reqBody)
		if err != nil {
			rec.StatusCode = http.StatusBadRequest
			rec.ErrorType = "invalid_request_body"
			writeAuthError(w, http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON")
			return
		}
		forwardBody = injectedBody
		stripUsageChunk = injected
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method,
		p.upstreamBaseURL+r.URL.Path, bytes.NewReader(forwardBody))
	if err != nil {
		rec.StatusCode = http.StatusInternalServerError
		rec.ErrorType = "build_request_failed"
		writeAuthError(w, http.StatusInternalServerError, "internal_error", "构造上游请求失败")
		return
	}
	copyRequestHeaders(upstreamReq.Header, r.Header)
	upstreamReq.Header.Set("Authorization", "Bearer "+key.UpstreamKey)
	upstreamReq.ContentLength = int64(len(forwardBody))

	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		rec.StatusCode = http.StatusBadGateway
		rec.ErrorType = "upstream_unreachable"
		writeAuthError(w, http.StatusBadGateway, "upstream_unreachable", "无法连接上游服务")
		return
	}
	defer resp.Body.Close()

	rec.StatusCode = resp.StatusCode
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		rec.ErrorType = "upstream_error"
		_, _ = io.Copy(w, resp.Body)
		return
	}

	if rec.Stream {
		outcome := pipeStream(w, resp.Body, stripUsageChunk, start)
		rec.TTFTMS = int(outcome.ttft.Milliseconds())
		rec.Model = outcome.model
		if outcome.usage == nil {
			rec.ErrorType = "usage_missing"
			return
		}
		rec.Usage = toPricingUsage(*outcome.usage)
		rec.CostMicro = p.cost(&rec)
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		rec.ErrorType = "read_response_failed"
		return
	}
	if _, err := w.Write(respBody); err != nil {
		slog.Warn("向客户端写响应失败", "request_id", requestID, "err", err)
	}

	protoUsage, model, err := openai.ExtractUsage(respBody)
	if err != nil {
		rec.ErrorType = "usage_parse_failed"
		return
	}
	rec.Model = model
	rec.Usage = toPricingUsage(protoUsage)
	rec.CostMicro = p.cost(&rec)
}
```

- [ ] **Step 5: 运行测试，确认通过**

```bash
go test ./internal/edge/ -v -race
```

预期：Task 15、16、17 的测试全部 PASS。

> **注意**：`TestProxyRecordsUpstreamError` 断言错误响应体被转发，新版实现用 `io.Copy` 完成，行为不变。

- [ ] **Step 6: 提交**

```bash
git add internal/edge/stream.go internal/edge/stream_test.go internal/edge/proxy.go
git commit -m "feat: Edge 流式转发，include_usage 注入与剥离、TTFT 测量"
```

---

### Task 18: ClickHouse Sink、服务器装配与端到端验收

**Files:**
- Create: `internal/usage/clickhouse.go`
- Create: `internal/edge/server.go`
- Test: `internal/edge/server_test.go`
- Create: `cmd/edge/main.go`

- [ ] **Step 1: 写服务器路由的失败测试**

创建 `internal/edge/server_test.go`：

```go
package edge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/stretchr/testify/require"
)

func TestHealthzNeedsNoAuth(t *testing.T) {
	srv := NewServer(Deps{
		Keys:            apikey.NewMemoryStore(nil),
		Prices:          testTable(),
		Usage:           &recordingWriter{},
		UpstreamBaseURL: "http://unused",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"status":"ok"`)
}

func TestChatCompletionsRequiresAuth(t *testing.T) {
	srv := NewServer(Deps{
		Keys:            apikey.NewMemoryStore(nil),
		Prices:          testTable(),
		Usage:           &recordingWriter{},
		UpstreamBaseURL: "http://unused",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUnknownPathReturns404(t *testing.T) {
	srv := NewServer(Deps{
		Keys:            apikey.NewMemoryStore(nil),
		Prices:          testTable(),
		Usage:           &recordingWriter{},
		UpstreamBaseURL: "http://unused",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAllV1PathsAreProxied(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `","model":"deepseek-chat","usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	plain, hash, prefix, err := apikey.Generate()
	require.NoError(t, err)
	store := apikey.NewMemoryStore(nil)
	store.Put(hash, &apikey.Key{ID: "k1", Prefix: prefix, OrgID: "o", UserID: "u",
		UpstreamKey: "sk-up", Status: apikey.StatusActive})

	srv := NewServer(Deps{
		Keys: store, Prices: testTable(), Usage: &recordingWriter{},
		UpstreamBaseURL: upstream.URL,
	})

	for _, path := range []string{"/v1/chat/completions", "/v1/embeddings", "/v1/models"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+plain)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "路径 %s 应被代理", path)
		require.Contains(t, rec.Body.String(), path)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./internal/edge/ -run 'TestHealthz|TestChatCompletions|TestUnknownPath|TestAllV1' -v
```

预期：编译失败，`undefined: NewServer`。

- [ ] **Step 3: 写服务器实现**

创建 `internal/edge/server.go`：

```go
package edge

import (
	"encoding/json"
	"net/http"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
)

// Deps 是 Edge 服务器的全部外部依赖，均为接口，便于测试替换。
type Deps struct {
	Keys            apikey.Store
	Prices          pricing.Table
	Usage           usage.Writer
	UpstreamBaseURL string
}

type Server struct {
	deps Deps
}

func NewServer(deps Deps) *Server {
	return &Server{deps: deps}
}

// Handler 返回装配好的路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	proxy := NewProxy(s.deps.UpstreamBaseURL, s.deps.Prices, s.deps.Usage)
	authenticated := Authenticate(s.deps.Keys)(proxy)

	// /v1/ 下的一切都透明转发给上游，Edge 不维护端点白名单——
	// LiteLLM 新增端点时无需改 Edge。
	mux.Handle("/v1/", authenticated)

	return mux
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./internal/edge/ -v -race
```

预期：全部 PASS。

- [ ] **Step 5: 写 ClickHouse Sink**

```bash
go get github.com/ClickHouse/clickhouse-go/v2@latest
```

创建 `internal/usage/clickhouse.go`：

```go
package usage

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// ClickHouseSink 把用量记录批量写入 ClickHouse。
type ClickHouseSink struct {
	conn driver.Conn
}

func NewClickHouseSink(dsn string) (*ClickHouseSink, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("解析 ClickHouse DSN 失败: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("连接 ClickHouse 失败: %w", err)
	}
	return &ClickHouseSink{conn: conn}, nil
}

const insertSQL = `INSERT INTO airlock.usage_records (
	request_id, ts, org_id, user_id, key_id, provider, model,
	input_tokens, cached_input_tokens, output_tokens, reasoning_tokens,
	cost_micro, status_code, latency_ms, ttft_ms, stream, error_type
)`

func (s *ClickHouseSink) InsertBatch(ctx context.Context, records []Record) error {
	batch, err := s.conn.PrepareBatch(ctx, insertSQL)
	if err != nil {
		return fmt.Errorf("准备批次失败: %w", err)
	}
	for _, r := range records {
		var stream uint8
		if r.Stream {
			stream = 1
		}
		if err := batch.Append(
			r.RequestID, r.Timestamp, r.OrgID, r.UserID, r.KeyID, r.Provider, r.Model,
			r.Usage.InputTokens, r.Usage.CachedInputTokens, r.Usage.OutputTokens, r.Usage.ReasoningTokens,
			int64(r.CostMicro), uint16(r.StatusCode), uint32(r.LatencyMS), uint32(r.TTFTMS),
			stream, r.ErrorType,
		); err != nil {
			return fmt.Errorf("追加记录失败: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("发送批次失败: %w", err)
	}
	return nil
}

func (s *ClickHouseSink) Close() error {
	return s.conn.Close()
}
```

- [ ] **Step 6: 写 Edge 进程入口**

创建 `cmd/edge/main.go`：

```go
// Command edge 是 Airlock 的内联网关进程。
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/config"
	"github.com/softmatrix/airlock/internal/edge"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
	"github.com/softmatrix/airlock/migrations"
)

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "只执行数据库迁移后退出")
	flag.Parse()

	if err := run(*migrateOnly); err != nil {
		slog.Error("edge 启动失败", "err", err)
		os.Exit(1)
	}
}

func run(migrateOnly bool) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := migrations.Up(db); err != nil {
		return err
	}
	if migrateOnly {
		slog.Info("迁移完成")
		return nil
	}

	sink, err := usage.NewClickHouseSink(cfg.ClickHouseDSN)
	if err != nil {
		return err
	}
	defer sink.Close()

	writer := usage.NewBatchWriter(sink, 200, 2*time.Second)
	defer writer.Close()

	// P1.1 阶段密钥与价格从内存加载，由控制面在 P1.2/P1.3 接管为数据库来源。
	// 这里保留接口，切换时只换实现。
	keys := apikey.NewMemoryStore(nil)
	prices := pricing.NewMemoryTable(nil)

	srv := &http.Server{
		Addr:    cfg.EdgeListenAddr,
		Handler: edge.NewServer(edge.Deps{
			Keys:            keys,
			Prices:          prices,
			Usage:           writer,
			UpstreamBaseURL: cfg.UpstreamBaseURL,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("Edge 启动", "addr", cfg.EdgeListenAddr, "upstream", cfg.UpstreamBaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-stop:
		slog.Info("收到退出信号，开始优雅关闭")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}
```

- [ ] **Step 7: 全量测试与构建**

```bash
go mod tidy
make test
make build
```

预期：所有包测试 PASS，`bin/edge` 构建成功。

- [ ] **Step 8: 提交**

```bash
git add go.mod go.sum internal/usage/clickhouse.go internal/edge/server.go internal/edge/server_test.go cmd/edge/main.go
git commit -m "feat: ClickHouse Sink、Edge 路由装配与进程入口"
```

---

### Task 19: 端到端验收

验证 P1.1 的整条链路真的通了，而不只是单测通过。

**Files:**
- 无新增文件

- [ ] **Step 1: 启动全栈**

```bash
docker compose --env-file .env -f deploy/docker-compose.yml up -d
```

等待全部健康：

```bash
docker compose -f deploy/docker-compose.yml ps
```

预期：postgres、clickhouse、litellm 三个服务的 State 均为 running/healthy。

- [ ] **Step 2: 执行迁移**

```bash
export POSTGRES_DSN="postgres://airlock:airlock_dev@localhost:5432/airlock?sslmode=disable"
export CLICKHOUSE_DSN="clickhouse://airlock:airlock_dev@localhost:9000/airlock"
export EDGE_UPSTREAM_BASE_URL="http://localhost:4000"
export AIRLOCK_ENCRYPTION_KEY="YWlybG9jay1kZXYtb25seS0zMmJ5dGUta2V5ISEhISE="
make migrate
```

预期：goose 输出迁移成功。

- [ ] **Step 3: 造一条测试数据**

```bash
docker compose -f deploy/docker-compose.yml exec -T postgres \
  psql -U airlock -d airlock <<'SQL'
INSERT INTO organizations (id, parent_id, name, path)
VALUES ('org1', NULL, '研发中心', '/org1');
SQL
```

生成一个 `ak-` 密钥并入库。写一个一次性程序：

```bash
cat > /tmp/seed.go <<'EOF'
package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/cryptobox"
)

func main() {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("AIRLOCK_ENCRYPTION_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	c, err := cryptobox.NewCipher(key)
	if err != nil {
		log.Fatal(err)
	}
	enc, err := c.Encrypt(os.Getenv("LITELLM_MASTER_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	plain, hash, prefix, err := apikey.Generate()
	if err != nil {
		log.Fatal(err)
	}

	db, err := sql.Open("pgx", os.Getenv("POSTGRES_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO api_keys
		(id, key_hash, key_prefix, org_id, user_id, name, upstream_key_enc, status)
		VALUES ('k1', $1, $2, 'org1', 'user1', '端到端验收', $3, 'active')`,
		hash, prefix, enc)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(plain)
}
EOF
export LITELLM_MASTER_KEY=sk-airlock-master-dev-only
AK=$(go run /tmp/seed.go)
echo "生成的密钥：$AK"
```

- [ ] **Step 4: 灌入定价数据**

```bash
docker compose -f deploy/docker-compose.yml exec -T postgres \
  psql -U airlock -d airlock <<'SQL'
INSERT INTO model_prices (id, provider, model, effective_from, currency, tiers)
VALUES (
  'p1', 'litellm', 'deepseek-chat', '2020-01-01T00:00:00Z', 'CNY',
  '[{"MaxInputTokens":0,"InputPer1M":2000000,"CachedInputPer1M":500000,"OutputPer1M":8000000,"ReasoningPer1M":8000000}]'::jsonb
);
SQL
```

> **注意**：`cmd/edge/main.go` 当前用的是 `apikey.NewMemoryStore` 与 `pricing.NewMemoryTable`（P1.2/P1.3 才接数据库）。本步骤先把数据准备好，Step 5 用一个临时装配跑通端到端。

- [ ] **Step 5: 用临时装配跑通端到端**

```bash
cat > /tmp/e2e.go <<'EOF'
package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/cryptobox"
	"github.com/softmatrix/airlock/internal/edge"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
)

func main() {
	db, err := sql.Open("pgx", os.Getenv("POSTGRES_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	encKey, _ := base64.StdEncoding.DecodeString(os.Getenv("AIRLOCK_ENCRYPTION_KEY"))
	cipher, err := cryptobox.NewCipher(encKey)
	if err != nil {
		log.Fatal(err)
	}

	// 从库里读密钥
	var hash, prefix, orgID, userID, encUpstream string
	err = db.QueryRow(`SELECT key_hash, key_prefix, org_id, user_id, upstream_key_enc
		FROM api_keys WHERE id = 'k1'`).Scan(&hash, &prefix, &orgID, &userID, &encUpstream)
	if err != nil {
		log.Fatal(err)
	}
	upstream, err := cipher.Decrypt(encUpstream)
	if err != nil {
		log.Fatal(err)
	}
	keys := apikey.NewMemoryStore(nil)
	keys.Put(hash, &apikey.Key{ID: "k1", Prefix: prefix, OrgID: orgID, UserID: userID,
		UpstreamKey: upstream, Status: apikey.StatusActive})

	// 从库里读价格
	var provider, model, currency, tiersJSON string
	var effFrom time.Time
	err = db.QueryRow(`SELECT provider, model, effective_from, currency, tiers::text
		FROM model_prices WHERE id = 'p1'`).Scan(&provider, &model, &effFrom, &currency, &tiersJSON)
	if err != nil {
		log.Fatal(err)
	}
	var tiers []pricing.Tier
	if err := json.Unmarshal([]byte(tiersJSON), &tiers); err != nil {
		log.Fatal(err)
	}
	prices := pricing.NewMemoryTable([]pricing.ModelPrice{{
		Provider: provider, Model: model, EffectiveFrom: effFrom,
		Currency: currency, Tiers: tiers,
	}})

	sink, err := usage.NewClickHouseSink(os.Getenv("CLICKHOUSE_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer sink.Close()
	writer := usage.NewBatchWriter(sink, 1, 500*time.Millisecond)
	defer writer.Close()

	h := edge.NewServer(edge.Deps{
		Keys: keys, Prices: prices, Usage: writer,
		UpstreamBaseURL: os.Getenv("EDGE_UPSTREAM_BASE_URL"),
	}).Handler()

	log.Println("Edge 监听 :8080")
	log.Fatal(http.ListenAndServe(":8080", h))
}
EOF
go run /tmp/e2e.go &
E2E_PID=$!
sleep 2
```

- [ ] **Step 6: 验证非流式调用**

```bash
curl -sS http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $AK" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"用一句话介绍长江"}],"max_tokens":100}' \
  -D /tmp/headers.txt | jq .
```

预期：
- 返回正常的 `choices` 与 `usage`
- 响应头中有 `X-Airlock-Request-Id`

```bash
grep -i "x-airlock-request-id" /tmp/headers.txt
```

- [ ] **Step 7: 验证流式调用**

```bash
curl -sS -N http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $AK" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","stream":true,"messages":[{"role":"user","content":"数到五"}],"max_tokens":100}' \
  | tee /tmp/stream.txt | head -5
```

预期：看到逐块吐出的 `data: {...}`，末尾有 `data: [DONE]`。

验证注入的 usage 块被剥掉了：

```bash
grep -c '"usage"' /tmp/stream.txt
```

预期输出：`0`（Edge 自行注入 include_usage，因此 usage 块不下发）。

- [ ] **Step 8: 验证鉴权拦截**

```bash
curl -sS -o /dev/null -w "%{http_code}\n" http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer ak-invalid-key-that-does-not-exist" \
  -H "Content-Type: application/json" -d '{"model":"deepseek-chat"}'
```

预期：`401`

```bash
curl -sS -o /dev/null -w "%{http_code}\n" http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -d '{"model":"deepseek-chat"}'
```

预期：`401`

- [ ] **Step 9: 验证用量与成本已落 ClickHouse**

```bash
sleep 3
docker compose -f deploy/docker-compose.yml exec clickhouse \
  clickhouse-client --user airlock --password airlock_dev --query "
    SELECT request_id, org_id, user_id, model, stream,
           input_tokens, output_tokens, cost_micro, status_code, latency_ms, ttft_ms, error_type
    FROM airlock.usage_records
    ORDER BY ts DESC
    LIMIT 5
    FORMAT Vertical"
```

**逐条核对：**
- 非流式与流式各有一条记录
- `org_id` = `org1`，`user_id` = `user1`
- `model` = `deepseek-chat`
- 流式那条 `stream` = 1 且 `ttft_ms` > 0
- `cost_micro` > 0，且与手工按 `usage` 和定价表算出的数字一致
- `error_type` 为空

- [ ] **Step 10: 手工核对成本正确性**

从 Step 6 的响应中取出 `usage.prompt_tokens` 与 `usage.completion_tokens`，手工计算：

```
预期成本(Micro) = prompt_tokens * 2000000 / 1000000 + completion_tokens * 8000000 / 1000000
```

与 ClickHouse 中的 `cost_micro` 比对。**必须完全相等**——这是 PRD 验收标准 A2「账单逐分对齐」的最小验证。

- [ ] **Step 11: 清理**

```bash
kill $E2E_PID
rm -f /tmp/seed.go /tmp/e2e.go /tmp/headers.txt /tmp/stream.txt
docker compose -f deploy/docker-compose.yml down
```

- [ ] **Step 12: 记录验收结果并提交**

在 `docs/superpowers/findings/` 下创建 `2026-08-27-p1.1-acceptance.md`，记录：

```markdown
# P1.1 端到端验收记录

| 项目 | 内容 |
|---|---|
| 日期 | （填实际日期） |
| 验收人 | （填） |

## 验收项

| 项 | 结果 | 证据 |
|---|---|---|
| 非流式调用成功 | | （粘贴响应摘要） |
| 流式调用成功且首块及时 | | （粘贴前几帧） |
| 注入的 usage 块已剥离 | | grep 计数为 0 |
| 无效密钥返回 401 | | |
| 缺少 Authorization 返回 401 | | |
| 用量落 ClickHouse | | （粘贴查询结果） |
| 流式记录含 ttft_ms > 0 | | |
| 成本与手工计算一致 | | 手工值 vs 实际值 |

## 遗留问题

（列出发现但未在 P1.1 解决的问题，作为 P1.2 的输入）
```

```bash
git add docs/superpowers/findings/2026-08-27-p1.1-acceptance.md
git commit -m "docs: P1.1 端到端验收记录"
```

---

## 完成标准

P0 + P1.1 全部完成时，下面每一条都应成立：

- [ ] 三份 P0 结论文档写完，无占位符，且 Task 3 的分支决策已明确
- [ ] `make test` 全绿，含 `-race`
- [ ] `make build` 产出 `bin/edge`
- [ ] 端到端验收记录中每一项都是通过
- [ ] 成本计算与手工核算完全一致
- [ ] 所有代码已提交，工作区干净

---

## 自查记录

对照 spec 的覆盖情况：

| Spec 要求（Roadmap P0 / P1.1） | 对应任务 |
|---|---|
| 实测 LiteLLM 管理端点无 License 可用性 | Task 2、3 |
| 组织树拍平方案验证 | Task 4 |
| 流式缓冲窗口延迟原型 | Task 5 |
| Go 单仓骨架 | Task 6 |
| PostgreSQL schema 与迁移框架 | Task 13 |
| LiteLLM 容器接三家供应商 | Task 1 |
| 透明直通 Edge：ak- 校验 | Task 8、14、15 |
| 透明直通 Edge：映射上游密钥并转发 | Task 16 |
| 透明直通 Edge：流式支持 | Task 17 |
| 用量与审计落 ClickHouse | Task 12、13、18 |
| 定价模型：输入/输出单价 | Task 9 |
| 定价模型：缓存命中价 | Task 9 |
| 定价模型：思考 token 计费 | Task 9 |
| 定价模型：阶梯价 | Task 9 |
| 定价模型：按时间生效的价格版本 | Task 10 |
| 上游密钥加密存储（PRD 非功能需求） | Task 7 |

**已知的范围外项**（属于 P1.2 及以后，本计划不做）：
- 从数据库加载密钥与价格（`cmd/edge/main.go` 目前用内存实现，Task 19 用临时装配验证端到端）
- Casdoor 接入与 SSO
- 组织树与角色的管理 API
- 控制台前端
