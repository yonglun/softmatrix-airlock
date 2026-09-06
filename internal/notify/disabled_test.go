package notify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 未配置 SMTP 时投递必须「成功」。
//
// 返回错误的话，ApprovalWorker.DeliverPending 会重试到上限再标 failed，
// outbox 里就堆满看起来像真故障的记录，运维每天都要来问一次。
func TestDisabledSenderReportsSuccess(t *testing.T) {
	err := DisabledSender{}.Send(context.Background(), Message{
		Recipient: "someone@example.com", Subject: "标题", Body: "正文",
	})

	require.NoError(t, err)
}

func TestDisabledSenderChannelIsEmail(t *testing.T) {
	// 入队时 channel 硬编码成 "email"（control/approval.go），
	// 接口注释要求 Channel() 与它一致，这里跟着写 email。
	require.Equal(t, "email", DisabledSender{}.Channel())
}

// 它必须能满足 Sender 接口，否则装配处编译不过。
func TestDisabledSenderSatisfiesSender(t *testing.T) {
	var _ Sender = DisabledSender{}
}
