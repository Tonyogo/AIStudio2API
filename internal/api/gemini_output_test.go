package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

func TestGeminiOutputParts_MergeTextAndReasoning(t *testing.T) {
	result := generationResult{
		events: []aistudio.Event{
			{Kind: aistudio.EventReasoning, Text: "I think "},
			{Kind: aistudio.EventReasoning, Text: "therefore "},
			{Kind: aistudio.EventReasoning, Text: "I am."},
			{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig_abc123"},
			{Kind: aistudio.EventText, Text: "Hello "},
			{Kind: aistudio.EventText, Text: "world!"},
		},
	}

	parts := geminiOutputParts(result)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %+v", len(parts), parts)
	}

	// 合并相邻思考并挂载签名
	expectedThought := map[string]any{
		"thought":          true,
		"text":             "I think therefore I am.",
		"thoughtSignature": "sig_abc123",
	}
	if !reflect.DeepEqual(parts[0], expectedThought) {
		t.Errorf("parts[0] = %+v, want %+v", parts[0], expectedThought)
	}

	// 合并相邻正文
	expectedText := map[string]any{
		"text": "Hello world!",
	}
	if !reflect.DeepEqual(parts[1], expectedText) {
		t.Errorf("parts[1] = %+v, want %+v", parts[1], expectedText)
	}
}

func TestGeminiOutputParts_TextOnly(t *testing.T) {
	result := generationResult{
		events: []aistudio.Event{
			{Kind: aistudio.EventText, Text: "Chunk 1 "},
			{Kind: aistudio.EventText, Text: "Chunk 2 "},
			{Kind: aistudio.EventText, Text: "Chunk 3"},
		},
	}

	parts := geminiOutputParts(result)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d: %+v", len(parts), parts)
	}

	expectedText := map[string]any{
		"text": "Chunk 1 Chunk 2 Chunk 3",
	}
	if !reflect.DeepEqual(parts[0], expectedText) {
		t.Errorf("parts[0] = %+v, want %+v", parts[0], expectedText)
	}
}

func TestGeminiOutputParts_LeadingThoughtSignature(t *testing.T) {
	result := generationResult{
		events: []aistudio.Event{
			{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig_pre"},
			{Kind: aistudio.EventReasoning, Text: "Thinking step."},
			{Kind: aistudio.EventText, Text: "Final text."},
		},
	}

	parts := geminiOutputParts(result)
	want := []map[string]any{
		{"text": "", "thoughtSignature": "sig_pre"},
		{"text": "Thinking step.", "thought": true},
		{"text": "Final text."},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts=%+v want=%+v", parts, want)
	}
}

func TestGeminiOutputParts_StandaloneThoughtSignatureFallback(t *testing.T) {
	result := generationResult{
		events: []aistudio.Event{
			{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig_only"},
		},
	}

	parts := geminiOutputParts(result)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d: %+v", len(parts), parts)
	}
	if parts[0]["thoughtSignature"] != "sig_only" {
		t.Errorf("expected thoughtSignature 'sig_only', got %v", parts[0]["thoughtSignature"])
	}
	if _, ok := parts[0]["thought"]; ok || parts[0]["text"] != "" {
		t.Errorf("expected empty text part without thought, got %+v", parts[0])
	}
}

func TestGeminiOutputParts_WithToolCall(t *testing.T) {
	call := aistudio.FunctionCall{
		ID:        "call_1",
		Name:      "get_weather",
		Arguments: json.RawMessage(`{"location":"Tokyo"}`),
	}
	result := generationResult{
		events: []aistudio.Event{
			{Kind: aistudio.EventReasoning, Text: "Need weather for Tokyo."},
			{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig_call"},
			{Kind: aistudio.EventToolCall, ToolCall: &call},
		},
	}

	parts := geminiOutputParts(result)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %+v", len(parts), parts)
	}

	if parts[0]["thought"] != true || parts[0]["text"] != "Need weather for Tokyo." {
		t.Errorf("unexpected parts[0]: %+v", parts[0])
	}
	if parts[0]["thoughtSignature"] != "sig_call" {
		t.Errorf("expected sig_call on parts[0], got %v", parts[0]["thoughtSignature"])
	}
	if parts[1]["functionCall"] == nil {
		t.Errorf("expected functionCall on parts[1], got %+v", parts[1])
	}
}

// TestGeminiOutputParts_SignedBoundaries 保留相邻内容的签名边界
func TestGeminiOutputParts_SignedBoundaries(t *testing.T) {
	for _, kind := range []aistudio.EventKind{aistudio.EventText, aistudio.EventReasoning} {
		parts := geminiOutputParts(generationResult{events: []aistudio.Event{
			{Kind: kind, Text: "first", ThoughtSignature: "sig-a"},
			{Kind: kind, Text: "second", ThoughtSignature: "sig-b"},
		}})
		if len(parts) != 2 || parts[0]["text"] != "first" || parts[1]["text"] != "second" || parts[0]["thoughtSignature"] != "sig-a" || parts[1]["thoughtSignature"] != "sig-b" {
			t.Fatalf("kind=%s signed parts=%+v", kind, parts)
		}
	}
}

// TestGeminiOutputParts_PendingSignature 保留前置签名与已有签名
func TestGeminiOutputParts_PendingSignature(t *testing.T) {
	parts := geminiOutputParts(generationResult{events: []aistudio.Event{
		{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig-a"},
		{Kind: aistudio.EventReasoning, Text: "reason", ThoughtSignature: "sig-b"},
		{Kind: aistudio.EventText, Text: "answer"},
	}})
	want := []map[string]any{
		{"text": "", "thoughtSignature": "sig-a"},
		{"text": "reason", "thought": true, "thoughtSignature": "sig-b"},
		{"text": "answer"},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts=%+v want=%+v", parts, want)
	}
}

// TestGeminiOutputParts_TrailingSignatures 保留尾部签名与已有签名的顺序
func TestGeminiOutputParts_TrailingSignatures(t *testing.T) {
	parts := geminiOutputParts(generationResult{events: []aistudio.Event{
		{Kind: aistudio.EventText, Text: "answer", ThoughtSignature: "sig-a"},
		{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig-b"},
	}})
	want := []map[string]any{
		{"text": "answer", "thoughtSignature": "sig-a"},
		{"text": "", "thoughtSignature": "sig-b"},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts=%+v want=%+v", parts, want)
	}
}

// TestGeminiOutputParts_LeadingSignatures 保留连续前置签名的原始顺序
func TestGeminiOutputParts_LeadingSignatures(t *testing.T) {
	parts := geminiOutputParts(generationResult{events: []aistudio.Event{
		{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig-a"},
		{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig-b"},
		{Kind: aistudio.EventThoughtSignature, ThoughtSignature: "sig-c"},
		{Kind: aistudio.EventText, Text: "answer"},
	}})
	want := []map[string]any{
		{"text": "", "thoughtSignature": "sig-a"},
		{"text": "", "thoughtSignature": "sig-b"},
		{"text": "", "thoughtSignature": "sig-c"},
		{"text": "answer"},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts=%+v want=%+v", parts, want)
	}
}

// TestGeminiOutputParts_SignedAudio 保留 PCM 音频片段的独立签名
func TestGeminiOutputParts_SignedAudio(t *testing.T) {
	var result generationResult
	for _, sig := range []string{"audio-a", "audio-b"} {
		if err := result.apply(aistudio.Event{Kind: aistudio.EventMedia, ThoughtSignature: sig, Media: &aistudio.Media{MIME: "audio/l16;rate=24000", Data: []byte{1, 2}}}); err != nil {
			t.Fatal(err)
		}
	}
	parts := geminiOutputParts(result)
	if len(parts) != 2 || parts[0]["thoughtSignature"] != "audio-a" || parts[1]["thoughtSignature"] != "audio-b" {
		t.Fatalf("signed audio=%+v", parts)
	}
}

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

		// 模拟静默已达 streamHeartbeatInterval
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
