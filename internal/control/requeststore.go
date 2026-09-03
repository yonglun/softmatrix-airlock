package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type postgresRequestStore struct {
	db *sql.DB
}

func NewPostgresRequestStore(db *sql.DB) *postgresRequestStore {
	return &postgresRequestStore{db: db}
}

const requestColumns = `id, kind, status, requester_id, org_id, reason,
	key_name, models, target_key_id, bump_to_budget, bump_expires_at,
	prev_budget, reclaimed_at, decided_by, decided_at, executed_at,
	issued_key_id, last_error, attempts, created_at`

func scanRequest(sc interface{ Scan(...any) error }) (*Request, error) {
	var r Request
	var models []byte
	if err := sc.Scan(&r.ID, &r.Kind, &r.Status, &r.RequesterID, &r.OrgID, &r.Reason,
		&r.KeyName, &models, &r.TargetKeyID, &r.BumpToBudget, &r.BumpExpiresAt,
		&r.PrevBudget, &r.ReclaimedAt, &r.DecidedBy, &r.DecidedAt, &r.ExecutedAt,
		&r.IssuedKeyID, &r.LastError, &r.Attempts, &r.CreatedAt); err != nil {
		return nil, err
	}
	if len(models) > 0 {
		if err := json.Unmarshal(models, &r.Models); err != nil {
			return nil, fmt.Errorf("解析 models 失败: %w", err)
		}
	}
	return &r, nil
}

func scanRequests(rows *sql.Rows) ([]*Request, error) {
	defer func() { _ = rows.Close() }()
	out := []*Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描申请单行失败: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Create 写入一张新申请单。两类申请共用这条 INSERT，
// 各自没填的列留 NULL——形状由 requests_kind_shape_check 兜住。
func (s *postgresRequestStore) Create(ctx context.Context, r *Request) error {
	var models any
	if r.Kind == RequestKindNewKey {
		raw, err := json.Marshal(orEmptyStrings(r.Models))
		if err != nil {
			return fmt.Errorf("序列化 models 失败: %w", err)
		}
		models = raw
	}
	// RETURNING 把数据库算出的默认值（status、created_at）读回调用方的
	// 结构体——否则响应里的 created_at 永远是零值，字段等于白写。
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO requests (id, kind, requester_id, org_id, reason,
		                      key_name, models, target_key_id, bump_to_budget, bump_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING status, created_at`,
		r.ID, r.Kind, r.RequesterID, r.OrgID, r.Reason,
		r.KeyName, models, r.TargetKeyID, r.BumpToBudget, r.BumpExpiresAt).
		Scan(&r.Status, &r.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建申请单失败: %w", err)
	}
	return nil
}

func (s *postgresRequestStore) Get(ctx context.Context, id string) (*Request, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+requestColumns+` FROM requests WHERE id = $1`, id)
	r, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRequestNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询申请单失败: %w", err)
	}
	return r, nil
}

func (s *postgresRequestStore) ListByRequester(
	ctx context.Context, userID string,
) ([]*Request, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM requests
		 WHERE requester_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("按申请人查询失败: %w", err)
	}
	return scanRequests(rows)
}

// Decide 写入审批结论。
//
// WHERE status = 'pending' 是乐观并发：两个管理员同时点批准，
// 第二次必须失败，而不是把 decided_by 覆盖成后点的那个人。
func (s *postgresRequestStore) Decide(ctx context.Context, id, status, decidedBy string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE requests SET status = $1, decided_by = $2, decided_at = $3
		WHERE id = $4 AND status = 'pending'`,
		status, decidedBy, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("更新审批结论失败: %w", err)
	}
	return s.guardOutcome(ctx, res, id, ErrRequestNotPending)
}

// MarkExecuted 把已批准的申请标记为已执行。
//
// 与 Decide 同一条乐观并发：只有还停在 approved 的单子能转 executed。
// 少了这个守卫，两个并发的领取都会看到 approved、都签发一把密钥，
// 一次审批换来两把——这是审批流最不能出的错。
func (s *postgresRequestStore) MarkExecuted(
	ctx context.Context, id string, issuedKeyID *string, prevBudget *float64,
) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE requests SET status = 'executed', executed_at = $1,
		                    issued_key_id = $2, prev_budget = $3
		WHERE id = $4 AND status = 'approved'`,
		time.Now().UTC(), issuedKeyID, prevBudget, id)
	if err != nil {
		return fmt.Errorf("标记执行完成失败: %w", err)
	}
	return s.guardOutcome(ctx, res, id, ErrRequestNotApproved)
}

// guardOutcome 把「零行受影响」翻译成「不存在」或「状态不对」。
// 分辨这两者，调用方才能返回正确的状态码、或正确地善后。
func (s *postgresRequestStore) guardOutcome(
	ctx context.Context, res sql.Result, id string, onWrongState error,
) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, gerr := s.Get(ctx, id); gerr != nil {
			return gerr
		}
		return onWrongState
	}
	return nil
}

func (s *postgresRequestStore) MarkFailed(ctx context.Context, id, reason string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE requests SET status = 'failed', last_error = $1 WHERE id = $2`, reason, id)
	if err != nil {
		return fmt.Errorf("标记执行失败失败: %w", err)
	}
	return affectedOrNotFound(res)
}

// RecordAttempt 计一次失败尝试但不改状态，留在原位等下一轮重试。
func (s *postgresRequestStore) RecordAttempt(ctx context.Context, id, lastErr string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE requests SET attempts = attempts + 1, last_error = $1 WHERE id = $2`,
		lastErr, id)
	if err != nil {
		return fmt.Errorf("记录执行尝试失败: %w", err)
	}
	return affectedOrNotFound(res)
}

// ListApprovedBumps 返回等待执行的提额申请。
// 只捞 quota_bump——已批准的 new_key 由申请人自助领取，不归 worker 管。
func (s *postgresRequestStore) ListApprovedBumps(ctx context.Context) ([]*Request, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM requests
		 WHERE kind = 'quota_bump' AND status = 'approved'
		 ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("查询待执行提额失败: %w", err)
	}
	return scanRequests(rows)
}

// ListExpiredBumps 返回已生效、已过期、尚未回收的提额。
// reclaimed_at IS NULL 让回收天然幂等：回收过的下一轮就捞不到了。
func (s *postgresRequestStore) ListExpiredBumps(
	ctx context.Context, now time.Time,
) ([]*Request, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM requests
		 WHERE kind = 'quota_bump' AND status = 'executed'
		   AND reclaimed_at IS NULL AND bump_expires_at <= $1
		 ORDER BY bump_expires_at`, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("查询到期提额失败: %w", err)
	}
	return scanRequests(rows)
}

func (s *postgresRequestStore) MarkReclaimed(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE requests SET reclaimed_at = $1 WHERE id = $2 AND reclaimed_at IS NULL`,
		time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("标记已回收失败: %w", err)
	}
	return affectedOrNotFound(res)
}

func affectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRequestNotFound
	}
	return nil
}

// ---- 通知 outbox ----

type postgresNotificationStore struct {
	db *sql.DB
}

func NewPostgresNotificationStore(db *sql.DB) *postgresNotificationStore {
	return &postgresNotificationStore{db: db}
}

const notificationColumns = `id, request_id, event, channel, recipient, subject, body,
	status, attempts, last_error, sent_at, created_at`

// Enqueue 把一条通知排入 outbox。只写库，不发送——
// 发送放在审批的关键路径上，邮件服务器一慢就会拖垮审批接口，
// 一失败还可能让已经成功的审批回滚。
func (s *postgresNotificationStore) Enqueue(ctx context.Context, n *Notification) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO notifications (id, request_id, event, channel, recipient, subject, body)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		n.ID, n.RequestID, n.Event, n.Channel, n.Recipient, n.Subject, n.Body)
	if err != nil {
		return fmt.Errorf("排入通知失败: %w", err)
	}
	return nil
}

// ListPending 取一批待投递的通知，最老的先发。
func (s *postgresNotificationStore) ListPending(
	ctx context.Context, limit int,
) ([]*Notification, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+notificationColumns+` FROM notifications
		 WHERE status = 'pending' ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待投递通知失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []*Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.RequestID, &n.Event, &n.Channel, &n.Recipient,
			&n.Subject, &n.Body, &n.Status, &n.Attempts, &n.LastError,
			&n.SentAt, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("扫描通知行失败: %w", err)
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (s *postgresNotificationStore) MarkSent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET status = 'sent', sent_at = $1, attempts = attempts + 1
		 WHERE id = $2`, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("标记通知已送达失败: %w", err)
	}
	return nil
}

// RecordFailure 计一次失败但保持 pending，下一轮继续重试。
// 邮件服务器抖一下不该让通知丢掉——那意味着审批人永远不知道有待审申请。
func (s *postgresNotificationStore) RecordFailure(ctx context.Context, id, lastErr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET attempts = attempts + 1, last_error = $1 WHERE id = $2`,
		lastErr, id)
	if err != nil {
		return fmt.Errorf("记录通知投递失败失败: %w", err)
	}
	return nil
}

// MarkFailed 终止重试，留待人工查看。
func (s *postgresNotificationStore) MarkFailed(ctx context.Context, id, lastErr string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET status = 'failed', attempts = attempts + 1, last_error = $1
		 WHERE id = $2`, lastErr, id)
	if err != nil {
		return fmt.Errorf("标记通知失败失败: %w", err)
	}
	return nil
}

// ListAllPending 返回全部待审申请，最老的排前面。
func (s *postgresRequestStore) ListAllPending(ctx context.Context) ([]*Request, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM requests
		 WHERE status = 'pending' ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("查询待审申请失败: %w", err)
	}
	return scanRequests(rows)
}

// ListPendingForOrgPaths 返回 org 落在这些路径子树内的待审申请。
//
// 子树匹配必须加分隔符再比前缀：/root/rd 是 /root/rd2 的前缀，但 rd2
// 并不在 rd 的子树里。少了这一笔，一个部门的申请会泄漏给隔壁部门的
// 审批人。这个陷阱在 P1.2b 的权限判定、P1.3b 的审批人查找、P1.3c 的
// 子树吊销里各踩过一次，这是第四次。
//
// paths 为空直接返回空列表：既省一次查询，也避免空列表被退化成
// 「不加限制」这个最危险的方向。
func (s *postgresRequestStore) ListPendingForOrgPaths(
	ctx context.Context, paths []string,
) ([]*Request, error) {
	if len(paths) == 0 {
		return []*Request{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+requestColumns+` FROM requests
		WHERE status = 'pending'
		  AND org_id IN (
		      SELECT o.id FROM organizations o
		      WHERE o.path = ANY($1)
		         OR EXISTS (
		             SELECT 1 FROM unnest($1::text[]) AS p(path)
		             WHERE o.path LIKE p.path || '/%'
		         )
		  )
		ORDER BY created_at`, paths)
	if err != nil {
		return nil, fmt.Errorf("按可见范围查询待审申请失败: %w", err)
	}
	return scanRequests(rows)
}
