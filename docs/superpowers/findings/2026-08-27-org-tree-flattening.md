# P0 结论：组织树拍平方案

| 项目 | 内容 |
|---|---|
| 日期 | 2026-08-27 |
| 背景 | Airlock 支持任意深度组织树，LiteLLM 固定为 Organization → Team → User 三层 |
| 验证环境 | 本地 docker compose 起的 LiteLLM + postgres，`http://localhost:4000`，master key `sk-airlock-master-dev-only` |

## 测试树与创建记录

样本树（对应真实企业的常见五层形态）：

```
集团（L1）
└── 研发中心（L2）
    └── 平台产品部（L3）
        └── 网关组（L4）
            └── 数据面小组（L5，叶子）
```

按「组织 = 树根下第一层，团队 = 叶子节点」的方案实际创建，返回体节选：

- `POST /organization/new`（`organization_alias: "orgtree-研发中心"`）→
  `organization_id: 2aaa949c-def5-4c0e-9986-f876986f1080`，`litellm_budget_table.max_budget: 1000.0`
- `POST /team/new`（`team_alias: "orgtree-数据面小组-v2"`，带 `organization_id`）→
  `team_id: 6a4dca5e-ff77-4261-a40b-539271ed814e`，正确挂到上面的 organization_id 下，`max_budget: 100.0`

`GET /organization/info?organization_id=...` 验证了挂载关系：返回体的 `teams` 数组里能看到该团队，说明
Organization → Team 的父子关系确实被 LiteLLM 记录并可查询——但这是**唯一**存在的层级关系，且只有这一层。

## LiteLLM 团队实体的实际字段

`GET /team/list` 返回的团队对象完整字段列表（对 5 个团队实测一致）：

```
access_group_ids, admins, allow_team_guardrail_config, blocked, budget_duration,
budget_limits, budget_reset_at, created_at, default_team_member_models, keys,
litellm_model_table, max_budget, max_parallel_requests, members, members_with_roles,
metadata, model_id, model_max_budget, model_spend, models, object_permission,
object_permission_id, organization_id, policies, router_settings, rpm_limit,
soft_budget, spend, team_alias, team_id, team_member_permissions, team_memberships,
tpm_limit, updated_at
```

**唯一表达层级关系的字段是 `organization_id`**（Team → Organization，单向、单层）。没有任何字段表达
Team → Team 或更深层级的父子关系：

- 没有 `parent_team_id`、`parent_id`、`path`、`depth` 之类的字段。
- `metadata` 是一个空闲散字典（`{}`），理论上 Airlock 可以自行往里塞一个 `parent_node_id`，但这只是
  **存储**，LiteLLM 自身的预算引擎、路由、鉴权逻辑完全不会读取或校验这个字段——塞进去的值对 LiteLLM
  的行为没有任何影响。

**实测确认**：向 `POST /team/new` 传入一个编造的 `parent_team_id: "nonexistent-parent-id"` 字段，请求
成功返回 200，团队被正常创建（`team_id: fe54cd12-b246-413d-8797-864e3c1d7240`），但返回体里完全不含
`parent_team_id`——该字段被 Pydantic 模型静默丢弃，不报错也不存储。这证明 LiteLLM 的 `NewTeamRequest`
schema（通过 `/openapi.json` 核实）里确实不存在任何团队嵌套的概念，不是文档没写而是接口没有。

`NewTeamRequest`（OpenAPI schema）字段全集同样印证：除 `organization_id` 外，无任何父子关系字段。
`LiteLLM_TeamTable`（DB 落地表结构）字段与 `/team/list` 返回一致，同样没有。

结论：**LiteLLM 的层级能力上限是 Organization → Team 两层，Team 之间不能互相嵌套。**

## 采用的映射方案

- Airlock 组织树的**根下第一层**（本例中的「研发中心」，即 L2）映射为一个 LiteLLM **Organization**。
  L1「集团」本身不在 LiteLLM 建任何实体——它只存在于 Airlock 自己的数据模型里，作为多个 LiteLLM
  Organization 的上级容器（例如一个集团下可能有多个业务线，各自对应一个 LiteLLM Organization）。
- 每个**持有 Key 的叶子节点**（本例中的「数据面小组」，即 L5）映射为一个 LiteLLM **Team**，并通过
  `organization_id` 挂载到其对应的 Organization 下。Key 在 `/key/generate` 时绑定到该 Team。
- **中间层不在 LiteLLM 侧建实体**。「平台产品部」（L3）、「网关组」（L4）只存在于 Airlock 自己的组织树
  数据库里，作为 Airlock 端展示、审批、策略挂载用的节点，不对应任何 LiteLLM Organization 或 Team。
- 之所以不给每个中间层都建一个「假团队」（即 Step 2 中额外创建的 `orgtree-平台产品部` 团队）：那样做
  只是把中间层伪装成了一个和叶子节点平级的 LiteLLM Team，它既不能表达"是谁的父节点"，也不会让 LiteLLM
  在请求时校验这个虚拟团队的预算（因为发起请求的 Key 绑定的是叶子团队，不是这个虚拟团队）。伪造出的
  实体只会污染 LiteLLM 侧的团队列表、增加运营混乱，不产生任何真实的强制力，因此不采用。

## 信息损失

| 树的层级 | 预算能否强制执行 | Airlock 能否准确展示 | 说明 |
|---|---|---|---|
| 第 1 层（集团） | 否 | 能 | LiteLLM 无对应实体。Airlock 侧知道该集团下辖哪些 LiteLLM Organization，可以调用 `/organization/info` 或 `/organization/list` 汇总多个 Organization 的 `spend` 做展示，但没有任何请求路径会在超支时被 LiteLLM 拦截。 |
| 第 2 层（研发中心） | 能 | 能 | 直接映射为 LiteLLM Organization，`litellm_budget_table.max_budget` 由 LiteLLM 原生强制执行；`spend`、`teams` 列表由 `/organization/info` 直接返回，展示准确、实时。 |
| 第 3 层（平台产品部） | 否 | 能（有条件） | LiteLLM 无对应实体，无法拦截。Airlock 能展示，但前提是 Airlock 自己的数据库记录了「哪些叶子团队（LiteLLM team_id）挂在这个节点下」，然后定期拉取每个叶子团队的 `spend` 字段做汇总求和——这是 Airlock 自己算出来的展示值，不是 LiteLLM 提供的。 |
| 第 4 层（网关组） | 否 | 能（有条件） | 同第 3 层：无 LiteLLM 实体、无强制力；展示依赖 Airlock 自己对下辖叶子团队 `spend` 的汇总，机制与第 3 层完全一致。 |
| 第 5 层（数据面小组，叶子） | 能 | 能 | 直接映射为 LiteLLM Team，预算、`spend`、`budget_reset_at` 均由 LiteLLM 原生管理和强制执行。 |

**五层树里，只有 2 层（映射为 Organization 的第 2 层、映射为 Team 的叶子层）的预算能被 LiteLLM 真正强制
执行；中间的第 1、3、4 层完全没有强制力，只能靠 Airlock 自己做"汇总展示"，且这种展示是准实时（依赖轮询
或事件驱动的汇总更新），而不是请求路径上的硬拦截。**

**Q3 实测/推理确认**：如果客户给「平台产品部」（L3）设置了预算，超限时会发生什么——

LiteLLM 处理一次请求时，只会检查两处预算：请求所用 Key 绑定的 **Team**（本例中的「数据面小组」）的
`max_budget`，以及该 Team 所属的 **Organization**（本例中的「研发中心」）的 `max_budget`。因为「平台产品
部」在 LiteLLM 里没有任何数据库行（没有 `budget_id`、没有 `litellm_budget_table` 记录），LiteLLM 的预算
校验代码路径里根本不存在一个可以拿来比较的对象——不是"检查了但放过"，而是**这一层的预算判断从未发生
过**。所以「平台产品部」的预算即使在 Airlock 侧的策略表里配置了数值，请求也会一路畅通地打到 LiteLLM、
消耗真实的模型调用配额，直到触发「数据面小组」自己的团队预算或「研发中心」的组织预算为止——这中间「平
台产品部」名义上设定的更严格限额完全不会被尊重。这一点通过 Step 2 里"编造 `parent_team_id` 被静默丢
弃"和 schema 层面确认"团队实体没有中间层归属字段"两个证据共同印证：LiteLLM 没有承载这层预算的数据结
构，自然也不可能在运行时执行它。

## 对客户沟通的影响

P1 阶段面对「我们部门树有五层」这个问题时的标准回答：

> "Airlock 的组织树支持任意深度，您五层部门结构在我们系统里可以完整建模、完整展示、完整审计。但受限于
> 底层网关（LiteLLM）目前只有『组织 - 团队』两级硬预算结构，我们把您树里第二层（比如"研发中心"）映射为
> 网关组织、每个实际领取 API Key 的末端小组映射为网关团队，这两层的预算是由网关在请求发生的那一刻硬性
> 拦截的，绝对不会超支。中间层级（比如"平台产品部""网关组"）的预算和用量，我们会准实时汇总子团队数据
> 展示给您（依赖定期拉取各末端团队的用量并求和，不是请求路径上的硬性拦截，所以会有秒级到分钟级的延
> 迟），方便您做预算规划和月度对账，但目前版本这些中间层的限额是"软限制"——不会在请求发生的瞬间被硬
> 性拦截，而是我们会在超支后第一时间告警。同样地，最顶层的集团级预算也遵循「展示 + 告警」而非硬性拦截
> 的模式——这与中间层的处理方式一致，因为集团这一层在网关侧同样没有对应的预算实体。如果您需要中间层
> 或集团层也做到硬性实时拦截，这是我们下一阶段（P2）自建配额引擎要解决的问题，我们可以把您这个诉求纳
> 入优先级评估。"

要点：不夸大（不能说"五层都能强制执行"），不掩盖缺口（明确说集团层和中间层都是软限制/告警，且展示是
"准实时"而非"实时"），给出时间线预期（P2 才能补上）。

## 对 P2 的输入

P2 自建配额引擎时，Edge 需要承担的判定职责：

1. **完整祖先链查询**：每次请求进来时，Edge 不能只看这条 Key 绑定的 LiteLLM team_id/organization_id，
   必须用 Airlock 自己的组织树数据（邻接表或路径枚举模型，支持任意深度）查出这个叶子节点的**全部祖先
   节点**（本例中五层全部），而不是只有 LiteLLM 认识的那两层。
2. **每一层独立的实时计数器**：对每个设置了预算策略的祖先节点（不管它在 LiteLLM 里有没有对应实体），
   Edge 都要维护一个自己的、请求级别实时更新的用量计数器（不能只依赖 LiteLLM 事后回传的 `spend`
   字段——那是异步/轮询更新的，有滞后，会在窗口期内被击穿）。
3. **请求前置拦截点**：预算判断必须发生在 Edge 把请求转发给 LiteLLM **之前**，任何一层（含中间层）超
   限就要在这一步拒绝，不能指望 LiteLLM 兜底——已验证 LiteLLM 对它不认识的层级完全不做任何检查。
4. **并发/竞态处理**：中间层预算没有 LiteLLM 底层数据库约束兜底，Edge 自己的计数器必须处理并发请求下
   的竞态（例如用 Redis 原子自增+回滚，或者数据库行级锁），否则会出现"多个并发请求同时通过检查、共同
   造成超支"的问题。
5. **独立的用量台账**：Edge/Airlock 需要按完整祖先路径（全部 5 层的节点 ID）记录每次调用的 token 数、
   成本，而不是只依赖 LiteLLM `/team/list`、`/organization/info` 这种只理解两层结构的汇总接口——中间层
   的展示汇总要从 Airlock 自己的台账里算，不能从 LiteLLM 拿。
6. **与 LiteLLM 数据的定期对账**：LiteLLM 仍然是"这次调用到底花了多少钱"的权威来源（它对接真实的模型
   定价）。Edge 自己算的用量需要定期跟 LiteLLM 侧的 `spend` 做差异核对，发现漂移要告警，避免 Edge 自己
   算错导致误拦截或漏拦截。
7. **策略节点与 LiteLLM 实体的映射表**：Edge 需要维护"哪些 Airlock 节点对应哪个 LiteLLM
   organization_id / team_id"的映射（对应第 2 层和叶子层），对没有 LiteLLM 实体的中间层节点，映射表里
   要能查出"这个节点下辖哪些叶子层 team_id"，用于汇总展示和用量归集。
