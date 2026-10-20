# Gemini 与全局 SSE 流式接口注释帧保活 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Gemini SSE 流式接口的保活帧统一为符合 SSE 规范的 `: ping\n\n` 注释帧，将心跳周期调整为 30 秒（保持首包错误等待 10 秒），并消除静默元事件误重置定时器与多候选并发重复发心跳的问题，防止反向代理或网关在 120 秒空闲超时时断开流式连接。

**Architecture:** 在 `internal/api/generate.go` 中将首包等待时长 `streamStartTimeout`（10s）与流式心跳间隔 `streamHeartbeatInterval`（30s）解耦，并通过 `shouldResetHeartbeat` 过滤流末尾汇总的元事件；在 `internal/api/errors.go` 与 `internal/api/gemini.go` 中统一使用 `writeSSEHeartbeat` 发送 `: ping\n\n`；在 `geminiStream` 和 `streamChatCompletion` 中记录 `lastWrite` 时间戳，对多候选并发下的心跳进行节流。

**Tech Stack:** Go 标准库 (`net/http`, `net/http/httptest`, `time`, `sync`, `testing`)

## Global Constraints

- 所有 SSE 流式接口（Gemini `alt=sse`、OpenAI Chat Completions、Responses、Interactions）统一发送 `: ping\n\n` 注释帧。
- Gemini 流式接口未携带 `alt=sse` 时返回 JSON 数组流（`Content-Type: application/json; charset=UTF-8`），不得写入 `: ping\n\n`。
- 首包等待时长 `streamStartTimeout` 保持 `10 * time.Second`；流式心跳间隔 `streamHeartbeatInterval` 设为 `30 * time.Second`。
- 汇总型元事件（`EventUsage`、`EventFinish`、`EventGrounding`、`EventCitation`）不得重置流式心跳定时器。
- 多候选并发（`candidateCount > 1` 或 `n > 1`）时，距离上次实际向客户端写出未满 `streamHeartbeatInterval` 的重复心跳调用必须跳过。

---

### Task 1: 拆分首包等待与心跳间隔常量并过滤非输出元事件重置

**Files:**
- Modify: `internal/api/generate.go:20-211`
- Modify: `internal/api/gemini_output_test.go`

**Interfaces:**
- Consumes: `aistudio.Event`, `aistudio.EventKind` (`internal/aistudio/event.go`)
- Produces:
  - `const streamStartTimeout = 10 * time.Second` (`internal/api/generate.go`)
  - `const streamHeartbeatInterval = 30 * time.Second` (`internal/api/generate.go`)
  - `func shouldResetHeartbeat(event aistudio.Event) bool` (`internal/api/generate.go`)

- [ ] **Step 1: 在 `internal/api/gemini_output_test.go` 末尾编写失败测试**

在 `internal/api/gemini_output_test.go` 末尾追加以下测试函数（需在 import 中引入 `"time"`）：

```go
// TestStreamHeartbeatConstantsAndResetRule 验证首包等待、心跳间隔常量与心跳重置规则
func TestStreamHeartbeatConstantsAndResetRule(t *testing.T) {
	if streamStartTimeout != 10*time.Second {
		t.Fatalf("streamStartTimeout = %v, want 10s", streamStartTimeout)
	}
	if streamHeartbeatInterval != 30*time.Second {
		t.Fatalf("streamHeartbeatInterval = %v, want 30s", streamHeartbeatInterval)
	}
	for _, kind := range []aistudio.EventKind{
		aistudio.EventText,
		aistudio.EventReasoning,
		aistudio.EventToolCall,
		aistudio.EventExecutableCode,
		aistudio.EventCodeExecutionResult,
		aistudio.EventMedia,
		aistudio.EventThoughtSignature,
	} {
		if !shouldResetHeartbeat(aistudio.Event{Kind: kind}) {
			t.Errorf("shouldResetHeartbeat(%s) = false, want true", kind)
		}
	}
	for _, kind := range []aistudio.EventKind{
		aistudio.EventUsage,
		aistudio.EventFinish,
		aistudio.EventGrounding,
		aistudio.EventCitation,
	} {
		if shouldResetHeartbeat(aistudio.Event{Kind: kind}) {
			t.Errorf("shouldResetHeartbeat(%s) = true, want false", kind)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认其失败**

Run: `go test ./internal/api -run TestStreamHeartbeatConstantsAndResetRule -v`
Expected: FAIL with `undefined: streamStartTimeout` and `undefined: shouldResetHeartbeat`

- [ ] **Step 3: 在 `internal/api/generate.go` 中实现常量拆分与 `shouldResetHeartbeat`**

修改 `internal/api/generate.go`：

1. 将第 20 行的 `const streamHeartbeatInterval = 10 * time.Second` 替换为：
```go
const (
	// streamStartTimeout 是写出流式响应头前等待首个事件的时长
	streamStartTimeout = 10 * time.Second
	// streamHeartbeatInterval 是流式响应静默时发送保活心跳的间隔
	streamHeartbeatInterval = 30 * time.Second
)
```

2. 在 `awaitStreamStart`（原第 109 行）中将 `time.NewTimer(streamHeartbeatInterval)` 改为 `time.NewTimer(streamStartTimeout)`：
```go
func awaitStreamStart(ctx context.Context, events <-chan aistudio.Event) (<-chan aistudio.Event, error) {
	timer := time.NewTimer(streamStartTimeout)
	defer timer.Stop()
```

3. 在 `consumeEventsWithHeartbeat` 的 `case event, ok := <-events:` 分支（原第 208 行）中，仅当 `shouldResetHeartbeat(event)` 为 `true` 时重置心跳定时器，并新增 `shouldResetHeartbeat` 函数：
```go
			if emit != nil {
				if err := emit(event); err != nil {
					return result, err
				}
			}
			if shouldResetHeartbeat(event) {
				resetHeartbeat()
			}
		}
	}
}

// shouldResetHeartbeat 判断事件是否属于流中途产生客户端输出的增量事件
func shouldResetHeartbeat(event aistudio.Event) bool {
	switch event.Kind {
	case aistudio.EventUsage, aistudio.EventFinish, aistudio.EventGrounding, aistudio.EventCitation:
		return false
	default:
		return true
	}
}
```

- [ ] **Step 4: 运行测试确认其通过**

Run: `go test ./internal/api -run TestStreamHeartbeatConstantsAndResetRule -v`
Expected: PASS

- [ ] **Step 5: 提交 Task 1**

```bash
git add internal/api/generate.go internal/api/gemini_output_test.go
git commit -m "feat(api): 拆分首包等待与 30s 流式心跳间隔并过滤非增量元事件重置

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: 统一 Gemini SSE 心跳为 `: ping\n\n` 并实现多候选心跳节流

**Files:**
- Modify: `internal/api/errors.go:82-96`
- Modify: `internal/api/gemini.go:1364-1428`
- Modify: `internal/api/openai.go:946-966`
- Modify: `internal/api/gemini_output_test.go`

**Interfaces:**
- Consumes:
  - `streamHeartbeatInterval` (`internal/api/generate.go`)
  - `writeSSEHeartbeat(w http.ResponseWriter) error` (`internal/api/errors.go`)
- Produces:
  - `geminiStream` 结构体的 `lastWrite time.Time` 字段与��流后的 `heartbeat() error` (`internal/api/gemini.go`)
  - `streamChatCompletion` 中的 `lastWrite time.Time` 心跳节流 (`internal/api/openai.go`)

- [ ] **Step 1: 在 `internal/api/gemini_output_test.go` 末尾编写失败测试**

在 `internal/api/gemini_output_test.go` 的 `import` 中补充 `"net/http"` 与 `"net/http/httptest"`，并在文件末尾添加以下测试：

```go
// TestGeminiStreamHeartbeatSSEAndJSON 验证 Gemini 流在 alt=sse 下发送标准注释帧且多候选节流，JSON 数组模式下不发送
func TestGeminiStreamHeartbeatSSEAndJSON(t *testing.T) {
	t.Run("alt=sse writes standard SSE comment and throttles concurrent heartbeats", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse", nil)
		rec := httptest.NewRecorder()
		stream, err := openGeminiStream(rec, req)
		if err != nil {
			t.Fatalf("openGeminiStream failed: %v", err)
		}

		// 刚打开流时距离 lastWrite 未满 streamHeartbeatInterval，首次并发调用应被节流跳过
		if err := stream.heartbeat(); err != nil {
			t.Fatalf("throttled heartbeat failed: %v", err)
		}
		if got := rec.Body.String(); got != "" {
			t.Fatalf("expected empty body when throttled, got %q", got)
		}

		// 模拟静默已��� streamHeartbeatInterval
		stream.lastWrite = time.Now().Add(-streamHeartbeatInterval)
		if err := stream.heartbeat(); err != nil {
			t.Fatalf("heartbeat failed: %v", err)
		}
		if got := rec.Body.String(); got != ": ping\n\n" {
			t.Fatalf("body = %q, want %q", got, ": ping\n\n")
		}

		// 紧随其后的第二个候选协程心跳应被节流，不再重复写出
		if err := stream.heartbeat(); err != nil {
			t.Fatalf("second candidate heartbeat failed: %v", err)
		}
		if got := rec.Body.String(); got != ": ping\n\n" {
			t.Fatalf("body after second heartbeat = %q, want single %q", got, ": ping\n\n")
		}
	})

	t.Run("json array stream does not write heartbeat", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:streamGenerateContent", nil)
		rec := httptest.NewRecorder()
		stream, err := openGeminiStream(rec, req)
		if err != nil {
			t.Fatalf("openGeminiStream failed: %v", err)
		}
		stream.lastWrite = time.Now().Add(-streamHeartbeatInterval)
		if err := stream.heartbeat(); err != nil {
			t.Fatalf("heartbeat failed: %v", err)
		}
		stream.close()
		if got := rec.Body.String(); got != "[]" {
			t.Fatalf("json stream body = %q, want %q", got, "[]")
		}
	})
}
```

- [ ] **Step 2: 运行测试确认其失败**

Run: `go test ./internal/api -run TestGeminiStreamHeartbeatSSEAndJSON -v`
Expected: FAIL with `stream.lastWrite undefined (type *geminiStream has no field or method lastWrite)`

- [ ] **Step 3: 修改 `internal/api/errors.go`、`internal/api/gemini.go` 与 `internal/api/openai.go`**

1. 在 `internal/api/errors.go` 中删除 `writeGeminiHeartbeat`（第 82-88 行），保留 `writeSSEHeartbeat`：
```go
// writeSSEHeartbeat 发送心跳并传播连接错误
func writeSSEHeartbeat(w http.ResponseWriter) error {
	if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
```

2. 在 `internal/api/gemini.go` 中更新 `geminiStream` 及其方法（第 1364-1415 行）：
```go
// geminiStream 写出 streamGenerateContent 的响应块：alt=sse 时为 SSE，否则与官方 REST 一样为逐个对象写出的 JSON 数组
type geminiStream struct {
	w         http.ResponseWriter
	sse       bool
	mu        sync.Mutex
	written   bool
	lastWrite time.Time
}

// openGeminiStream 按 alt 参数写出流式响应头
func openGeminiStream(w http.ResponseWriter, r *http.Request) (*geminiStream, error) {
	stream := &geminiStream{w: w, sse: r.URL.Query().Get("alt") == "sse", lastWrite: time.Now()}
	if stream.sse {
		return stream, streamHeaders(w)
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	return stream, http.NewResponseController(w).Flush()
}

// write 写出一个响应块
func (stream *geminiStream) write(value map[string]any) error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.sse {
		err := writeSSE(stream.w, "", value)
		if err == nil {
			stream.lastWrite = time.Now()
		}
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	prefix := "["
	if stream.written {
		prefix = "\r\n,\r\n"
	}
	stream.written = true
	if _, err := io.WriteString(stream.w, prefix+string(data)); err != nil {
		return err
	}
	err = http.NewResponseController(stream.w).Flush()
	if err == nil {
		stream.lastWrite = time.Now()
	}
	return err
}

// heartbeat 在 SSE 流静默达到心跳间隔时写出注释帧，JSON 数组不写
func (stream *geminiStream) heartbeat() error {
	if !stream.sse {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if !stream.lastWrite.IsZero() && time.Since(stream.lastWrite) < streamHeartbeatInterval {
		return nil
	}
	err := writeSSEHeartbeat(stream.w)
	if err == nil {
		stream.lastWrite = time.Now()
	}
	return err
}
```

3. 在 `internal/api/openai.go` 的 `streamChatCompletion`（第 950-965 行）中同步加入 `lastWrite` 节流：
```go
	var writing sync.Mutex
	lastWrite := time.Now()
	write := func(index int, delta map[string]any, finish *string) error {
		writing.Lock()
		defer writing.Unlock()
		err := writeChatChunk(w, id, created, request.Model, index, delta, finish, request.StreamOptions.IncludeUsage)
		if err == nil {
			lastWrite = time.Now()
		}
		return err
	}
	for index := range choices.events {
		if err := write(index, map[string]any{"role": "assistant", "content": ""}, nil); err != nil {
			return
		}
	}
	heartbeat := func() error {
		writing.Lock()
		defer writing.Unlock()
		if time.Since(lastWrite) < streamHeartbeatInterval {
			return nil
		}
		err := writeSSEHeartbeat(w)
		if err == nil {
			lastWrite = time.Now()
		}
		return err
	}
```

- [ ] **Step 4: 运行全部测试确认通过**

Run: `go test ./internal/api/... -v`
Expected: PASS

- [ ] **Step 5: 提交 Task 2**

```bash
git add internal/api/errors.go internal/api/gemini.go internal/api/openai.go internal/api/gemini_output_test.go
git commit -m "feat(api): 统一 Gemini SSE 心跳为注释帧并对多候选心跳节流

Co-Authored-By: Claude <noreply@anthropic.com>"
```
