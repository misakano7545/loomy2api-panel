// tools_test.go 工具面观测（tools_declared / tool_calls）。
//
// 这两个数一起才判得出「声明了工具却一次没调用」的停摆回合：单看任一数都分不清
// 是模型不需要工具，还是上游把工具丢了。
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/misakano7545/loomy2api-panel/internal/usage"
)

// TestToolCallsInShapes 两种组包形态都要数得出来：只认一种的路径会恒记 0。
func TestToolCallsInShapes(t *testing.T) {
	asAny := map[string]any{"choices": []any{map[string]any{
		"message": map[string]any{"tool_calls": []any{map[string]any{"id": "a"}}},
	}}}
	if n := toolCallsIn(asAny); n != 1 {
		t.Fatalf("[]any 形态数出 %d，期望 1", n)
	}
	asMaps := map[string]any{"choices": []any{map[string]any{
		"message": map[string]any{"tool_calls": []map[string]any{{"id": "a"}, {"id": "b"}}},
	}}}
	if n := toolCallsIn(asMaps); n != 2 {
		t.Fatalf("[]map 形态数出 %d，期望 2", n)
	}
}

const toolsBody = `"tools":[{"type":"function","function":{"name":"a","parameters":{}}},` +
	`{"type":"function","function":{"name":"b","parameters":{}}}]`

// TestToolsSurfaceNonStream 非流式：声明 2 个、实际调用 1 个。
func TestToolsSurfaceNonStream(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200,
		`{"choices":[{"message":{"content":"","tool_calls":[{"id":"c1","type":"function",`+
			`"function":{"name":"a","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{}}`))
	h, _, ledger := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"messages":[{"role":"user","content":"hi"}],`+toolsBody+`}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	recs := ledger.Recent(10, usage.RecordFilter{})
	if len(recs) != 1 {
		t.Fatalf("台账记录数 %d，期望 1", len(recs))
	}
	if recs[0].ToolsDeclared != 2 || recs[0].ToolCalls != 1 {
		t.Fatalf("声明/调用 = %d/%d，期望 2/1", recs[0].ToolsDeclared, recs[0].ToolCalls)
	}
}

// TestToolsSurfaceStream 流式：收到 tool_calls 帧就记 1（不数分片数）。
func TestToolsSurfaceStream(t *testing.T) {
	fs := newFakeUpstream(t, sseHandler(
		`{"choices":[{"delta":{"content":"想一下"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"a","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	))
	h, _, ledger := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"stream":true,"messages":[{"role":"user","content":"hi"}],`+toolsBody+`}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	recs := ledger.Recent(10, usage.RecordFilter{})
	if len(recs) != 1 || !recs[0].Stream {
		t.Fatalf("流式记录不对: %+v", recs)
	}
	if recs[0].ToolsDeclared != 2 || recs[0].ToolCalls != 1 {
		t.Fatalf("声明/调用 = %d/%d，期望 2/1", recs[0].ToolsDeclared, recs[0].ToolCalls)
	}
}

// TestToolsSurfaceFailKeepsDeclared 请求最终失败也要留下声明数（否则失败回合不进分母）。
func TestToolsSurfaceFailKeepsDeclared(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(500, `{"error":{"message":"boom"}}`))
	h, _, ledger := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"messages":[{"role":"user","content":"hi"}],`+toolsBody+`}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	recs := ledger.Recent(10, usage.RecordFilter{})
	if len(recs) != 1 || recs[0].Status == 200 {
		t.Fatalf("失败记录不对: %+v", recs)
	}
	if recs[0].ToolsDeclared != 2 {
		t.Fatalf("失败记录的声明数 = %d，期望 2", recs[0].ToolsDeclared)
	}
}

// TestToolsSurfaceAggregated 非流式长任务内部走流式聚合：从聚合结果里数调用次数。
func TestToolsSurfaceAggregated(t *testing.T) {
	fs := newFakeUpstream(t, sseHandler(
		`{"choices":[{"delta":{"content":"想一下"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"a","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`,
	))
	h, _, ledger := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"max_tokens":99999,"messages":[{"role":"user","content":"hi"}],`+toolsBody+`}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	recs := ledger.Recent(10, usage.RecordFilter{})
	if len(recs) != 1 || recs[0].Stream {
		t.Fatalf("聚合记录不对: %+v", recs)
	}
	if recs[0].ToolsDeclared != 2 || recs[0].ToolCalls != 1 {
		t.Fatalf("声明/调用 = %d/%d，期望 2/1", recs[0].ToolsDeclared, recs[0].ToolCalls)
	}
}

// TestToolsSurfacePlainChatStaysZero 没声明 tools 的纯聊天保持零值，不进分母。
func TestToolsSurfacePlainChatStaysZero(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200,
		`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	h, _, ledger := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	recs := ledger.Recent(10, usage.RecordFilter{})
	if len(recs) != 1 {
		t.Fatalf("台账记录数 %d，期望 1", len(recs))
	}
	if recs[0].ToolsDeclared != 0 || recs[0].ToolCalls != 0 {
		t.Fatalf("纯聊天不该有工具面数据: %+v", recs[0])
	}
}
