# Gemini 与全局 SSE 流式接口注释帧保活设计方案

## 1. 背景与问题

通过反向代理或网关访问当前服务的 Gemini 流式接口（`POST /v1beta/models/{model}:streamGenerateContent?alt=sse`）时，若网关配置了 120 秒空闲读超时（如 Nginx `proxy_read_timeout 120s` 或云网关默认超时），在长思考或慢响应场景下可能触发连接超时断开。

排查现有实现发现以下问题：
1. **非标准注释帧**：`internal/api/errors.go` 中的 `writeGeminiHeartbeat` 仅向客户端写出单个换行符 `\n`，而非符合 SSE（Server-Sent Events）规范的注释帧（如 `: ping\n\n`）。部分网关、缓冲层或客户端解析器不将孤立换行符识别为有效 SSE 帧，甚至可能引发缓冲或解析异常。
2. **心跳间隔偏短且与首包等待耦合**：`internal/api/generate.go` 中 `streamHeartbeatInterval` 固定为 `10s`，同时被 `awaitStreamStart`（响应头写出前等待首个事件以捕获上游早期错误）和流式心跳复用，不符合每 30-60 秒发送保活帧的预期。
3. **静默事件误重置心跳计时器**：在 `consumeEventsWithHeartbeat` 中，每当从上游通道收到一个 `aistudio.Event` 并调用 `emit(event)` 后，无论该事件是否实际向客户端写出了字节（例如 `EventGrounding`、`EventCitation` 等在流末尾才汇总输出的元事件，或在未输出思考时单独到达的 `EventThoughtSignature`），都会无条件调用 `resetHeartbeat()` 重置定时器。
4. **多候选并发重复心跳**：当 `candidateCount > 1`（或 OpenAI `n > 1`）时，`choices.run` 为每个候选启动独立的 `consumeStreamEvents` 协程，各协程各自维护定时器，在静默期到期时会同时写出多份重复的心跳帧。

## 2. 设计目标

1. **统一标准 SSE 注释帧**：所有 SSE 流式接口（包括 Gemini `alt=sse`、OpenAI Chat Completions、Responses、Interactions）统一使用 `: ping\n\n` 作为保活注释帧。
2. **合理的心跳周期与首包等待分离**：
   - 保持首包等待时长 `streamStartTimeout = 10 * time.Second`，确保在 10 秒内若上游无事件，服务端尽早写出 HTTP 200 及 `text/event-stream` 响应头，开启流式保活。
   - 将流式心跳周期设为 `streamHeartbeatInterval = 30 * time.Second`（处于 30-60 秒区间，在 120 秒超时窗口内可提供至少 3 次保活容错）。
3. **精准保活计时与多候选节流**：
   - 仅当��件实际触发客户端写出时重置心跳计时，避免静默元事件推迟心跳。
   - 多候选并发时，共享最后一次向客户端写出的时间戳，确保同一静默周期内只发送一次 `: ping\n\n`。
4. **保持非 SSE 格式兼容**：Gemini 接口未传 `alt=sse` 时返回的是分块 JSON 数组（`Content-Type: application/json`），不写入 `: ping\n\n` 以免破坏 JSON 语法。

## 3. 详细设计

### 3.1 常量拆分 (`internal/api/generate.go`)

将原有的单一常量拆分为两个职责明确的常量：

```go
// streamStartTimeout 是写出流式响应头前等待首个事件的时长
const streamStartTimeout = 10 * time.Second

// streamHeartbeatInterval 是流式响应静默时发送保活心跳的间隔
const streamHeartbeatInterval = 30 * time.Second
```

- `awaitStreamStart` 使用 `streamStartTimeout`（10s）。
- `consumeEventsWithHeartbeat` 与 `responses_background.go` 使用 `streamHeartbeatInterval`（30s）。

### 3.2 统一 SSE 心跳函数 (`internal/api/errors.go` & `internal/api/gemini.go`)

1. 删除 `internal/api/errors.go` 中的 `writeGeminiHeartbeat`。
2. `internal/api/gemini.go` 中 `geminiStream.heartbeat()` 改为调用 `writeSSEHeartbeat(stream.w)`，写出 `: ping\n\n` ���立即 `Flush()`：

```go
// writeSSEHeartbeat 发送 SSE 注释帧心跳并传播连接错误
func writeSSEHeartbeat(w http.ResponseWriter) error {
	if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
```

### 3.3 精准心跳计时与多候选节流 (`internal/api/generate.go`, `internal/api/gemini.go`, `internal/api/openai.go`)

1. **避免非输出事件误重置心跳计时 (`internal/api/generate.go`)**：
   - 在 `consumeEventsWithHeartbeat` 中定义辅助判断 `shouldResetHeartbeat(event aistudio.Event) bool`，对于流中途不直接产出增量帧的汇总型元事件（`EventUsage`、`EventFinish`、`EventGrounding`、`EventCitation`）不重置 `heartbeatTimer`。
2. **基于最后写出时间的多候选心跳节流 (`internal/api/gemini.go`, `internal/api/openai.go`)**：
   - 在 `geminiStream` 中维护 `lastWrite time.Time` 字段：
     - `openGeminiStream` 初始化 `lastWrite: time.Now()`。
     - `stream.write` 成功写出数据帧并 `Flush()` 后，在互斥锁内更新 `stream.lastWrite = time.Now()`。
     - `stream.heartbeat()` 在 `stream.sse == true` 时获取互斥锁，若 `!stream.lastWrite.IsZero() && time.Since(stream.lastWrite) < streamHeartbeatInterval` 则直接返回 `nil`；否则���用 `writeSSEHeartbeat(stream.w)` 写出 `: ping\n\n` 并更新 `stream.lastWrite = time.Now()`。
   - 在 `openai.go` 的 `streamChatCompletion` 中采用相同的 `lastWrite time.Time` 保护机制，防止 `n > 1` 时多个候选协程同时写出重复的 `: ping\n\n`。

### 3.4 数据流与时序

以 Gemini `streamGenerateContent?alt=sse` 为例：
1. 客户端发起请求，服务端调用 `s.startChatChoices(r.Context(), request, count, true)`。
2. `startChatChoices` 调用 `s.service.Generate` 获取上游事件流后，调用 `awaitStreamStart(ctx, upstream)`：
   - 若 10 秒内（`streamStartTimeout`）上游返回首个事件（或错误），立即返回；若为错误则在写出响应头前按 HTTP 错误码返回 JSON 错误体。
   - 若 10 秒内上游仍在思考尚未产生首个事件，`awaitStreamStart` 定时器到期返回原事件流。
3. 进入 `streamGemini`，调用 `openGeminiStream(w, r)`：
   - 写出 `Content-Type: text/event-stream`、`Cache-Control: no-cache`、`Connection: keep-alive` 及 `HTTP 200 OK`，并立即 `Flush()`。
   - 记录 `stream.lastWrite = time.Now()`。
4. 进入 `consumeStreamEvents` 循环：
   - 每当有候选事件写出：`stream.write` 获取互斥锁，调用 `writeSSE(stream.w, "", payload)` 写出 `data: {...}\n\n` 并 `Flush()`，更新 `stream.lastWrite = time.Now()`，重置该协程的 30 秒心跳定时器。
   - 每当静默满 30 秒（`streamHeartbeatInterval`）：`heartbeatTimer` 到期，调用 `stream.heartbeat()`。
   - `stream.heartbeat()` 在 `stream.sse == true` 时获取互斥锁，检查距离 `stream.lastWrite` 是否已达到 `streamHeartbeatInterval`（多候选并发时仅首个到期的协程满足条件），满足则调用 `writeSSEHeartbeat(stream.w)` 写出 `: ping\n\n` 并 `Flush()`，同时更新 `stream.lastWrite = time.Now()`。
5. 流正常结束时，写出包含 `finishReason` 与 `usageMetadata` 的最终 `data: {...}\n\n` 帧。

### 3.5 错误处理

- 当客户端或网关中途断开连接时，`writeSSEHeartbeat` 中的 `io.WriteString` 或 `http.NewResponseController(w).Flush()` 会返回写入错误。
- `consumeEventsWithHeartbeat` 捕获 `heartbeat()` 返回的错误后，立即中止事件循环并返回该错误。
- 外层 `choices.run` 收到错误后调用 `choices.fail(err)` 取消 `choices.ctx`，从而立即关闭上游请求并释放账户租约，避免浪费上游配额。

## 4. 变更文件清单

1. `internal/api/generate.go`：
   - 新增 `streamStartTimeout = 10 * time.Second`，供 `awaitStreamStart` 使用。
   - 将 `streamHeartbeatInterval` 调整为 `30 * time.Second`。
   - 在 `consumeEventsWithHeartbeat` 中过滤非增量元事件，避免误重置心跳定时器。
2. `internal/api/errors.go`：
   - 移除 `writeGeminiHeartbeat` 函数，统一保留 `writeSSEHeartbeat`（发送 `: ping\n\n` 并 `Flush()`）。
3. `internal/api/gemini.go`：
   - `geminiStream` 新增 `lastWrite time.Time` 字段，在 `openGeminiStream` 与 `write` 成功时更新。
   - `geminiStream.heartbeat()` 在 `stream.sse` 下检查 `lastWrite` 节流并调用 `writeSSEHeartbeat(stream.w)`。
4. `internal/api/openai.go`：
   - `streamChatCompletion` 在多候选并发（`n > 1`）时通过 `lastWrite` 节流 `heartbeat()`，防止多个 choice 协程同时写出重复的 `: ping\n\n`。
5. `internal/api/gemini_output_test.go`：
   - 新增针对 `geminiStream` 在 `alt=sse` 与默认 JSON 数组模式下的心跳输出测试，以及 `consumeStreamEvents` 静默期触发 `: ping\n\n` 的单元测试。
