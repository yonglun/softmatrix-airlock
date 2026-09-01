package notify

import (
	"context"
	"fmt"
	"mime"
	"net/smtp"
	"strings"
)

type SMTPConfig struct {
	// Addr 形如 host:port。
	Addr string
	From string
}

type SMTPSender struct {
	cfg SMTPConfig
}

func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	return &SMTPSender{cfg: cfg}
}

func (s *SMTPSender) Channel() string { return "email" }

// Send 投递一封纯文本邮件。
//
// 不做认证与 TLS：本地 mail 容器与多数企业内网 relay 都不需要，
// 而把没验证过的认证路径写进来只会制造「看起来支持」的假象。
// 真实部署需要认证时再按实际环境补，那时才有东西可测。
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := buildMessage(s.cfg.From, m)
	if err := smtp.SendMail(s.cfg.Addr, nil, s.cfg.From,
		[]string{m.Recipient}, []byte(msg)); err != nil {
		return fmt.Errorf("投递邮件失败（收件人 %s）: %w", m.Recipient, err)
	}
	return nil
}

// buildMessage 拼出最小可用的 RFC 5322 报文。
// 主题按 RFC 2047 编码，否则中文主题在多数客户端里会是乱码。
func buildMessage(from string, m Message) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + m.Recipient + "\r\n")
	b.WriteString("Subject: " + encodeHeader(m.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(m.Body)
	return b.String()
}

func encodeHeader(s string) string {
	return mime.QEncoding.Encode("UTF-8", s)
}
