package edge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/softmatrix/airlock/internal/openai"
)

// sseScanBufferMax 是单个 SSE 帧的最大长度。
// 默认 bufio.Scanner 上限只有 64KB，长上下文的首帧可能超过。
const sseScanBufferMax = 4 * 1024 * 1024

// streamOutcome 是流式转发结束后的结果。
type streamOutcome struct {
	usage *openai.Usage
	model string
	ttft  time.Duration
	// scanErr 是 scanner.Err()：流干净结束（EOF）时为 nil，
	// 网络中断或单帧超过 sseScanBufferMax（bufio.ErrTooLong）时非 nil。
	scanErr error
}

// ensureIncludeUsage 保证请求带上 stream_options.include_usage=true，
// 否则上游不会在流末尾发送 usage，Edge 就无从记账。
//
// 返回 injected=true 表示这个选项是 Edge 加的，此时末尾的 usage 块
// 不属于客户端预期的输出，转发时必须剥掉。
func ensureIncludeUsage(body []byte) (out []byte, injected bool, err error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("解析请求体失败: %w", err)
	}

	if raw, ok := payload["stream_options"]; ok {
		var opts map[string]json.RawMessage
		if err := json.Unmarshal(raw, &opts); err != nil {
			return nil, false, fmt.Errorf("解析 stream_options 失败: %w", err)
		}
		// 客户端已经表过态（无论 true 还是 false），一律尊重，不覆盖。
		if _, exists := opts["include_usage"]; exists {
			return body, false, nil
		}
		opts["include_usage"] = json.RawMessage("true")
		merged, err := json.Marshal(opts)
		if err != nil {
			return nil, false, fmt.Errorf("序列化 stream_options 失败: %w", err)
		}
		payload["stream_options"] = merged
	} else {
		payload["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}

	out, err = json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("序列化请求体失败: %w", err)
	}
	return out, true, nil
}

// pipeStream 逐帧转发 SSE，同时抓取 usage、模型名与首字延迟。
// stripUsageChunk 为 true 时，只含 usage 的那一块不下发给客户端。
func pipeStream(w http.ResponseWriter, body io.Reader, stripUsageChunk bool, start time.Time) streamOutcome {
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), sseScanBufferMax)

	var outcome streamOutcome
	var ttftSet bool

	for scanner.Scan() {
		line := scanner.Bytes()

		payload, isData := openai.ParseSSEData(line)
		if !isData {
			// 空行、event:、注释一律原样透传，保持 SSE 语义完整。
			writeLine(w, flusher, line)
			continue
		}

		if openai.IsDone(payload) {
			writeLine(w, flusher, line)
			continue
		}

		// 每一块都可能带 usage——不能只看 IsUsageOnlyChunk 判定的那些块。
		// 非 OpenAI 标准的上游可能把 usage 和内容塞进同一块（choices 非空）。
		if u, model, err := openai.ExtractUsage(payload); err == nil {
			if outcome.usage == nil && openai.HasUsage(payload) {
				outcome.usage = &u
			}
			if outcome.model == "" && model != "" {
				outcome.model = model
			}
		}

		if openai.IsUsageOnlyChunk(payload) {
			if stripUsageChunk {
				continue // 这是 Edge 自己要来的，不下发
			}
			writeLine(w, flusher, line)
			continue
		}

		// 内容块：记录首字延迟
		if !ttftSet {
			outcome.ttft = time.Since(start)
			ttftSet = true
		}
		writeLine(w, flusher, line)
	}

	outcome.scanErr = scanner.Err()
	return outcome
}

func writeLine(w http.ResponseWriter, flusher http.Flusher, line []byte) {
	_, _ = w.Write(line)
	_, _ = w.Write([]byte("\n"))
	if flusher != nil {
		flusher.Flush()
	}
}
