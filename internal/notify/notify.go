// Package notify 负责把消息投递到外部渠道。
//
// 本包只知道「怎么把一条消息发出去」，完全不知道 Airlock 的审批流、
// 申请单或组织树——消息内容由调用方组装好传进来。
// 这条边界由 Makefile 的 lint 检查强制。
package notify

import "context"

// Message 是一条待投递的消息。
type Message struct {
	Recipient string
	Subject   string
	Body      string
}

// Sender 把消息投递到某个渠道。
type Sender interface {
	// Channel 返回渠道标识，必须与 notifications.channel 的取值一致。
	Channel() string
	Send(ctx context.Context, m Message) error
}
