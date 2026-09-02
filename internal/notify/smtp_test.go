package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSMTP 起一个只会说 SMTP 最小方言的假服务器，返回其地址与收到的全文。
//
// 用真的 TCP 而不是 mock net/smtp：这一层的价值就在于「我们发出去的
// 字节是不是合法的 SMTP 会话」，mock 掉就什么都没验证。
func fakeSMTP(t *testing.T, failAt string) (addr string, received func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var got strings.Builder

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

		w("220 fake ESMTP")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			mu.Lock()
			got.WriteString(line)
			mu.Unlock()
			trimmed := strings.TrimSpace(line)
			upper := strings.ToUpper(trimmed)

			switch {
			case inData:
				if trimmed == "." {
					inData = false
					w("250 OK")
				}
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				w("250-fake")
				w("250 OK")
			case strings.HasPrefix(upper, "MAIL FROM"):
				if failAt == "MAIL" {
					w("550 rejected")
					continue
				}
				w("250 OK")
			case strings.HasPrefix(upper, "RCPT TO"):
				if failAt == "RCPT" {
					w("550 no such user")
					continue
				}
				w("250 OK")
			case upper == "DATA":
				inData = true
				w("354 send it")
			case upper == "QUIT":
				w("221 bye")
				return
			default:
				w("250 OK")
			}
		}
	}()

	return ln.Addr().String(), func() string {
		mu.Lock()
		defer mu.Unlock()
		return got.String()
	}
}

func TestSMTPSenderDeliversMessage(t *testing.T) {
	addr, received := fakeSMTP(t, "")
	s := NewSMTPSender(SMTPConfig{Addr: addr, From: "airlock@example.com"})

	err := s.Send(context.Background(), Message{
		Recipient: "dev@example.com",
		Subject:   "有一张待审申请",
		Body:      "请前往控制台处理。",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return strings.Contains(received(), "dev@example.com")
	}, 2*time.Second, 20*time.Millisecond)

	full := received()
	require.Contains(t, full, "MAIL FROM:<airlock@example.com>")
	require.Contains(t, full, "RCPT TO:<dev@example.com>")
	require.Contains(t, full, "Subject:")
}

func TestSMTPSenderReturnsErrorOnRejectedRecipient(t *testing.T) {
	addr, _ := fakeSMTP(t, "RCPT")
	s := NewSMTPSender(SMTPConfig{Addr: addr, From: "airlock@example.com"})

	err := s.Send(context.Background(), Message{
		Recipient: "nobody@example.com", Subject: "x", Body: "y",
	})
	require.Error(t, err, "被拒的收件人必须报错，否则 outbox 会误标为已送达")
}

func TestSMTPSenderErrorOnUnreachableServer(t *testing.T) {
	s := NewSMTPSender(SMTPConfig{Addr: "127.0.0.1:1", From: "airlock@example.com"})

	err := s.Send(context.Background(), Message{
		Recipient: "dev@example.com", Subject: "x", Body: "y",
	})
	require.Error(t, err)
}

func TestSMTPSenderChannel(t *testing.T) {
	s := NewSMTPSender(SMTPConfig{Addr: "127.0.0.1:25", From: "a@b.c"})
	require.Equal(t, "email", s.Channel(),
		"必须与 notifications.channel 的 CHECK 约束取值一致")
}
