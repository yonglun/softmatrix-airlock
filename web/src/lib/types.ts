// 这些类型必须与后端**实际的** wire 形状一一对应。
//
// 注意后端的键名并不统一：Org / User / RoleGrant 是领域结构体、没有
// json tag，序列化出来是 PascalCase；而 keyView / requestView 有
// json tag，是 snake_case。控制台是第一个消费者，只能如实适配。
// 统一 wire 形状值得做，但要动 P1.2a/b 的处理器与测试，不在本期范围内。

/** GET /api/orgs —— PascalCase（Org 没有 json tag） */
export type Org = {
  ID: string
  ParentID: string | null
  Name: string
  Path: string
  ExternalSource: string | null
  ExternalID: string | null
  IsKeyHolder: boolean
}

/** whoami 里的 user —— PascalCase（User 没有 json tag） */
export type User = {
  ID: string
  ExternalID: string
  Email: string
  DisplayName: string
  Status: string
  PrimaryOrgID: string | null
}

/** whoami 里的 grants —— PascalCase（RoleGrant 没有 json tag） */
export type RoleGrant = {
  ID: string
  UserID: string
  RoleID: string
  OrgID: string | null
}

/** GET /api/whoami */
export type Whoami = {
  user: User
  grants: RoleGrant[]
  global_permissions: string[]
  workbenches: string[]
}

/** GET /api/requests、/api/requests/to-approve —— snake_case（requestView 有 json tag） */
export type ApiRequest = {
  id: string
  kind: string
  status: string
  requester_id: string
  org_id: string
  reason: string
  key_name: string | null
  models: string[]
  target_key_id: string | null
  bump_to_budget: number | null
  bump_expires_at: string | null
  decided_by: string | null
  decided_at: string | null
  issued_key_id: string | null
  created_at: string
}

/** GET /api/orgs/{id}/keys、GET /api/keys/mine —— snake_case（keyView 有 json tag） */
export type ApiKey = {
  id: string
  key_prefix: string
  org_id: string
  user_id: string
  name: string
  status: string
  models: string[]
  max_budget: number | null
  budget_duration: string | null
  rpm_limit: number | null
  tpm_limit: number | null
  expires_at: string | null
  /** 轮换状态：共存窗口内旧凭据仍可用，到 prev_key_expires_at 为止 */
  rotated_at: string | null
  prev_key_expires_at: string | null
  created_at: string
}

/** 签发、轮换、领取的响应：keyView 加一个只出现这一次的明文 */
export type IssuedKey = ApiKey & { key: string }

/** 两个批量吊销端点的响应 */
export type RevokeResult = { revoked: number }
