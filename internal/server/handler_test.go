package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misakano7545/loomy2api-panel/internal/config"
	"github.com/misakano7545/loomy2api-panel/internal/pool"
	"github.com/misakano7545/loomy2api-panel/internal/upstream"
	"github.com/misakano7545/loomy2api-panel/internal/usage"
)

// fakeUpstream 一个可编程的上游：按顺序返回预设响应。
type fakeUpstream struct {
	srv      *httptest.Server
	handlers []http.HandlerFunc
	seen     []*http.Request
	bodies   []string
}

func newFakeUpstream(t *testing.T, handlers ...http.HandlerFunc) *fakeUpstream {
	fs := &fakeUpstream{handlers: handlers}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fs.seen = append(fs.seen, r)
		fs.bodies = append(fs.bodies, string(body))
		// 回填 body：处理函数可能还要自己读一次（否则拿到空串）。
		r.Body = io.NopCloser(bytes.NewReader(body))
		idx := len(fs.seen) - 1
		if idx < len(fs.handlers) {
			fs.handlers[idx](w, r)
			return
		}
		// 超出预设：返回最后一个
		fs.handlers[len(fs.handlers)-1](w, r)
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

func jsonHandler(status int, payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}
}

func sseHandler(chunks ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

// newTestHandler 构造一套带假上游的 handler。
func newTestHandler(t *testing.T, upURL string, apiKeys []string, handlers ...http.HandlerFunc) (*Handler, *pool.Pool, *usage.Ledger) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Upstream = upURL
	cfg.APIKeys = apiKeys
	cfg.MaxRetries = 1
	cfg.LogRequests = false

	p := pool.New("")
	p.SetOptions(pool.Options{
		Strategy: "weighted", PickTopN: 5,
		SoftRateBase: 600, SoftRateMax: 7200, NotFoundCool: 60, CooldownFallback: 300,
		BreakerThreshold: 5, BreakerBase: 60, BreakerMax: 1800,
		StickyTTL: 1800, StickyMaxEntries: 100,
	})
	expire := time.Now().Add(7 * 24 * time.Hour).Unix()
	bal := int64(1000)
	p.Add(&pool.Account{
		Name: "acc", Enabled: true, Session: "sess-1", ExpireAt: expire,
		Available: &bal, LoginID: "13800000000",
	})

	up := upstream.NewClient(upURL, "0.9.38", "", 30*time.Second)
	ledger := usage.New("", 1000)
	h := New(cfg, p, up, ledger)
	return h, p, ledger
}

// TestAuthRejectsMissingKey 配了 api_key 时缺头必须 401。
func TestAuthRejectsMissingKey(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200, `{"ok":true}`))
	h, _, _ := newTestHandler(t, fs.srv.URL, []string{"sk-test"})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 %d，期望 401", w.Code)
	}
	if len(fs.seen) != 0 {
		t.Error("鉴权失败不应打到上游")
	}
}

// TestAuthAcceptsBearerAndXAPIKey 两种鉴权头都要认。
func TestAuthAcceptsBearerAndXAPIKey(t *testing.T) {
	fs := newFakeUpstream(t,
		jsonHandler(200, `{"id":"x","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	h, _, _ := newTestHandler(t, fs.srv.URL, []string{"sk-test"})
	mux := h.Routes()

	for _, hdr := range []struct{ k, v string }{
		{"Authorization", "Bearer sk-test"},
		{"x-api-key", "sk-test"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
		req.Header.Set(hdr.k, hdr.v)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s 鉴权后状态码 %d，期望 200（body: %s）", hdr.k, w.Code, w.Body.String())
		}
	}
}

// TestChatCompletionsNonStream 非流式短请求直通。
func TestChatCompletionsNonStream(t *testing.T) {
	payload := `{"id":"chatcmpl-1","model":"imodel/spark-x","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"points_consumed":2}}`
	fs := newFakeUpstream(t, jsonHandler(200, payload))
	h, p, ledger := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"imodel/spark-x","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200（body: %s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "你好") {
		t.Errorf("响应体未透传: %s", w.Body.String())
	}
	if got := w.Header().Get("X-Loomy-Account"); got != "acc" {
		t.Errorf("X-Loomy-Account = %q", got)
	}
	// 账号计数与积分应被记账
	acc := p.Get("acc")
	if acc.Requests != 1 {
		t.Errorf("requests = %d，期望 1", acc.Requests)
	}
	if acc.PointsUsed != 2 {
		t.Errorf("pointsUsed = %d，期望 2", acc.PointsUsed)
	}
	// 台账应有一条成功记录
	recs := ledger.Recent(10, usage.RecordFilter{})
	if len(recs) != 1 || recs[0].Status != 200 {
		t.Errorf("台账记录不对: %+v", recs)
	}
	if recs[0].Prompt != 5 || recs[0].Completion != 3 {
		t.Errorf("token 记录不对: %+v", recs[0])
	}
}

// TestUpstreamHeadersRequired 三个必需头必须出现在上游请求里。
func TestUpstreamHeadersRequired(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if len(fs.seen) == 0 {
		t.Fatal("上游没被调用")
	}
	got := fs.seen[0]
	if auth := got.Header.Get("Authorization"); auth != "Bearer sess-1" {
		t.Errorf("Authorization = %q", auth)
	}
	if tok := got.Header.Get("token"); tok != "sess-1" {
		t.Errorf("token 头 = %q（客户端 useSessionAuth 双写）", tok)
	}
	tp := got.Header.Get("traceparent")
	if !strings.HasPrefix(tp, "00-") {
		t.Errorf("traceparent 缺失或格式不对: %q（缺它上游会挂死到超时）", tp)
	}
	if v := got.Header.Get("loomy-version"); v == "" {
		t.Error("loomy-version 头缺失")
	}
}

// TestEmptyMessagesFailsFast 空 messages 应 400 且不打上游、不冷却账号。
func TestEmptyMessagesFailsFast(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200, `{"choices":[]}`))
	h, p, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[]}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("状态码 %d，期望 400", w.Code)
	}
	if len(fs.seen) != 0 {
		t.Error("空 messages 不应打到上游")
	}
	if p.Get("acc").InCooldown() {
		t.Error("客户端错误不应冷却账号")
	}
}

// TestRetryOnQuotaThenSucceed 首号额度耗尽 → 换号成功。
func TestRetryOnQuotaThenSucceed(t *testing.T) {
	fs := newFakeUpstream(t,
		jsonHandler(402, `{"error":"积分不足"}`),
		jsonHandler(200, `{"id":"x","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`),
	)
	cfg := config.Defaults()
	cfg.Upstream = fs.srv.URL
	cfg.MaxRetries = 2
	cfg.LogRequests = false

	p := pool.New("")
	p.SetOptions(pool.Options{Strategy: "weighted", PickTopN: 5,
		SoftRateBase: 600, SoftRateMax: 7200, NotFoundCool: 60, CooldownFallback: 300,
		BreakerThreshold: 5, BreakerBase: 60, BreakerMax: 1800,
		StickyTTL: 1800, StickyMaxEntries: 100})
	expire := time.Now().Add(7 * 24 * time.Hour).Unix()
	bal := int64(1000)
	p.Add(&pool.Account{Name: "a", Enabled: true, Session: "s1", ExpireAt: expire, Available: &bal})
	p.Add(&pool.Account{Name: "b", Enabled: true, Session: "s2", ExpireAt: expire, Available: &bal})

	up := upstream.NewClient(fs.srv.URL, "0.9.38", "", 30*time.Second)
	h := New(cfg, p, up, usage.New("", 100))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("换号后应成功，实际 %d（body: %s）", w.Code, w.Body.String())
	}
	if len(fs.seen) != 2 {
		t.Errorf("上游调用 %d 次，期望 2（换号重试一次）", len(fs.seen))
	}
	// 首个失败账号应进入硬冷却
	if !p.Get("a").InCooldown() && !p.Get("b").InCooldown() {
		t.Error("额度耗尽应让某个账号进入冷却")
	}
}

// TestStreamPassthrough 流式响应逐帧透传且恰好一个 [DONE]。
func TestStreamPassthrough(t *testing.T) {
	chunk1 := `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}`
	chunk2 := `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"points_consumed":1}}`
	fs := newFakeUpstream(t, sseHandler(chunk1, chunk2, "[DONE]"))
	h, p, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"content":"你"`) || !strings.Contains(body, `"content":"好"`) {
		t.Errorf("流式内容未透传: %s", body)
	}
	if got := strings.Count(body, "[DONE]"); got != 1 {
		t.Errorf("[DONE] 出现 %d 次，期望恰好 1 次", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	if p.Get("acc").PointsUsed != 1 {
		t.Errorf("流式积分未记账: %d", p.Get("acc").PointsUsed)
	}
}

// TestStreamMissingDoneGetsSyntheticDone 上游漏发 [DONE] 时要兜底补一个。
func TestStreamMissingDoneGetsSyntheticDone(t *testing.T) {
	chunk := `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`
	fs := newFakeUpstream(t, sseHandler(chunk))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if got := strings.Count(w.Body.String(), "[DONE]"); got != 1 {
		t.Errorf("[DONE] 出现 %d 次，期望 1（应兜底补齐）", got)
	}
}

// TestAnthropicMessagesNonStream /v1/messages 非流式翻译。
func TestAnthropicMessagesNonStream(t *testing.T) {
	payload := `{"id":"c1","model":"imodel/spark-x","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`
	chunk := `{"id":"c1","model":"imodel/spark-x","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"points_consumed":1}}`
	fs := newFakeUpstream(t, sseHandler(chunk), jsonHandler(200, payload))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"imodel/spark-x","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d（body: %s）", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v（%s）", err, w.Body.String())
	}
	if out["type"] != "message" {
		t.Errorf("type = %v，期望 message", out["type"])
	}
	content, _ := out["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("content 为空: %v", out)
	}
	first := content[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "你好" {
		t.Errorf("文本块不对: %v", first)
	}
	if out["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", out["stop_reason"])
	}
}

// TestAnthropicStreamEvents /v1/messages 流式应发 Anthropic 事件名。
func TestAnthropicStreamEvents(t *testing.T) {
	chunk1 := `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}`
	chunk2 := `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}],"usage":{"completion_tokens":2}}`
	fs := newFakeUpstream(t, sseHandler(chunk1, chunk2, "[DONE]"))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	body := w.Body.String()
	for _, want := range []string{
		"event: message_start", "event: content_block_start",
		"event: content_block_delta", "event: content_block_stop",
		"event: message_delta", "event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("缺少事件 %q\n实际: %s", want, body)
		}
	}
	if !strings.Contains(body, "text_delta") {
		t.Errorf("缺少 text_delta: %s", body)
	}
}

// TestResponsesEndpoint /v1/responses 非流式翻译成 response 对象。
func TestResponsesEndpoint(t *testing.T) {
	chunk := `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"答"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	fs := newFakeUpstream(t, sseHandler(chunk, "[DONE]"))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"你好"}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d（body: %s）", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if out["object"] != "response" {
		t.Errorf("object = %v，期望 response", out["object"])
	}
	output, _ := out["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("output 为空: %v", out)
	}
	if output[0].(map[string]any)["type"] != "message" {
		t.Errorf("首个 output 类型 = %v", output[0].(map[string]any)["type"])
	}
}

// TestHealthz 无账号时 503、有账号时 200 且带 service 标识。
func TestHealthz(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200, `{}`))
	h, p, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("有健康账号时应 200，实际 %d", w.Code)
	}
	if w.Header().Get("X-Service") != "loomy2api" {
		t.Error("缺少 X-Service 身份头")
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["service"] != "loomy2api" {
		t.Errorf("service 字段 = %v", body["service"])
	}

	// 禁用唯一账号 → 503
	p.Get("acc").Enabled = false
	w2 := httptest.NewRecorder()
	h.Routes().ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("无可用账号时应 503，实际 %d", w2.Code)
	}
}

// TestModelsEndpoint 模型目录接口（上游失败回落兜底表）。
func TestModelsEndpoint(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(500, `boom`))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	data, _ := out["data"].([]any)
	if len(data) == 0 {
		t.Error("上游失败时应回落兜底模型表")
	}
	first := data[0].(map[string]any)
	if first["object"] != "model" {
		t.Errorf("object = %v", first["object"])
	}
}

// TestModelsStripsPrefix 无前缀模型名应补 imodel/。
func TestModelsStripsPrefix(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"spark-x","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if len(fs.bodies) == 0 {
		t.Fatal("上游没被调用")
	}
	if !strings.Contains(fs.bodies[0], `"model":"imodel/spark-x"`) {
		t.Errorf("模型前缀未补全: %s", fs.bodies[0])
	}
}

// TestAnthropicNonStreamForcesUpstreamStream 回归：上游严格按 stream 标志返回时，
// 网关内部必须强制 stream=true，否则 SSE 解析读到 0 帧、客户端拿到空回答。
func TestAnthropicNonStreamForcesUpstreamStream(t *testing.T) {
	var sawStream bool
	strict := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if s, _ := payload["stream"].(bool); s {
			sawStream = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"内容"},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		// stream=false：返回普通 JSON，SSE 解析器读不到任何帧
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":"内容"},"finish_reason":"stop"}],"usage":{}}`)
	}
	fs := newFakeUpstream(t, strict)
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	// 客户端显式不要流式
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"m","max_tokens":100,"stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if !sawStream {
		t.Fatal("网关内部未强制 stream=true（上游会返回普通 JSON，导致空回答）")
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	content, _ := out["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("内容为空（回归）：%s", w.Body.String())
	}
	first := content[0].(map[string]any)
	if first["text"] != "内容" {
		t.Errorf("文本 = %v，期望 内容", first["text"])
	}
}

// TestSSEStreamParsing SSE 解析器：跨包边界与 [DONE]。
func TestSSEStreamParsing(t *testing.T) {
	raw := "data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"
	sc := newSSEStream(strings.NewReader(raw))

	obj1, ok, err := sc.Next()
	if err != nil || !ok {
		t.Fatalf("第一帧解析失败: %v", err)
	}
	if obj1["a"] != float64(1) {
		t.Errorf("第一帧内容 = %v", obj1)
	}
	obj2, ok, err := sc.Next()
	if err != nil || !ok {
		t.Fatalf("第二帧解析失败: %v", err)
	}
	if obj2["b"] != float64(2) {
		t.Errorf("第二帧内容 = %v", obj2)
	}
	_, ok, err = sc.Next()
	if err != nil {
		t.Fatalf("[DONE] 处理失败: %v", err)
	}
	if ok {
		t.Error("[DONE] 应结束流")
	}
	if !sc.sawDone {
		t.Error("应记录 sawDone")
	}
}

// TestAggregatorBuildsFullResponse SSE 增量聚合成完整非流式响应。
func TestAggregatorBuildsFullResponse(t *testing.T) {
	agg := newAggregator()
	agg.Feed(map[string]any{
		"id": "c1", "model": "m",
		"choices": []any{map[string]any{"delta": map[string]any{
			"content": "你", "reasoning_content": "想",
		}}},
	})
	agg.Feed(map[string]any{
		"choices": []any{map[string]any{
			"delta":         map[string]any{"content": "好"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 2},
	})
	out := agg.Result("fallback-model")

	if out["object"] != "chat.completion" {
		t.Errorf("object = %v", out["object"])
	}
	choices := out["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "你好" {
		t.Errorf("content = %v", msg["content"])
	}
	if msg["reasoning_content"] != "想" {
		t.Errorf("reasoning_content = %v", msg["reasoning_content"])
	}
	if choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Errorf("finish_reason 未保留")
	}
	if out["model"] != "m" {
		t.Errorf("model = %v（应以上游回包为准）", out["model"])
	}
}

// TestAggregatorToolCalls 工具调用增量聚合完整。
func TestAggregatorToolCalls(t *testing.T) {
	agg := newAggregator()
	agg.Feed(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0, "id": "call_1",
				"function": map[string]any{"name": "f", "arguments": `{"a":`},
			}},
		}}},
	})
	agg.Feed(map[string]any{
		"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "function": map[string]any{"arguments": `1}`},
			}}},
			"finish_reason": "tool_calls",
		}},
	})
	out := agg.Result("m")
	msg := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	calls, ok := msg["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls 未聚合: %v", msg)
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["arguments"] != `{"a":1}` {
		t.Errorf("参数拼接 = %v", fn["arguments"])
	}
}

// TestCORSHeaders OPTIONS 预检与 CORS 头。
func TestCORSHeaders(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200, `{}`))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS 应 204，实际 %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("缺少 CORS 头")
	}
}

// TestUnknownPath404 未定义路径返回结构化 404。
func TestUnknownPath404(t *testing.T) {
	fs := newFakeUpstream(t, jsonHandler(200, `{}`))
	h, _, _ := newTestHandler(t, fs.srv.URL, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/nope", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("状态码 %d，期望 404", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if _, ok := out["error"]; !ok {
		t.Errorf("应返回结构化错误体: %s", w.Body.String())
	}
}

var _ = context.Background
