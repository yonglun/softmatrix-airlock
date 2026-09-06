package control

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/softmatrix/airlock/internal/license"
)

// licenseView 是 GET /api/license 的响应形状。
// 带 json tag，走 snake_case——与 keyView / summaryView / auditView 一致。
type licenseView struct {
	Status    string     `json:"status"`
	Customer  string     `json:"customer,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Seats     int        `json:"seats"`
	SeatsUsed int        `json:"seats_used"`
	// DaysRemaining 过期后为负数。试用（不设到期）时省略。
	DaysRemaining *int `json:"days_remaining,omitempty"`
}

// Snapshot 组装当前授权状态，含实时席位占用。
//
// 这是低频的页面级调用，每次现查一遍库即可，不需要缓存。
func (g *LicenseGate) Snapshot(ctx context.Context, now time.Time) (licenseView, error) {
	// 未装配时不该 nil 解引用。路由层的 licenseH 已经会在 License 为 nil 时
	// 换成 501 桩，这里是第二道保险。
	if g == nil {
		return licenseView{
			Status: string(license.StatusTrial), Seats: license.TrialSeats,
		}, nil
	}
	lic := g.License()

	used, err := g.users.CountActive(ctx)
	if err != nil {
		return licenseView{}, err
	}

	v := licenseView{
		Status:    string(g.Status(now)),
		Customer:  lic.Customer,
		Seats:     lic.Seats,
		SeatsUsed: used,
	}
	if !lic.ExpiresAt.IsZero() {
		expires := lic.ExpiresAt
		v.ExpiresAt = &expires
		// 向上取整：还剩 20 天零 3 小时就该显示 20 天而不是 19 天。
		days := int(math.Ceil(expires.Sub(now).Hours() / 24))
		v.DaysRemaining = &days
	}
	return v, nil
}

// HandleGet 回答当前授权状态。
//
// 刻意只要求已登录、不加权限门槛：红条要对所有人显示，否则普通成员只会
// 撞上一串没头没脑的 402，而没人告诉他发生了什么。
func (g *LicenseGate) HandleGet(w http.ResponseWriter, r *http.Request) {
	snap, err := g.Snapshot(r.Context(), time.Now())
	if err != nil {
		slog.Error("读取授权状态失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "读取授权状态失败")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}
