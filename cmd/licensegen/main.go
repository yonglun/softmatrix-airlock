// Command licensegen 签发 Airlock 的授权文件。
//
// 这是内部工具，不随产品交付。它需要签发私钥，而私钥只存在于签发方的
// 离线介质上——客户手上的二进制里只有公钥。
//
//	# 一次性：生成密钥对。公钥打到 stdout（贴进 internal/license/pubkey.go），
//	# 私钥写到 -out，权限 0600。
//	licensegen -genkey -out ~/.airlock/license-signing.key
//
//	# 日常：签发一份 license
//	licensegen -key ~/.airlock/license-signing.key \
//	  -customer "某某银行" -id AL-2026-0007 -seats 200 -expires 2027-09-06
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/softmatrix/airlock/internal/license"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run() error {
	genkey := flag.Bool("genkey", false, "生成一对新的签发密钥")
	out := flag.String("out", "", "-genkey 时私钥的写入路径")
	keyPath := flag.String("key", "", "签发私钥文件路径")
	customer := flag.String("customer", "", "客户名称")
	id := flag.String("id", "", "license 编号，如 AL-2026-0007")
	seats := flag.Int("seats", 0, "席位数，必须为正")
	expires := flag.String("expires", "", "到期日期 YYYY-MM-DD，当天仍然可用")
	flag.Parse()

	if *genkey {
		return generateKey(*out)
	}
	return signLicense(*keyPath, *customer, *id, *seats, *expires)
}

func generateKey(out string) error {
	if out == "" {
		return fmt.Errorf("-genkey 需要 -out 指定私钥写入路径")
	}
	if _, err := os.Stat(out); err == nil {
		// 覆盖一份还在用的签发私钥 = 所有现存客户的 license 再也签不出续期版本。
		return fmt.Errorf("%s 已存在；换密钥对是不兼容变更，请先确认后手动移走旧文件", out)
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return fmt.Errorf("生成密钥对失败: %w", err)
	}
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		return fmt.Errorf("写入私钥失败: %w", err)
	}

	fmt.Println("私钥已写入", out, "（权限 0600），请立刻离线备份。")
	fmt.Println("把下面这行贴进 internal/license/pubkey.go 的 trustedPublicKeyBase64：")
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

func signLicense(keyPath, customer, id string, seats int, expires string) error {
	if keyPath == "" {
		return fmt.Errorf("缺少 -key")
	}
	if expires == "" {
		return fmt.Errorf("缺少 -expires（格式 YYYY-MM-DD）")
	}

	// 裸日期签成次日零点 UTC：合同上写「授权至 9 月 6 日」，
	// 9 月 6 日当天就该是可用的。校验端用的是排他上界 now < expires_at。
	day, err := time.Parse("2006-01-02", expires)
	if err != nil {
		return fmt.Errorf("-expires 应为 YYYY-MM-DD，收到 %q", expires)
	}
	expiresAt := day.UTC().AddDate(0, 0, 1)

	now := time.Now().UTC()
	if !now.Before(expiresAt) {
		return fmt.Errorf("-expires %s 已经过去了，签出来的 license 一出生就是过期的", expires)
	}

	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("读取私钥失败: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("私钥文件不是合法 base64: %w", err)
	}
	if len(seed) != ed25519.PrivateKeySize {
		return fmt.Errorf("私钥长度应为 %d 字节，实际 %d", ed25519.PrivateKeySize, len(seed))
	}

	// Payload.Validate 会挡住 seats<=0、customer 为空、id 为空，
	// 不在这里重复一遍——两处校验迟早会漂移。
	out, err := license.Sign(ed25519.PrivateKey(seed), license.Payload{
		LicenseID: id,
		Customer:  customer,
		IssuedAt:  now,
		ExpiresAt: expiresAt,
		Seats:     seats,
	})
	if err != nil {
		return err
	}

	fmt.Println(string(out))
	return nil
}
