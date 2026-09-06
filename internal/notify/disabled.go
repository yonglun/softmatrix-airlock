package notify

import (
	"context"
	"log/slog"
)

// DisabledSender 是未配置 SMTP 时的投递实现：只记一条日志，不发信。
//
// Send 返回 nil 而不是错误，是刻意的取舍。返回错误的话，
// ApprovalWorker.DeliverPending 会重试到 maxDeliverAttempts 耗尽再标
// failed，outbox 里堆满看起来像真故障的记录。
//
// 代价是 notifications.status 会被标成「已送达」，而实际没有邮件发出——
// 这一点在运维手册里写明。彻底干净的做法是把入队时硬编码的
// Channel: "email" 改成从 sender 取、禁用时记成 none，那要动审批流的
// 两处入队点，留作后续。
type DisabledSender struct{}

// Channel 与入队时硬编码的取值保持一致。
func (DisabledSender) Channel() string { return "email" }

func (DisabledSender) Send(_ context.Context, m Message) error {
	slog.Info("邮件通知未启用，跳过投递",
		"recipient", m.Recipient, "subject", m.Subject)
	return nil
}
