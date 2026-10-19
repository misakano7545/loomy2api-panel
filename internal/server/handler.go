// Package server HTTP 网关：入站协议解析、账号轮转、SSE 转发与本地聚合。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misakano7545/loomy2api-panel/internal/bridge"
	"github.com/misakano7545/loomy2api-panel/internal/config"
	"github.com/misakano7545/loomy2api-panel/internal/httpauth"
	"github.com/misakano7545/loomy2api-panel/internal/pool"
	"github.com/misakano7545/loomy2api-panel/internal/upstream"
	"github.com/misakano7545/loomy2api-panel/internal/usage"
)

// Handler 网关主 handler。
type Handler struct {
	Cfg      *config.Config
	Pool     *pool.Pool
	Upstream *upstream.Client
	Ledger   *usage.Ledger
	Models   *ModelCache

	reqSeq  atomic.Int64
	started time.Time
}

// New 构造 handler。
func New(cfg *config.Config, p *pool.Pool, up *upstream.Client, ledger *usage.Ledger) *Handler {
	return &Handler{
		Cfg: cfg, Pool: p, Upstream: up, Ledger: ledger,
		Models:  NewModelCache(up),
		started: time.Now(),
	}
}

// Routes 返回路由表。
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/status", h.withAuth(h.status))
	mux.HandleFunc("/metrics", h.withAuth(h.metrics))

	mux.HandleFunc("/v1/chat/completions", h.withAuth(h.chatCompletions))
	mux.HandleFunc("/v1/responses", h.withAuth(h.responses))
	mux.HandleFunc("/chat/completions", h.withAuth(h.chatCompletions))
	mux.HandleFunc("/v1/messages", h.withAuth(h.anthropicMessages))
	mux.HandleFunc("/messages", h.withAuth(h.anthropicMessages))
	mux.HandleFunc("/v1/embeddings", h.withAuth(h.passthrough("embeddings", "embed")))
	mux.HandleFunc("/v1/images/generations", h.withAuth(h.passthrough("images/generations", "image")))
	mux.HandleFunc("/v1/search", h.withAuth(h.search))
	mux.HandleFunc("/v1/models", h.withAuth(h.listModels))
	mux.HandleFunc("/v1/points", h.withAuth(h.points))
	mux.HandleFunc("/v1/", h.withAuth(h.notFound))

	return mux
}

// withAuth 统一鉴权包装。
func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cors(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !httpauth.Verify(r, h.Cfg.APIKeys) {
			writeErr(w, http.StatusUnauthorized, "invalid api key", "authentication_error")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
		next(w, r)
	}
}

// cors 允许浏览器端直接调用（同源之外的自托管前端）。
func cors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotFound, "未知路径 "+r.URL.Path, "invalid_request_error")
}

// ------------------------------------------------------------- health / 状态

// healthz 健康检查：有可用账号返回 200，否则 503。
//
// 响应带 service 字段与 X-Service 头作身份标识：同端口上可能残留别的服务，
// 对方即使返回 2xx 也不会带该字段，宿主探活据此避免「假成功」。
func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	ov := h.Pool.Overview()
	healthy, _ := ov["healthy"].(int)
	total, _ := ov["total"].(int)
	w.Header().Set("X-Service", "loomy2api")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	code := http.StatusOK
	if healthy == 0 {
		code = http.StatusServiceUnavailable
	}
	w.WriteHeader(code)
	writeJSON(w, map[string]any{
		"healthy": healthy,
		"total":   total,
		"service": "loomy2api",
		"uptime":  int64(time.Since(h.started).Seconds()),
	})
}

// status 账号状态汇总 + 每账号详情。
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ov := h.Pool.Overview()
	accounts := make([]map[string]any, 0, h.Pool.Count())
	for _, a := range h.Pool.All() {
		accounts = append(accounts, a.Public())
	}
	ov["accounts"] = accounts
	ov["version"] = Version
	writeJSON(w, ov)
}

// metrics Prometheus 文本。
func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	ov := h.Pool.Overview()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP loomy2api_accounts_total 账号总数\n")
	fmt.Fprintf(&b, "# TYPE loomy2api_accounts_total gauge\n")
	fmt.Fprintf(&b, "loomy2api_accounts_total %v\n", ov["total"])
	fmt.Fprintf(&b, "loomy2api_accounts_healthy %v\n", ov["healthy"])
	fmt.Fprintf(&b, "loomy2api_accounts_cooling %v\n", ov["cooling"])
	fmt.Fprintf(&b, "loomy2api_requests_total %v\n", ov["requests"])
	fmt.Fprintf(&b, "loomy2api_points_used_total %v\n", ov["points_used"])

	if w24 := h.Ledger.Summary(map[string]int64{"24h": 86400})["24h"]; w24 != nil {
		fmt.Fprintf(&b, "loomy2api_requests_24h %d\n", w24.Total)
		fmt.Fprintf(&b, "loomy2api_prompt_tokens_24h %d\n", w24.Prompt)
		fmt.Fprintf(&b, "loomy2api_completion_tokens_24h %d\n", w24.Completion)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(&b, "loomy2api_mem_alloc_bytes %d\n", ms.Alloc)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

// ---------------------------------------------------------------- 模型目录

// ModelCache 模型目录缓存（1h，失败 5min 负缓存）。
type ModelCache struct {
	up      *upstream.Client
	mu      sync.Mutex
	models  []config.Model
	fetched time.Time
	failed  time.Time
}

// NewModelCache 构造缓存。
func NewModelCache(up *upstream.Client) *ModelCache {
	return &ModelCache{up: up}
}

// Get 取模型目录（优先缓存，其次上游，最后兜底表）。
func (m *ModelCache) Get(ctx context.Context, session string) []config.Model {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if len(m.models) > 0 && now.Sub(m.fetched) < time.Hour {
		return m.models
	}
	if !m.failed.IsZero() && now.Sub(m.failed) < 5*time.Minute {
		return fallbackOr(m.models)
	}
	if session == "" {
		return fallbackOr(m.models)
	}
	list, err := m.up.Models(ctx, session)
	if err != nil || len(list) == 0 {
		m.failed = now
		log.Printf("[models] 上游目录拉取失败：%v（回落到缓存/兜底表）", err)
		return fallbackOr(m.models)
	}
	m.models = list
	m.fetched = now
	m.failed = time.Time{}
	return list
}

// Invalidate 强制下次重新拉取。
func (m *ModelCache) Invalidate() {
	m.mu.Lock()
	m.fetched = time.Time{}
	m.mu.Unlock()
}

func fallbackOr(cached []config.Model) []config.Model {
	if len(cached) > 0 {
		return cached
	}
	return config.FallbackModels()
}

// anySession 取任一健康账号的登录态（用于只读查询）。
func (h *Handler) anySession() string {
	for _, a := range h.Pool.All() {
		if a.Enabled && a.SessionValid() {
			return a.Session
		}
	}
	return ""
}

// listModels GET /v1/models。
func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	models := h.Models.Get(r.Context(), h.anySession())
	data := make([]map[string]any, 0, len(models))
	now := time.Now().Unix()
	for _, m := range models {
		item := map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  now,
			"owned_by": "iflytek",
		}
		if m.ContextLength > 0 {
			item["context_length"] = m.ContextLength
		}
		if m.MaxOutput > 0 {
			item["max_output_tokens"] = m.MaxOutput
		}
		if m.Multiplier > 0 {
			item["multiplier"] = m.Multiplier
		}
		if m.Name != "" {
			item["name"] = m.Name
		}
		if len(m.Capabilities) > 0 {
			item["capabilities"] = m.Capabilities
		}
		if len(m.ReasoningTiers) > 0 {
			item["reasoning_efforts"] = m.ReasoningTiers
		}
		data = append(data, item)
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

// points GET /v1/points 聚合全池额度。
func (h *Handler) points(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, a := range h.Pool.All() {
		item := map[string]any{
			"name":       a.Name,
			"enabled":    a.Enabled,
			"balance":    a.Balance,
			"daily":      a.DailyBalance,
			"available":  a.AvailablePoints(),
			"updated_at": a.QuotaAt,
		}
		out = append(out, item)
	}
	writeJSON(w, map[string]any{"accounts": out})
}

// ---------------------------------------------------------------- 会话处理

// chatCompletions POST /v1/chat/completions。
func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	req, err := readJSON(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		// 空 messages 直接 400 快速失败：不打上游、不占账号、不冷却。
		writeErr(w, http.StatusBadRequest, "messages 不能为空", "invalid_request_error")
		return
	}

	model := h.resolveModel(req)
	stream := truthy(req["stream"])
	skey := pool.SessionKey(req)
	extra := headerExtras(r, req)

	if !stream {
		// 非流式长任务：上游对久无响应的非流式请求约 60s 就回 504，
		// 而流式同模型能跑几分钟。故内部转流式取数再聚合，客户端无感。
		if minTok, _ := intOf(req["max_tokens"]); minTok >= h.Cfg.KeepaliveMinTokens {
			h.chatAggregated(w, r, req, model, skey, extra)
			return
		}
	}

	if stream {
		h.chatStream(w, r, req, model, skey, extra)
		return
	}
	h.chatOnce(w, r, req, model, skey, extra)
}

// chatOnce 非流式短请求：直接调上游。
func (h *Handler) chatOnce(w http.ResponseWriter, r *http.Request, req map[string]any, model, skey string, extra map[string]string) {
	started := time.Now()
	var tried []string
	var lastStatus int
	var lastBody string

	for attempt := 0; attempt <= h.Cfg.MaxRetries; attempt++ {
		acc, err := h.Pool.Acquire(tried, model, skey)
		if err != nil {
			if len(tried) == 0 {
				writeErr(w, http.StatusServiceUnavailable, err.Error(), "upstream_error")
				return
			}
			break
		}
		tried = append(tried, acc.Name)
		opts := h.Pool.Options()
		resp, err := h.Upstream.Do(r.Context(), http.MethodPost, "chat/completions", acc.Session, req, extra)
		if err != nil {
			h.Pool.Release(acc)
			h.Pool.ReportFailure(acc, 0, err.Error(), skey, model)
			lastStatus, lastBody = http.StatusBadGateway, err.Error()
			h.logCall(acc, model, 0, time.Since(started), "sync 连接异常: "+err.Error())
			continue
		}
		body := resp.BodyText()
		h.Pool.Release(acc)
		if resp.Status == http.StatusOK {
			var obj map[string]any
			usageObj := map[string]any{}
			if err := json.Unmarshal([]byte(body), &obj); err == nil {
				usageObj, _ = obj["usage"].(map[string]any)
			}
			pts, prompt, completion, reasoning := usageParts(usageObj)
			h.Pool.ReportSuccess(acc, skey, model, pts)
			h.record(usage.Record{
				Account: acc.Name, Model: model, Kind: "chat", Status: 200,
				Prompt: prompt, Completion: completion, Reasoning: reasoning,
				Points: pts, LatencyMs: time.Since(started).Milliseconds(),
				ClientIP: clientIP(r), UserAgent: ua(r), RequestID: rid(resp.Header),
			})
			h.logCall(acc, model, 200, time.Since(started), usageSummary(prompt, completion, pts, reasoning))
			w.Header().Set("X-Loomy-Account", acc.Name)
			if id := rid(resp.Header); id != "" {
				w.Header().Set("X-Request-Id", id)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, body)
			_ = opts
			return
		}

		lastStatus, lastBody = resp.Status, body
		fault := pool.Classify(resp.Status, body)
		h.Pool.ReportFailure(acc, resp.Status, body, skey, model)
		h.logCall(acc, model, resp.Status, time.Since(started), "上游返回，换号重试")
		if pool.FatalRequest(fault) {
			break
		}
	}

	if lastStatus == 0 {
		lastStatus = http.StatusBadGateway
	}
	if lastBody == "" {
		lastBody = "所有账号都失败了"
	}
	h.writeUpstreamError(w, lastStatus, lastBody)
}

// chatAggregated 非流式长任务：内部走流式取数再聚合。
func (h *Handler) chatAggregated(w http.ResponseWriter, r *http.Request, req map[string]any, model, skey string, extra map[string]string) {
	started := time.Now()
	sreq := cloneMap(req)
	sreq["stream"] = true

	// 先发 200 + chunked，并周期性写空白保活，绕开反代/CF 的 60~100s 504。
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	stopKeepalive := make(chan struct{})
	done := make(chan struct{})
	if flusher != nil {
		go func() {
			t := time.NewTicker(15 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stopKeepalive:
					return
				case <-done:
					return
				case <-t.C:
					// JSON 合法前导空白
					_, _ = io.WriteString(w, strings.Repeat(" ", 8))
					flusher.Flush()
				}
			}
		}()
	}
	defer func() {
		close(stopKeepalive)
		close(done)
	}()

	obj, err := h.collectStream(r.Context(), sreq, model, skey, extra)
	if err != nil {
		writeJSON(w, map[string]any{"error": map[string]any{
			"message": err.Error(), "type": "upstream_error", "code": 502,
		}})
		return
	}
	h.logCall(nil, model, 200, time.Since(started), "流式聚合（长任务）")
	buf, _ := json.Marshal(obj)
	_, _ = w.Write(buf)
}

// collectStream 用流式调上游并把 SSE 聚合成一条非流式响应。
//
// 内部强制 stream=true：调用方（/v1/messages、/v1/responses、长任务）传进来的
// 请求体可能是 stream:false，而这里按 SSE 解析响应——不覆盖的话上游返回普通 JSON，
// 解析器读到 0 帧，客户端会拿到一个空回答。
func (h *Handler) collectStream(ctx context.Context, req map[string]any, model, skey string, extra map[string]string) (map[string]any, error) {
	upReq := cloneMap(req)
	upReq["stream"] = true

	var tried []string
	var lastErr error

	for attempt := 0; attempt <= h.Cfg.MaxRetries; attempt++ {
		acc, err := h.Pool.Acquire(tried, model, skey)
		if err != nil {
			if len(tried) == 0 {
				return nil, err
			}
			break
		}
		tried = append(tried, acc.Name)
		started := time.Now()

		resp, err := h.Upstream.Do(ctx, http.MethodPost, "chat/completions", acc.Session, upReq, extra)
		if err != nil {
			h.Pool.Release(acc)
			h.Pool.ReportFailure(acc, 0, err.Error(), skey, model)
			lastErr = err
			continue
		}
		if resp.Status != http.StatusOK {
			body := resp.BodyText()
			h.Pool.Release(acc)
			h.Pool.ReportFailure(acc, resp.Status, body, skey, model)
			lastErr = fmt.Errorf("上游 HTTP %d: %s", resp.Status, truncate(body, 200))
			continue
		}

		agg := newAggregator()
		sc := newSSEStream(resp.Body)
		for {
			obj, ok, serr := sc.Next()
			if serr != nil {
				lastErr = serr
				break
			}
			if !ok {
				break
			}
			agg.Feed(obj)
		}
		_ = resp.Body.Close()
		h.Pool.Release(acc)

		out := agg.Result(model)
		pts, prompt, completion, reasoning := usageParts(agg.Usage())
		h.Pool.ReportSuccess(acc, skey, model, pts)
		h.record(usage.Record{
			Account: acc.Name, Model: model, Kind: "chat", Stream: false, Status: 200,
			Prompt: prompt, Completion: completion, Reasoning: reasoning, Points: pts,
			LatencyMs: time.Since(started).Milliseconds(),
		})
		return out, nil
	}
	if lastErr == nil {
		lastErr = errors.New("所有账号都失败了")
	}
	return nil, lastErr
}

// chatStream 流式请求：把上游 SSE 原样转发给客户端。
func (h *Handler) chatStream(w http.ResponseWriter, r *http.Request, req map[string]any, model, skey string, extra map[string]string) {
	started := time.Now()
	var tried []string
	var lastStatus int
	var lastBody string
	var lastResp *upstream.Response
	var lastAcc *pool.Account

	for attempt := 0; attempt <= h.Cfg.MaxRetries; attempt++ {
		acc, err := h.Pool.Acquire(tried, model, skey)
		if err != nil {
			if len(tried) == 0 {
				writeErr(w, http.StatusServiceUnavailable, err.Error(), "upstream_error")
				return
			}
			break
		}
		tried = append(tried, acc.Name)
		resp, err := h.Upstream.Do(r.Context(), http.MethodPost, "chat/completions", acc.Session, req, extra)
		if err != nil {
			h.Pool.Release(acc)
			h.Pool.ReportFailure(acc, 0, err.Error(), skey, model)
			lastStatus, lastBody = http.StatusBadGateway, err.Error()
			continue
		}
		if resp.Status == http.StatusOK {
			lastResp, lastAcc = resp, acc
			break
		}
		body := resp.BodyText()
		h.Pool.Release(acc)
		lastStatus, lastBody = resp.Status, body
		h.Pool.ReportFailure(acc, resp.Status, body, skey, model)
		if pool.FatalRequest(pool.Classify(resp.Status, body)) {
			break
		}
	}

	if lastResp == nil {
		if lastStatus == 0 {
			lastStatus = http.StatusBadGateway
		}
		h.writeUpstreamError(w, lastStatus, lastBody)
		return
	}
	defer h.Pool.Release(lastAcc)
	defer lastResp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Loomy-Account", lastAcc.Name)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	var firstAt time.Time
	agg := newAggregator()

	sc := newSSEStream(lastResp.Body)
	for {
		obj, ok, err := sc.Next()
		if err != nil {
			// 上游断流：保留已拿到的部分，收尾由下面的统一兜底负责。
			break
		}
		if !ok {
			break
		}
		if firstAt.IsZero() {
			firstAt = time.Now()
		}
		agg.Feed(obj)
		buf, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", buf); err != nil {
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	// 上游的 [DONE] 只是结束标记、不会被转发，所以这里必须补发恰好一个。
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	elapsed := time.Since(started)
	pts, prompt, completion, reasoning := usageParts(agg.Usage())
	h.Pool.ReportSuccess(lastAcc, skey, model, pts)
	ttfb := int64(0)
	if !firstAt.IsZero() {
		ttfb = firstAt.Sub(started).Milliseconds()
	}
	h.record(usage.Record{
		Account: lastAcc.Name, Model: model, Kind: "chat", Stream: true, Status: 200,
		Prompt: prompt, Completion: completion, Reasoning: reasoning, Points: pts,
		LatencyMs: elapsed.Milliseconds(), TTFBMs: ttfb,
		ClientIP: clientIP(r), UserAgent: ua(r),
	})
	h.logStream(lastAcc, model, elapsed, ttfb, completion, reasoning, pts)
}

// responses POST /v1/responses（Codex Responses API → chat completions）。
func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	req, err := readJSON(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	chatReq, err := bridge.ResponsesToChat(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	model := h.resolveModel(chatReq)
	chatReq["model"] = model
	skey := pool.SessionKey(chatReq)
	extra := headerExtras(r, req)

	if truthy(req["stream"]) {
		h.responsesStream(w, r, chatReq, model, skey, extra)
		return
	}
	obj, err := h.collectStream(r.Context(), chatReq, model, skey, extra)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	writeJSON(w, bridge.ChatToResponse(obj, model))
}

// responsesStream Codex Responses SSE 输出。
func (h *Handler) responsesStream(w http.ResponseWriter, r *http.Request, chatReq map[string]any, model, skey string, extra map[string]string) {
	started := time.Now()
	sreq := cloneMap(chatReq)
	sreq["stream"] = true

	var tried []string
	var acc *pool.Account
	var resp *upstream.Response
	var lastStatus int
	var lastBody string
	for attempt := 0; attempt <= h.Cfg.MaxRetries; attempt++ {
		a, err := h.Pool.Acquire(tried, model, skey)
		if err != nil {
			if len(tried) == 0 {
				writeErr(w, http.StatusServiceUnavailable, err.Error(), "upstream_error")
				return
			}
			break
		}
		tried = append(tried, a.Name)
		rs, err := h.Upstream.Do(r.Context(), http.MethodPost, "chat/completions", a.Session, sreq, extra)
		if err != nil {
			h.Pool.Release(a)
			h.Pool.ReportFailure(a, 0, err.Error(), skey, model)
			lastStatus, lastBody = http.StatusBadGateway, err.Error()
			continue
		}
		if rs.Status == http.StatusOK {
			acc, resp = a, rs
			break
		}
		body := rs.BodyText()
		h.Pool.Release(a)
		lastStatus, lastBody = rs.Status, body
		h.Pool.ReportFailure(a, rs.Status, body, skey, model)
		if pool.FatalRequest(pool.Classify(rs.Status, body)) {
			break
		}
	}
	if resp == nil {
		if lastStatus == 0 {
			lastStatus = http.StatusBadGateway
		}
		h.writeUpstreamError(w, lastStatus, lastBody)
		return
	}
	defer h.Pool.Release(acc)
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Loomy-Account", acc.Name)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emit := func(ev bridge.SSEEvent) {
		buf, err := json.Marshal(ev.Data)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, buf)
		if flusher != nil {
			flusher.Flush()
		}
	}

	st := bridge.NewResponsesStreamer(model, "")
	for _, ev := range st.Start() {
		emit(ev)
	}
	sc := newSSEStream(resp.Body)
	for {
		obj, ok, err := sc.Next()
		if err != nil || !ok {
			break
		}
		for _, ev := range st.Feed(obj) {
			emit(ev)
		}
	}
	for _, ev := range st.Finish() {
		emit(ev)
	}

	pts, prompt, completion, reasoning := usageParts(st.Usage())
	h.Pool.ReportSuccess(acc, skey, model, pts)
	h.record(usage.Record{
		Account: acc.Name, Model: model, Kind: "responses", Stream: true, Status: 200,
		Prompt: prompt, Completion: completion, Reasoning: reasoning, Points: pts,
		LatencyMs: time.Since(started).Milliseconds(),
		ClientIP:  clientIP(r), UserAgent: ua(r),
	})
	h.logStream(acc, model, time.Since(started), 0, completion, reasoning, pts)
}

// anthropicMessages POST /v1/messages。
func (h *Handler) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	req, err := readJSON(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败: "+err.Error(), "invalid_request_error")
		return
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		writeAnthropicErr(w, http.StatusBadRequest, "messages 不能为空")
		return
	}
	chatReq := bridge.ToOpenAI(req)
	model := h.resolveModel(chatReq)
	chatReq["model"] = model
	skey := pool.SessionKey(chatReq)
	extra := headerExtras(r, req)

	if truthy(req["stream"]) {
		h.anthropicStream(w, r, chatReq, model, skey, extra)
		return
	}
	obj, err := h.collectStream(r.Context(), chatReq, model, skey, extra)
	if err != nil {
		writeAnthropicErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, bridge.ToAnthropic(obj, model))
}

// anthropicStream Anthropic SSE 输出。
func (h *Handler) anthropicStream(w http.ResponseWriter, r *http.Request, chatReq map[string]any, model, skey string, extra map[string]string) {
	started := time.Now()
	sreq := cloneMap(chatReq)
	sreq["stream"] = true

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// 先占好账号与响应头，避免把错误当成 SSE 发出去。
	var tried []string
	var acc *pool.Account
	var resp *upstream.Response
	var lastStatus int
	var lastBody string
	for attempt := 0; attempt <= h.Cfg.MaxRetries; attempt++ {
		a, err := h.Pool.Acquire(tried, model, skey)
		if err != nil {
			if len(tried) == 0 {
				writeAnthropicErr(w, http.StatusServiceUnavailable, err.Error())
				return
			}
			break
		}
		tried = append(tried, a.Name)
		rs, err := h.Upstream.Do(r.Context(), http.MethodPost, "chat/completions", a.Session, sreq, extra)
		if err != nil {
			h.Pool.Release(a)
			h.Pool.ReportFailure(a, 0, err.Error(), skey, model)
			lastStatus, lastBody = http.StatusBadGateway, err.Error()
			continue
		}
		if rs.Status == http.StatusOK {
			acc, resp = a, rs
			break
		}
		body := rs.BodyText()
		h.Pool.Release(a)
		lastStatus, lastBody = rs.Status, body
		h.Pool.ReportFailure(a, rs.Status, body, skey, model)
		if pool.FatalRequest(pool.Classify(rs.Status, body)) {
			break
		}
	}
	if resp == nil {
		if lastStatus == 0 {
			lastStatus = http.StatusBadGateway
		}
		writeAnthropicErr(w, lastStatus, lastBody)
		return
	}
	defer h.Pool.Release(acc)
	defer resp.Body.Close()

	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emit := func(ev bridge.Event) {
		buf, err := json.Marshal(ev.Data)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, buf)
		if flusher != nil {
			flusher.Flush()
		}
	}

	st := bridge.NewStreamTranslator(model, "")
	for _, ev := range st.Start() {
		emit(ev)
	}
	sc := newSSEStream(resp.Body)
	for {
		obj, ok, err := sc.Next()
		if err != nil || !ok {
			break
		}
		for _, ev := range st.Feed(obj) {
			emit(ev)
		}
	}
	for _, ev := range st.Finish() {
		emit(ev)
	}

	pts, prompt, completion, reasoning := usageParts(st.Usage())
	h.Pool.ReportSuccess(acc, skey, model, pts)
	h.record(usage.Record{
		Account: acc.Name, Model: model, Kind: "messages", Stream: true, Status: 200,
		Prompt: prompt, Completion: completion, Reasoning: reasoning, Points: pts,
		LatencyMs: time.Since(started).Milliseconds(),
		ClientIP:  clientIP(r), UserAgent: ua(r),
	})
}

// passthrough 图片 / 向量等固定上游端点。
func (h *Handler) passthrough(suffix, kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取请求体失败", "invalid_request_error")
			return
		}
		var payload map[string]any
		if len(body) > 0 {
			if err := json.Unmarshal(body, &payload); err != nil {
				writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON", "invalid_request_error")
				return
			}
		}
		model := h.resolveModel(payload)

		started := time.Now()
		var tried []string
		var lastStatus int
		var lastBody string
		for attempt := 0; attempt <= h.Cfg.MaxRetries; attempt++ {
			acc, err := h.Pool.Acquire(tried, model, "")
			if err != nil {
				if len(tried) == 0 {
					writeErr(w, http.StatusServiceUnavailable, err.Error(), "upstream_error")
					return
				}
				break
			}
			tried = append(tried, acc.Name)
			resp, err := h.Upstream.Do(r.Context(), http.MethodPost, suffix, acc.Session, payload, nil)
			if err != nil {
				h.Pool.Release(acc)
				h.Pool.ReportFailure(acc, 0, err.Error(), "", model)
				lastStatus, lastBody = http.StatusBadGateway, err.Error()
				continue
			}
			out := resp.BodyText()
			h.Pool.Release(acc)
			if resp.Status == http.StatusOK {
				var obj map[string]any
				usageObj := map[string]any{}
				if json.Unmarshal([]byte(out), &obj) == nil {
					usageObj, _ = obj["usage"].(map[string]any)
				}
				pts, prompt, completion, reasoning := usageParts(usageObj)
				h.Pool.ReportSuccess(acc, "", model, pts)
				h.record(usage.Record{
					Account: acc.Name, Model: model, Kind: kind, Status: 200,
					Prompt: prompt, Completion: completion, Reasoning: reasoning, Points: pts,
					LatencyMs: time.Since(started).Milliseconds(),
					ClientIP:  clientIP(r), UserAgent: ua(r),
				})
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("X-Loomy-Account", acc.Name)
				w.WriteHeader(200)
				_, _ = io.WriteString(w, out)
				return
			}
			lastStatus, lastBody = resp.Status, out
			h.Pool.ReportFailure(acc, resp.Status, out, "", model)
			if pool.FatalRequest(pool.Classify(resp.Status, out)) {
				break
			}
		}
		if lastStatus == 0 {
			lastStatus = http.StatusBadGateway
		}
		h.writeUpstreamError(w, lastStatus, lastBody)
	}
}

// search POST /v1/search（内置联网检索，只读，不占账号额度）。
func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	payload, err := readJSON(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败", "invalid_request_error")
		return
	}
	session := h.anySession()
	if session == "" {
		writeErr(w, http.StatusServiceUnavailable, "没有可用账号", "upstream_error")
		return
	}
	obj, status, err := h.Upstream.Search(r.Context(), session, payload)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	if status != 200 {
		h.writeUpstreamError(w, status, toJSON(obj))
		return
	}
	writeJSON(w, obj)
}

// ---------------------------------------------------------------- 工具函数

func (h *Handler) resolveModel(payload map[string]any) string {
	model := str(payload["model"])
	if model == "" {
		model = h.Cfg.DefaultModel
	}
	if h.Cfg.StripPrefix {
		// 接受 "imodel/spark-x" 形态；上游本身也用 provider/model，
		// 所以只在缺前缀时补，不强行剥掉。
		if !strings.Contains(model, "/") {
			model = "imodel/" + model
		}
	}
	if payload != nil {
		payload["model"] = model
	}
	return model
}

func (h *Handler) record(r usage.Record) {
	if h.Ledger != nil {
		h.Ledger.Record(r)
	}
}

func (h *Handler) logCall(acc *pool.Account, model string, status int, d time.Duration, note string) {
	if !h.Cfg.LogRequests {
		return
	}
	name := "-"
	if acc != nil {
		name = acc.Name
	}
	seq := h.reqSeq.Add(1)
	log.Printf("| #%03d | %s | %-11s | %3d | %s | %6dms | %s |",
		seq, time.Now().Format("15:04:05"), truncate(model, 11), status,
		truncate(name, 20), d.Milliseconds(), note)
}

func (h *Handler) logStream(acc *pool.Account, model string, d time.Duration, ttfbMs, completion, reasoning, pts int64) {
	if !h.Cfg.LogRequests {
		return
	}
	seq := h.reqSeq.Add(1)
	rate := "-"
	if gen := d.Milliseconds() - ttfbMs; gen > 0 && completion > 0 {
		rate = fmt.Sprintf("%.1ftok/s", float64(completion)/float64(gen)*1000)
	}
	name := "-"
	if acc != nil {
		name = acc.Name
	}
	extra := ""
	if reasoning > 0 {
		extra = fmt.Sprintf(" think=%d", reasoning)
	}
	log.Printf("| #%03d | %s | %-11s | stream | 200 | %s | TTFB=%dms | tok=%d%s | %s | total=%s |",
		seq, time.Now().Format("15:04:05"), truncate(model, 11), truncate(name, 20),
		ttfbMs, completion, extra, rate, d.Round(time.Millisecond))
	_ = pts
}

func (h *Handler) writeUpstreamError(w http.ResponseWriter, status int, body string) {
	// 上游错误体尽量原样透传（信息量比自造的大）。
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err == nil && obj != nil {
		if _, ok := obj["error"]; ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			writeJSON(w, obj)
			return
		}
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	writeErr(w, status, truncate(body, 800), "upstream_error")
}

// SSEStream SSE 增量读取器。
type SSEStream struct {
	body    io.Reader
	buf     []byte
	eof     bool
	sawDone bool
}

func newSSEStream(body io.Reader) *SSEStream {
	return &SSEStream{body: body, buf: make([]byte, 0, 16<<10)}
}

// Next 读下一个 data 帧；ok=false 表示流结束。
func (s *SSEStream) Next() (map[string]any, bool, error) {
	tmp := make([]byte, 8<<10)
	for {
		// 从缓冲里切出一行
		if i := indexByte(s.buf, '\n'); i >= 0 {
			line := trimCR(s.buf[:i])
			s.buf = s.buf[i+1:]
			if obj, done, ok := parseSSELine(line); ok {
				if done {
					s.sawDone = true
					return nil, false, nil
				}
				return obj, true, nil
			}
			continue
		}
		if s.eof {
			if len(s.buf) > 0 {
				line := trimCR(s.buf)
				s.buf = s.buf[:0]
				if obj, done, ok := parseSSELine(line); ok {
					if done {
						s.sawDone = true
						return nil, false, nil
					}
					return obj, true, nil
				}
			}
			return nil, false, nil
		}
		n, err := s.body.Read(tmp)
		if n > 0 {
			s.buf = append(s.buf, tmp[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.eof = true
				continue
			}
			return nil, false, err
		}
	}
}

func parseSSELine(line []byte) (map[string]any, bool, bool) {
	if !hasPrefix(line, "data:") {
		return nil, false, false
	}
	payload := trimSpace(line[5:])
	if len(payload) == 0 {
		return nil, false, false
	}
	if string(payload) == "[DONE]" {
		return nil, true, true
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, false, false
	}
	return obj, false, true
}

// Aggregator 把 SSE 增量聚合成一条完整响应。
type Aggregator struct {
	content strings.Builder
	reason  strings.Builder
	tools   map[int]*toolBuf
	order   []int
	finish  string
	usage   map[string]any
	id      string
	created int64
	model   string
}

type toolBuf struct {
	ID   string
	Name string
	Args strings.Builder
}

func newAggregator() *Aggregator {
	return &Aggregator{tools: map[int]*toolBuf{}, usage: map[string]any{}}
}

// Feed 吃一个增量。
func (a *Aggregator) Feed(obj map[string]any) {
	if id := str(obj["id"]); id != "" {
		a.id = id
	}
	if m := str(obj["model"]); m != "" {
		a.model = m
	}
	if c, ok := obj["created"].(float64); ok && c > 0 {
		a.created = int64(c)
	}
	if u, ok := obj["usage"].(map[string]any); ok && len(u) > 0 {
		for k, v := range u {
			a.usage[k] = v
		}
	}
	choices, _ := obj["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	ch, _ := choices[0].(map[string]any)
	if fr := str(ch["finish_reason"]); fr != "" {
		a.finish = fr
	}
	delta, _ := ch["delta"].(map[string]any)
	if delta == nil {
		delta, _ = ch["message"].(map[string]any)
	}
	if delta == nil {
		return
	}
	if t := str(delta["content"]); t != "" {
		a.content.WriteString(t)
	}
	if r := str(delta["reasoning_content"]); r != "" {
		a.reason.WriteString(r)
	}
	if calls, ok := delta["tool_calls"].([]any); ok {
		for _, raw := range calls {
			call, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			idx, _ := intOf(call["index"])
			buf, exists := a.tools[idx]
			if !exists {
				buf = &toolBuf{}
				a.tools[idx] = buf
				a.order = append(a.order, idx)
			}
			if id := str(call["id"]); id != "" {
				buf.ID = id
			}
			fn, _ := call["function"].(map[string]any)
			if name := str(fn["name"]); name != "" {
				buf.Name = name
			}
			if args := str(fn["arguments"]); args != "" {
				buf.Args.WriteString(args)
			}
		}
	}
}

// Usage 累积的 usage。
func (a *Aggregator) Usage() map[string]any { return a.usage }

// Result 拼出非流式响应。
func (a *Aggregator) Result(model string) map[string]any {
	content := a.content.String()
	msg := map[string]any{"role": "assistant", "content": content}
	if r := a.reason.String(); r != "" {
		msg["reasoning_content"] = r
	}
	if len(a.order) > 0 {
		sort.Ints(a.order)
		calls := make([]any, 0, len(a.order))
		for _, idx := range a.order {
			buf := a.tools[idx]
			if buf.Name == "" {
				continue
			}
			id := buf.ID
			if id == "" {
				id = "call_" + randomHex(6)
			}
			args := buf.Args.String()
			if args == "" {
				args = "{}"
			}
			calls = append(calls, map[string]any{
				"id": id, "type": "function",
				"function": map[string]any{"name": buf.Name, "arguments": args},
			})
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
			if content == "" {
				msg["content"] = nil
			}
		}
	}
	id := a.id
	if id == "" {
		id = "chatcmpl-" + randomHex(6)
	}
	finish := a.finish
	if finish == "" {
		finish = "stop"
	}
	name := a.model
	if name == "" {
		name = model
	}
	created := a.created
	if created == 0 {
		created = time.Now().Unix()
	}
	return map[string]any{
		"id": id, "object": "chat.completion", "created": created, "model": name,
		"choices": []any{map[string]any{
			"index": 0, "message": msg, "logprobs": nil, "finish_reason": finish,
		}},
		"usage": a.usage,
	}
}

// headerExtras 从请求体里摘出归属头参数（ChatId / MsgId / TurnId / purpose）。
func headerExtras(r *http.Request, payload map[string]any) map[string]string {
	extra := map[string]string{}
	for _, pair := range [][2]string{
		{"chat_id", "ChatId"}, {"msg_id", "MsgId"}, {"turn_id", "TurnId"},
	} {
		if v, ok := payload[pair[0]]; ok {
			if s := str(v); s != "" {
				extra[pair[1]] = s
			}
			delete(payload, pair[0])
		}
	}
	if v, ok := payload["request_purpose"]; ok {
		if s := str(v); s != "" {
			extra["X-Loomy-Request-Purpose"] = s
		}
		delete(payload, "request_purpose")
	}
	// 客户端自带的 traceparent 优先（保持链路串起来）。
	if tp := r.Header.Get("traceparent"); tp != "" {
		extra["traceparent"] = tp
	}
	return extra
}

func (h *Handler) writeUpstreamErrorPreserving(w http.ResponseWriter, status int, body string) {
	h.writeUpstreamError(w, status, body)
}

// Version 网关版本（发布时由 CI 用 -X 注入）。
var Version = "dev"
