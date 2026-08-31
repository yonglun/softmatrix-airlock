package control

import (
	"net/http"

	"github.com/softmatrix/airlock/internal/litellm"
)

// SyncAPI 暴露 LiteLLM 同步的状态与手工触发。
//
// syncer 为 nil 表示同步未配置（未设 LITELLM_MASTER_KEY）。
// 那种情况下状态接口仍然要能答「未启用」，而不是 500。
type SyncAPI struct {
	syncer *Syncer
}

func NewSyncAPI(s *Syncer) *SyncAPI { return &SyncAPI{syncer: s} }

// HandleStatus 返回当前差异，不做任何写入。
//
// 现算现返而不读持久化的同步状态：对账本来就全量比对两侧，
// 再存一份派生状态只会多一处会和现实漂移的地方。
func (a *SyncAPI) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if a.syncer == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":      false,
			"missing_orgs": []string{}, "missing_teams": []string{},
			"mismatched_orgs": []string{}, "mismatched_teams": []string{},
			"extra_orgs": []string{}, "extra_teams": []string{},
		})
		return
	}

	plan, err := a.syncer.Plan(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "litellm_unreachable", "读取 LiteLLM 状态失败")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":          true,
		"in_sync":          plan.InSync(),
		"missing_orgs":     idsOfOrgs(plan.MissingOrgs),
		"mismatched_orgs":  idsOfOrgs(plan.MismatchedOrgs),
		"missing_teams":    idsOfTeams(plan.MissingTeams),
		"mismatched_teams": idsOfTeams(plan.MismatchedTeams),
		"extra_orgs":       orEmpty(plan.ExtraOrgs),
		"extra_teams":      orEmpty(plan.ExtraTeams),
	})
}

// HandleTrigger 立刻跑一轮对账并返回结果。
func (a *SyncAPI) HandleTrigger(w http.ResponseWriter, r *http.Request) {
	if a.syncer == nil {
		writeError(w, http.StatusServiceUnavailable, "sync_disabled",
			"未配置 LITELLM_MASTER_KEY，同步未启用")
		return
	}

	res, err := a.syncer.ReconcileOnce(r.Context())
	if err != nil && res.OrgsCreated+res.OrgsUpdated+res.TeamsCreated+res.TeamsUpdated == 0 {
		writeError(w, http.StatusBadGateway, "litellm_unreachable", "同步失败")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orgs_created":  res.OrgsCreated,
		"orgs_updated":  res.OrgsUpdated,
		"teams_created": res.TeamsCreated,
		"teams_updated": res.TeamsUpdated,
		"skipped":       res.Skipped,
		"extra_orgs":    orEmpty(res.ExtraOrgs),
		"extra_teams":   orEmpty(res.ExtraTeams),
		"errors":        orEmpty(res.Errors),
	})
}

// orEmpty 把 nil 切片变成空切片——JSON 里 null 和 [] 对前端是两回事，
// 天真的 .length 会在 null 上直接抛异常（P1.2a 验收真的踩过）。
func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func idsOfOrgs(list []litellm.Organization) []string {
	out := make([]string, 0, len(list))
	for _, o := range list {
		out = append(out, o.ID)
	}
	return out
}

func idsOfTeams(list []litellm.Team) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.ID)
	}
	return out
}
