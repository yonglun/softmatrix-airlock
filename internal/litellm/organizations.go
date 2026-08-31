package litellm

// Organization 是 LiteLLM 的组织实体。
//
// 只保留 Airlock 会读写的两个字段——上游返回体有二十多个字段，
// 全量映射会让本包被上游的 schema 变动牵着走。
type Organization struct {
	ID    string `json:"organization_id"`
	Alias string `json:"organization_alias"`
}
