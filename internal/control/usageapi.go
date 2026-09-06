package control

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/softmatrix/airlock/internal/authz"
	"github.com/softmatrix/airlock/internal/usage"
)

// 时间范围的默认值与上限。
//
// 上限取 92 天：它覆盖「看上个季度」这个最常见的长跨度需求，同时把单次
// 查询触及的分区数压在 4 个以内（usage_records 按月分区）。没有这道闸，
// 一个手滑的 from=2020 就会让 ClickHouse 扫全表。
const (
	defaultRangeDays = 7
	maxRangeDays     = 92
)

var (
	errRangeInvalid  = errors.New("时间格式无法解析")
	errRangeInverted = errors.New("起始时间晚于结束时间")
	errRangeTooLong  = errors.New("时间跨度超过上限")
)

// parseRange 解析 from/to 参数。两者都缺省时取最近 defaultRangeDays 天。
//
// 接受两种写法：RFC3339（带时区的精确时刻）与 YYYY-MM-DD（人在 URL 里
// 手写时的自然形式，不支持它会逼着用户去查时区偏移）。
func parseRange(q url.Values) (from, to time.Time, err error) {
	now := time.Now().UTC()

	to, err = parseRangeEnd(q.Get("to"), now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	from, err = parseRangeStart(q.Get("from"), to.AddDate(0, 0, -defaultRangeDays))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	if !from.Before(to) {
		return time.Time{}, time.Time{}, errRangeInverted
	}
	if to.Sub(from) > time.Duration(maxRangeDays)*24*time.Hour {
		return time.Time{}, time.Time{}, errRangeTooLong
	}
	return from, to, nil
}

// parseRangeStart 解析起始时间。裸日期解析成当天零点 UTC——
// 「从 9 月 1 日起」，起点就该是那一天的开始。
func parseRangeStart(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errRangeInvalid
}

// parseRangeEnd 解析结束时间。裸日期解析成**次日**零点 UTC，而不是当天零点。
//
// 查询用的是 ts < to 排他上界；若 to 解析成当天零点，「到 9 月 6 日」会把
// 9 月 6 日一整天的数据全部排除——这与用户在日期选择器里选到今天、
// 却看不到今天任何记录的直觉完全相反。次日零点才让排他上界真正
// 覆盖到选定的那一天结束。
func parseRangeEnd(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().AddDate(0, 0, 1), nil
	}
	return time.Time{}, errRangeInvalid
}

// UsageReader 是控制面需要的用量读取能力。
//
// 定义在 control 侧、由 internal/usage 实现——依赖方向单向，
// 与 LiteLLMKeyAdmin 同一套路。
type UsageReader interface {
	Summary(ctx context.Context, q usage.SummaryQuery) ([]usage.SummaryRow, error)
	Records(ctx context.Context, q usage.AuditQuery) ([]usage.AuditRow, error)
}

// UsageAPI 暴露用量报表与审计检索。
//
// reader 为 nil 表示未配置 ClickHouse（CLICKHOUSE_DSN 为空）。那种情况下
// 接口要答「未启用」而不是 500——与 SyncAPI 在 syncer 为 nil 时的处理同理。
type UsageAPI struct {
	reader   UsageReader
	orgs     OrgStore
	resolver *authz.Resolver
}

func NewUsageAPI(reader UsageReader, orgs OrgStore, resolver *authz.Resolver) *UsageAPI {
	return &UsageAPI{reader: reader, orgs: orgs, resolver: resolver}
}

// writeRangeError 把时间范围的领域错误映射成状态码。
func writeRangeError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errRangeTooLong):
		writeError(w, http.StatusBadRequest, "range_too_long",
			"时间跨度超过上限（最长 92 天），请收窄范围")
		return true
	case errors.Is(err, errRangeInverted):
		writeError(w, http.StatusBadRequest, "range_inverted", "起始时间必须早于结束时间")
		return true
	case errors.Is(err, errRangeInvalid):
		writeError(w, http.StatusBadRequest, "range_invalid",
			"时间格式无法解析，请用 2026-09-01 或 RFC3339")
		return true
	}
	return false
}

// costScope 判定调用者能看哪些组织的成本。
//
// 返回的 orgIDs 为空且 err 为 nil 表示「不限范围」（持全局 cost:read_all）。
// 两种权限都没有时返回 errNoCostPermission。
var errNoCostPermission = errors.New("没有查看成本的权限")

func (a *UsageAPI) costScope(ctx context.Context, u *User) ([]string, error) {
	subj := subjectOf(u)

	globalAll, _, err := a.resolver.Scopes(ctx, subj, authz.PermCostReadAll)
	if err != nil {
		return nil, err
	}
	if globalAll {
		return nil, nil // 不限范围
	}

	globalRead, nodes, err := a.resolver.Scopes(ctx, subj, authz.PermCostRead)
	if err != nil {
		return nil, err
	}
	if globalRead {
		return nil, nil
	}
	if len(nodes) == 0 {
		return nil, errNoCostPermission
	}

	// 节点级权限覆盖整棵子树。Subtree 已经用了带分隔符的前缀匹配，
	// 因此同前缀的兄弟节点（/root/rd 与 /root/rd2）不会互相串台。
	seen := map[string]bool{}
	out := []string{}
	for _, node := range nodes {
		subtree, err := a.orgs.Subtree(ctx, node)
		if err != nil {
			if errors.Is(err, ErrOrgNotFound) {
				continue // 授予指向的节点已被删除，跳过
			}
			return nil, err
		}
		for _, o := range subtree {
			if !seen[o.ID] {
				seen[o.ID] = true
				out = append(out, o.ID)
			}
		}
	}
	return out, nil
}

func (a *UsageAPI) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if a.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "analytics_disabled",
			"用量分析未启用：未配置 CLICKHOUSE_DSN")
		return
	}
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
		return
	}

	q := r.URL.Query()
	from, to, err := parseRange(q)
	if writeRangeError(w, err) {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "解析时间范围失败")
		return
	}

	orgIDs, err := a.costScope(r.Context(), u)
	if errors.Is(err, errNoCostPermission) {
		writeError(w, http.StatusForbidden, "permission_denied", "没有查看成本的权限")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "权限判定失败")
		return
	}

	// ?org_id= 是在可见范围内的进一步收窄，不能用来越权。
	if want := q.Get("org_id"); want != "" {
		narrowed, err := a.narrow(r.Context(), orgIDs, want)
		if err != nil {
			writeError(w, http.StatusForbidden, "permission_denied", "没有查看该节点成本的权限")
			return
		}
		orgIDs = narrowed
	}

	groupBy := q.Get("group_by")
	if groupBy == "" {
		groupBy = usage.GroupByOrg
	}
	rows, err := a.reader.Summary(r.Context(), usage.SummaryQuery{
		From: from, To: to, GroupBy: groupBy, OrgIDs: orgIDs,
	})
	if errors.Is(err, usage.ErrUnknownGroupBy) {
		writeError(w, http.StatusBadRequest, "unknown_group_by",
			"未知的聚合维度，可选：org / user / model / day")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "analytics_unreachable", "查询用量失败")
		return
	}

	if q.Get("format") == "csv" {
		writeCSV(w, "usage-summary.csv",
			[]string{"维度", "请求数", "输入 token", "输出 token", "成本(元)"},
			summaryCSVRows(rows))
		return
	}
	writeJSON(w, http.StatusOK, summaryViews(rows))
}

// narrow 把可见范围收窄到 want 这个节点的子树，并校验它确实在可见范围内。
func (a *UsageAPI) narrow(ctx context.Context, visible []string, want string) ([]string, error) {
	subtree, err := a.orgs.Subtree(ctx, want)
	if err != nil {
		return nil, err
	}
	wanted := []string{}
	for _, o := range subtree {
		wanted = append(wanted, o.ID)
	}
	if visible == nil {
		return wanted, nil // 不限范围的人可以随便收窄
	}
	allowed := map[string]bool{}
	for _, id := range visible {
		allowed[id] = true
	}
	out := []string{}
	for _, id := range wanted {
		if allowed[id] {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, errNoCostPermission
	}
	return out, nil
}

type summaryView struct {
	Key          string `json:"key"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicro    int64  `json:"cost_micro"`
}

func summaryViews(rows []usage.SummaryRow) []summaryView {
	out := make([]summaryView, 0, len(rows))
	for _, r := range rows {
		out = append(out, summaryView{
			Key: r.Key, Requests: r.Requests,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
			CostMicro: r.CostMicro,
		})
	}
	return out
}

// HandleAuditRecords 按条件检索调用流水。
//
// 权限由中间件按全局 audit:read 判完（审计是合规职能，维持全局口径），
// 因此这里不再做可见范围收窄——与 HandleSummary 刻意不同。
func (a *UsageAPI) HandleAuditRecords(w http.ResponseWriter, r *http.Request) {
	if a.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "analytics_disabled",
			"用量分析未启用：未配置 CLICKHOUSE_DSN")
		return
	}

	q := r.URL.Query()
	from, to, err := parseRange(q)
	if writeRangeError(w, err) {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "解析时间范围失败")
		return
	}

	rows, err := a.reader.Records(r.Context(), usage.AuditQuery{
		From: from, To: to,
		OrgID:      q.Get("org_id"),
		UserID:     q.Get("user_id"),
		Model:      q.Get("model"),
		OnlyErrors: q.Get("only_errors") == "true",
		Limit:      atoiOr(q.Get("limit"), 50),
		Offset:     atoiOr(q.Get("offset"), 0),
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "analytics_unreachable", "查询调用流水失败")
		return
	}

	if q.Get("format") == "csv" {
		writeCSV(w, "audit-records.csv",
			[]string{"时间", "请求 ID", "组织", "成员", "密钥", "模型",
				"状态码", "耗时(ms)", "首字(ms)", "输入 token", "输出 token", "成本(元)", "错误类型"},
			auditCSVRows(rows))
		return
	}
	writeJSON(w, http.StatusOK, auditViews(rows))
}

// atoiOr 解析十进制整数，失败或为空时返回缺省值。
// 翻页参数写错不该让整个请求 400——退回缺省页更有用。
func atoiOr(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

type auditView struct {
	Timestamp    time.Time `json:"ts"`
	RequestID    string    `json:"request_id"`
	OrgID        string    `json:"org_id"`
	UserID       string    `json:"user_id"`
	KeyID        string    `json:"key_id"`
	Model        string    `json:"model"`
	StatusCode   int       `json:"status_code"`
	LatencyMS    int       `json:"latency_ms"`
	TTFTMS       int       `json:"ttft_ms"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostMicro    int64     `json:"cost_micro"`
	ErrorType    string    `json:"error_type"`
}

func auditViews(rows []usage.AuditRow) []auditView {
	out := make([]auditView, 0, len(rows))
	for _, r := range rows {
		out = append(out, auditView{
			Timestamp: r.Timestamp, RequestID: r.RequestID, OrgID: r.OrgID,
			UserID: r.UserID, KeyID: r.KeyID, Model: r.Model,
			StatusCode: r.StatusCode, LatencyMS: r.LatencyMS, TTFTMS: r.TTFTMS,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
			CostMicro: r.CostMicro, ErrorType: r.ErrorType,
		})
	}
	return out
}
