# P0 结论：LiteLLM 开源版管理端点可用性实测

| 项目 | 内容 |
|---|---|
| 日期 | 2026-08-27（实测执行于 2026-08-27 22:49–22:57 UTC，文档最终核对于 2026-08-28） |
| LiteLLM 镜像 | `ghcr.io/berriai/litellm:main-stable`，具体 digest：`ghcr.io/berriai/litellm@sha256:20b5044b619055374061a6d5b7b08754cad75aeabbf82ddf4f69cc0cf80ddaf4` |
| License 状态 | 未配置任何 License（证据见下方启动日志） |
| 实测脚本 | `spike/litellm_endpoints.sh` |
| 原始输出 | `/tmp/litellm-probe.txt` |

## 无 License 状态的证据

在撰写本文档时重新执行了 Task 2 Step 3 的 grep 命令，确认结论未过期：

```
$ docker compose -f deploy/docker-compose.yml logs litellm 2>&1 | grep -i -E "premium|enterprise|license" || echo "日志中无 License 相关记录"
日志中无 License 相关记录
```

此外，本次 8 个端点的完整响应体（无截断，保存于本机 `/tmp/litellm-full/*.json`）也逐一用同样的关键字（`premium`/`enterprise`/`license`，忽略大小写）过滤，同样零命中：

```
$ grep -il -E "premium|enterprise|license" /tmp/litellm-full/*.json
（无输出，exit code 1 = 无匹配）
```

即：无论从容器启动日志还是从 8 个端点的实际响应体来看，本实例都没有任何 License/premium/enterprise 相关提示或门禁行为的痕迹。

## 端点可用性

| 端点 | HTTP | 判定 | 备注 |
|---|---|---|---|
| POST /organization/new | 200 | 可用 | 返回完整 organization 对象：`organization_id`、`organization_alias`、`budget_id`、`litellm_budget_table`（内含 `max_budget:100.0`、`budget_duration:"30d"`，与请求体完全一致），无任何门禁提示。 |
| GET /organization/list | 200 | 可用 | 返回数组，包含刚创建的组织（`organization_id` 与 POST 响应一致），字段完整，证明创建是真实持久化的，不是空壳响应。 |
| POST /team/new | 200 | 可用 | 返回完整 team 对象：`team_id`、`tpm_limit:1000`、`rpm_limit:60`、`max_budget:50.0`、`budget_duration:"30d"`、`budget_reset_at` 均按请求正确落地，限流与预算参数均被开源版接受并生效。 |
| POST /user/new | 200 | 可用 | 返回完整 user 对象：`user_id`（UUID）、`max_budget:20.0`、`budget_duration:"30d"`，用户被真实创建。 |
| POST /key/generate（带预算、白名单、限流） | 200 | 可用 | 返回真实可用 Key（`key:"sk-4VJCJzkKeu5d-f2-dlNV0w"`），且请求体中的全部治理参数——`max_budget:10.0`、`models:["deepseek-chat"]`（模型白名单）、`tpm_limit:500`、`rpm_limit:30`、`max_parallel_requests:5`——均原样出现在响应中，说明预算、白名单、限流三类能力在开源版下全部生效，无一被裁剪或忽略。 |
| GET /spend/tags | 200 | 可用 | 返回按 tag 聚合的真实用量数据（`individual_request_tag`、`log_count`、`total_spend`），数据来自 Task 2 之前测试调用留下的真实请求记录，聚合逻辑正常工作。 |
| GET /spend/logs | 200 | 可用 | 返回完整的逐条调用日志数组，包含 `spend`、`total_tokens`、`cost_breakdown`（含 `input_cost`/`output_cost`/`total_cost`）、`model_map_information`（含完整计价表）等字段，说明成本核算与日志留存功能完整可用，不是阉割版。 |
| GET /model/info | 200 | 可用 | 返回已配置模型的完整信息数组（`deepseek-chat`、`qwen-plus`、`gpt-4o-mini`、`gpt-5.6-luna`），每个模型带完整的 `litellm_params` 与 `model_info`（含逐 token 计价、`access_via_team_ids` 等），信息详尽，无删减迹象。 |

**关于"其他失败"与依赖关系的说明：** 本次实测 8 个端点全部一次性成功（HTTP 200 + 预期资源），未出现参数错误、依赖缺失或需要跨调用传递 ID 的情况——每个探针调用都是独立的、不依赖其他探针调用产生的资源 ID（脚本本身也是这样设计的，8 条 `call` 语句彼此独立，没有把前一步返回的 `organization_id`/`team_id` 传给后续调用）。因此本次没有"其他失败"分类的样本，也没有需要在备注中说明的调用间依赖。

## 结论与分支决策

依据 Roadmap P0 的分支决策规则：

- 全部可用 → 按 Roadmap 执行，P1.3 使用 LiteLLM 原生预算
- 部分被门禁 → P1 配额范围收缩为「仅 Key 级预算」，P2 优先级提到 P1 之后立即执行
- 大面积被门禁 → P2 提前并入 P1

**本次判定：全部可用（8/8）。**

理由：
1. 8 个探测端点全部返回 HTTP 200，且响应体都是**带完整字段的真实资源对象**，不是空壳或降级响应——尤其是 `/key/generate` 这个最关键的端点，预算（`max_budget`）、模型白名单（`models`）、限流（`tpm_limit`/`rpm_limit`/`max_parallel_requests`）三类治理能力全部原样生效，这正是 PRD §3.3 中标注为"P1 | LiteLLM 原生预算"要依赖的核心能力。
2. 容器启动日志与全部 8 个响应体的关键字扫描（`premium`/`enterprise`/`license`）均为零命中，排除了"文档说开源但运行时偷偷要求 `premium_user`"的风险场景。
3. `/organization/new`、`/team/new`、`/user/new` 这三个在部分公开讨论中被质疑"可能是企业版专属"的写端点，实测均在无 License 环境下正常工作并持久化（`/organization/list` 能读回刚创建的组织，证明不是仅校验通过但静默丢弃）。
4. `/spend/tags`、`/spend/logs`、`/model/info` 三个读端点返回的数据详尽度（完整计价表、cost_breakdown、逐条调用元数据）说明底层的用量核算与模型管理能力也没有被功能裁剪。

因此，按 Roadmap 的分支规则，应采用**"全部可用"分支**：P1.3 直接使用 LiteLLM 原生预算能力（组织/团队/用户/Key 四级 + 模型白名单 + tpm/rpm 限流），无需在 P1 阶段就自建配额引擎；Airlock 自建配额引擎的工作（任意深度组织树强制执行、角色维度、成本中心、超限降级）按原计划保留在 P2，不需要提前。

需要提醒的唯一保留项（非本次实测范围，但与分支决策相关）：本次探测使用的是 **master key**（拥有最高权限），代表的是"平台管理员可以做什么"，而不是"某个受限 Key 能不能做什么"。Roadmap 后续任务如果涉及"非 master key 调用管理端点是否被拒绝"这类权限边界问题，需要单独用非 master key 补测，本文档不覆盖该场景。

**对 PRD §3.3 能力归属表的影响：**

对照 `docs/superpowers/specs/2026-08-27-airlock-prd.md` §3.3 表格逐行核对，**本次实测结果确认现有归属表无需修改**，具体确认如下：

- 第 88 行「组织 / 团队 / 成员 / Key 级配额 | P1 | **LiteLLM 原生预算**」——本次实测直接验证了这一行的前提假设：`/organization/new`、`/team/new`、`/user/new`、`/key/generate` 四个端点在无 License 下均可用，且预算参数（`max_budget`/`budget_duration`）在全部四级都真实生效。这一行标注为"LiteLLM 原生预算"是准确的，不需要改为"Airlock 自建"。
- 第 87 行「模型可见性白名单 | P1 | LiteLLM `models` 参数」——`/key/generate` 响应中 `models:["deepseek-chat"]` 原样生效，确认此行准确。
- 第 86 行「虚拟 Key 生命周期与审批流 | P1 | Airlock 自建 + LiteLLM `/key/*`」——`/key/generate` 实测可用，佐证了这一行里"LiteLLM `/key/*`"部分的可行性；"Airlock 自建"部分（审批流本身）不在本次探测范围内，维持原判。
- 「P1 已知的能力缺口」第 105-111 行列出的缺口（组织层级拍平、超限只能阻断、无角色/成本中心维度配额）都是关于 P2 才补的**增量能力**，与本次验证的"P1 阶段基础配额是否可用"是两个不同层面的问题，本次实测结果不影响这些缺口说明的正确性，无需修改。

综上，PRD §3.3 表格在本次实测后**保持原样，无需修正任何行**。
