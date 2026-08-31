// Package litellm 是 LiteLLM 管理 API 的客户端。
//
// 本包只认识 Organization 与 Team 两类实体，完全不知道 Airlock 的组织树、
// 路径语义或 is_key_holder——映射规则属于 internal/control，不属于这里。
// 这条边界由 Makefile 的 lint 检查强制。
package litellm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	BaseURL   string
	MasterKey string
	Timeout   time.Duration
}

type Client struct {
	cfg Config
	hc  *http.Client
}

const defaultTimeout = 10 * time.Second

func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Client{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout}}
}

// APIError 是上游返回的非 2xx 响应。
//
// 单独成类型是因为调用方需要按状态码区分处理：例如重复创建 Team 返回 400，
// 而重复创建 Organization 返回 500（LiteLLM 的真实行为，已实测）。
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("LiteLLM 返回 %d: %s", e.Status, e.Body)
}

const maxErrBody = 512

// do 发一次请求。body 为 nil 时不带请求体；out 为 nil 时丢弃响应体。
//
// 错误信息里只包含方法、路径与上游响应体，绝不包含请求头——
// Authorization 头里是 master key，进了日志就等于泄漏。
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求体失败（%s %s）: %w", method, path, err)
		}
		rdr = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("构造请求失败（%s %s）: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.MasterKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		// err 来自 net/http，不含请求头，可以安全带上。
		return fmt.Errorf("请求 LiteLLM 失败（%s %s）: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析 LiteLLM 响应失败（%s %s）: %w", method, path, err)
	}
	return nil
}
