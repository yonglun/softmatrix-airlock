package control

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/softmatrix/airlock/internal/cryptobox"
	"github.com/softmatrix/airlock/internal/notify"
)

// maxExecuteAttempts 是提额执行的重试上限。
// 无限重试会让一张坏单子每轮都刷一次错误日志，到点要停下来交给人。
const maxExecuteAttempts = 5

// maxDeliverAttempts 是通知投递的重试上限。
const maxDeliverAttempts = 5

// deliverBatchSize 是单轮投递的批量上限。
const deliverBatchSize = 50

type ApprovalWorkerDeps struct {
	Requests RequestStore
	Notifs   NotificationStore
	Keys     KeyStore
	Users    UserStore
	Admin    LiteLLMKeyAdmin
	Cipher   *cryptobox.Cipher
	Sender   notify.Sender
}

type ApprovalWorker struct {
	deps    ApprovalWorkerDeps
	trigger chan struct{}
}

func NewApprovalWorker(deps ApprovalWorkerDeps) *ApprovalWorker {
	return &ApprovalWorker{deps: deps, trigger: make(chan struct{}, 1)}
}

// ExecuteApprovedBumps 执行已批准的提额，返回成功执行的条数。
//
// 单条失败只计一次尝试并继续处理其余的——各申请单彼此独立，
// 一张坏单子不该把整个队列卡住。
func (w *ApprovalWorker) ExecuteApprovedBumps(ctx context.Context) (int, error) {
	list, err := w.deps.Requests.ListApprovedBumps(ctx)
	if err != nil {
		return 0, err
	}

	done, failures := 0, 0
	for _, r := range list {
		if err := w.executeOne(ctx, r); err != nil {
			failures++
			w.recordBumpFailure(ctx, r, err)
			continue
		}
		done++
	}
	if failures > 0 {
		return done, fmt.Errorf("有 %d 条提额执行失败", failures)
	}
	return done, nil
}

func (w *ApprovalWorker) executeOne(ctx context.Context, r *Request) error {
	if r.TargetKeyID == nil || r.BumpToBudget == nil {
		return fmt.Errorf("提额申请缺少目标密钥或额度")
	}
	k, err := w.deps.Keys.Get(ctx, *r.TargetKeyID)
	if err != nil {
		return err
	}
	upstreamKey, err := w.deps.Cipher.Decrypt(k.UpstreamKeyEnc)
	if err != nil {
		return fmt.Errorf("解密上游密钥失败: %w", err)
	}

	// 先记下原值再改：到期回收要照它恢复。
	var prev float64
	if k.MaxBudget != nil {
		prev = *k.MaxBudget
	}
	if err := w.deps.Admin.UpdateKeyBudget(ctx, upstreamKey, *r.BumpToBudget); err != nil {
		return err
	}
	if err := w.deps.Requests.MarkExecuted(ctx, r.ID, nil, &prev); err != nil {
		return err
	}
	w.notifyRequester(ctx, r, NotifyEventExecuted, "临时提额已生效",
		fmt.Sprintf("申请单 %s 的临时提额已生效，额度 %g，到期时间 %s。",
			r.ID, *r.BumpToBudget, r.BumpExpiresAt.Format(time.RFC3339)))
	return nil
}

func (w *ApprovalWorker) recordBumpFailure(ctx context.Context, r *Request, cause error) {
	if err := w.deps.Requests.RecordAttempt(ctx, r.ID, cause.Error()); err != nil {
		slog.Warn("记录提额执行失败失败", "request_id", r.ID, "err", err)
		return
	}
	if r.Attempts+1 >= maxExecuteAttempts {
		if err := w.deps.Requests.MarkFailed(ctx, r.ID, cause.Error()); err != nil {
			slog.Warn("标记提额执行失败失败", "request_id", r.ID, "err", err)
			return
		}
		slog.Error("提额执行重试耗尽，已标记失败",
			"request_id", r.ID, "attempts", r.Attempts+1, "err", cause)
	}
}

func (w *ApprovalWorker) notifyRequester(
	ctx context.Context, r *Request, event, subject, body string,
) {
	u, err := w.deps.Users.ByID(ctx, r.RequesterID)
	if err != nil || u.Email == "" {
		return
	}
	if err := w.deps.Notifs.Enqueue(ctx, &Notification{
		ID: newRequestID(), RequestID: r.ID, Event: event, Channel: "email",
		Recipient: u.Email, Subject: subject, Body: body,
	}); err != nil {
		slog.Warn("排入通知失败", "request_id", r.ID, "err", err)
	}
}

// ReclaimExpiredBumps 把已过期的临时提额恢复成原额度，返回回收条数。
//
// 用扫描而不是给每条提额起 time.AfterFunc：进程一重启定时器就没了，
// 临时提额会静默变成永久——这正是这类功能最不该出的错。
//
// 一个必须知道的后果：上游的判定是 spend >= max_budget。若用户在提额
// 期间花掉的钱已超过原额度，回收之后这把密钥会立刻被 429 阻断，
// 直到预算周期重置。语义上是对的（临时额度到期且已超出基础额度），
// 但它是个锋利的边角，产品文档与审批界面必须讲清楚。
func (w *ApprovalWorker) ReclaimExpiredBumps(ctx context.Context) (int, error) {
	list, err := w.deps.Requests.ListExpiredBumps(ctx, time.Now())
	if err != nil {
		return 0, err
	}

	done, failures := 0, 0
	for _, r := range list {
		if err := w.reclaimOne(ctx, r); err != nil {
			failures++
			slog.Error("回收临时提额失败，下一轮重试", "request_id", r.ID, "err", err)
			continue
		}
		done++
	}
	if failures > 0 {
		return done, fmt.Errorf("有 %d 条提额回收失败", failures)
	}
	return done, nil
}

func (w *ApprovalWorker) reclaimOne(ctx context.Context, r *Request) error {
	if r.TargetKeyID == nil || r.PrevBudget == nil {
		return fmt.Errorf("提额申请缺少目标密钥或原额度")
	}
	k, err := w.deps.Keys.Get(ctx, *r.TargetKeyID)
	if err != nil {
		return err
	}
	upstreamKey, err := w.deps.Cipher.Decrypt(k.UpstreamKeyEnc)
	if err != nil {
		return fmt.Errorf("解密上游密钥失败: %w", err)
	}
	if err := w.deps.Admin.UpdateKeyBudget(ctx, upstreamKey, *r.PrevBudget); err != nil {
		return err
	}
	if err := w.deps.Requests.MarkReclaimed(ctx, r.ID); err != nil {
		return err
	}
	w.notifyRequester(ctx, r, NotifyEventReclaimed, "临时提额已到期回收",
		fmt.Sprintf("申请单 %s 的临时提额已到期，额度已恢复为 %g。", r.ID, *r.PrevBudget))
	return nil
}

// DeliverPending 投递 outbox 里待发的通知，返回成功送达的条数。
func (w *ApprovalWorker) DeliverPending(ctx context.Context) (int, error) {
	list, err := w.deps.Notifs.ListPending(ctx, deliverBatchSize)
	if err != nil {
		return 0, err
	}

	sent, failures := 0, 0
	for _, n := range list {
		err := w.deps.Sender.Send(ctx, notify.Message{
			Recipient: n.Recipient, Subject: n.Subject, Body: n.Body,
		})
		if err == nil {
			if merr := w.deps.Notifs.MarkSent(ctx, n.ID); merr != nil {
				slog.Warn("标记通知已送达失败", "notification_id", n.ID, "err", merr)
			}
			sent++
			continue
		}

		failures++
		if n.Attempts+1 >= maxDeliverAttempts {
			if merr := w.deps.Notifs.MarkFailed(ctx, n.ID, err.Error()); merr != nil {
				slog.Warn("标记通知失败失败", "notification_id", n.ID, "err", merr)
			}
			slog.Error("通知投递重试耗尽，已标记失败",
				"notification_id", n.ID, "recipient", n.Recipient, "err", err)
			continue
		}
		if merr := w.deps.Notifs.RecordFailure(ctx, n.ID, err.Error()); merr != nil {
			slog.Warn("记录通知投递失败失败", "notification_id", n.ID, "err", merr)
		}
	}
	if failures > 0 {
		return sent, fmt.Errorf("有 %d 条通知投递失败", failures)
	}
	return sent, nil
}

// Nudge 请求尽快跑一轮。非阻塞；接收者为 nil 时是 no-op。
//
// trigger 容量为 1：连续的审批动作被合并成一轮，
// 不会每批准一张单子就触发一次全量扫描。
func (w *ApprovalWorker) Nudge() {
	if w == nil {
		return
	}
	select {
	case w.trigger <- struct{}{}:
	default:
	}
}

// Run 按 interval 周期跑三件事，并响应 Nudge，直到 ctx 被取消。
//
// 单轮失败只记日志不退出：上游或邮件服务器短暂不可用，
// 不该让整个审批流的后台处理停摆。
func (w *ApprovalWorker) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if n, err := w.ExecuteApprovedBumps(ctx); err != nil {
			slog.Error("执行提额失败，将在下轮重试", "err", err)
		} else if n > 0 {
			slog.Info("已执行临时提额", "count", n)
		}
		if n, err := w.ReclaimExpiredBumps(ctx); err != nil {
			slog.Error("回收提额失败，将在下轮重试", "err", err)
		} else if n > 0 {
			slog.Info("已回收到期提额", "count", n)
		}
		if n, err := w.DeliverPending(ctx); err != nil {
			slog.Error("投递通知失败，将在下轮重试", "err", err)
		} else if n > 0 {
			slog.Info("已投递通知", "count", n)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.trigger:
		}
	}
}
