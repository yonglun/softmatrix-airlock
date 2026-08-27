// 测量 SSE 流式响应在不同缓冲窗口下的首字延迟（TTFT）。
// 缓冲窗口 = 在向下游吐出内容前，先攒够多少个字符。
// 用法：go run ./spike/streambuffer -windows 0,32,128,512 -runs 5
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	base := flag.String("base", "http://localhost:4000", "LiteLLM 地址")
	key := flag.String("key", "sk-airlock-master-dev-only", "master key")
	model := flag.String("model", "deepseek-chat", "模型名")
	windowsArg := flag.String("windows", "0,32,128,512", "缓冲窗口字符数，逗号分隔")
	runs := flag.Int("runs", 5, "每个窗口重复次数")
	flag.Parse()

	var windows []int
	for _, s := range strings.Split(*windowsArg, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			fmt.Fprintf(os.Stderr, "无效的窗口值 %q: %v\n", s, err)
			os.Exit(1)
		}
		windows = append(windows, n)
	}

	fmt.Printf("%-10s %-10s %-10s %-10s %-10s\n", "窗口(字符)", "样本数", "TTFT-p50", "TTFT-p95", "TTFT-max")
	for _, w := range windows {
		var samples []time.Duration
		for i := 0; i < *runs; i++ {
			d, err := measure(*base, *key, *model, w)
			if err != nil {
				fmt.Fprintf(os.Stderr, "窗口 %d 第 %d 次失败: %v\n", w, i+1, err)
				continue
			}
			samples = append(samples, d)
		}
		if len(samples) == 0 {
			fmt.Printf("%-10d %-10s\n", w, "全部失败")
			continue
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		fmt.Printf("%-10d %-10d %-10s %-10s %-10s\n",
			w, len(samples),
			samples[pct(len(samples), 50)].Round(time.Millisecond),
			samples[pct(len(samples), 95)].Round(time.Millisecond),
			samples[len(samples)-1].Round(time.Millisecond))
	}
}

func pct(n, p int) int {
	i := n * p / 100
	if i >= n {
		i = n - 1
	}
	return i
}

// measure 发起一次流式请求，返回「攒够 window 个字符」所耗的时间。
// window = 0 表示不缓冲，第一个内容字符到达即计时结束。
func measure(base, key, model string, window int) (time.Duration, error) {
	reqBody := map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "用两百字介绍一下长江。"}},
		"stream":     true,
		"max_tokens": 400,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := make([]byte, 300)
		n, _ := resp.Body.Read(body)
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body[:n])
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	accumulated := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(payload, []byte("[DONE]")) {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			accumulated += len([]rune(c.Delta.Content))
		}
		if accumulated > window {
			return time.Since(start), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("流结束时仍未攒够 %d 个字符（实际 %d）", window, accumulated)
}
