// Package upstream Loomy 模型网关客户端（OpenAI 兼容上游）。
//
// 上游是 OpenAI 兼容的，但它对请求头有硬要求：
//   - Authorization 与 token 都要带（客户端用 useSessionAuth，网关两者都收）；
//   - traceparent 缺失会让上游挂死到超时（客户端源码里实测的结论）；
//   - loomy-version 只做存在性检查。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/misakano7545/loomy2api-panel/internal/account"
	"github.com/misakano7545/loomy2api-panel/internal/config"
)

// Client 上游模型网关客户端。
type Client struct {
	BaseURL string
	Version string
	Proxy   string
	Timeout time.Duration

	HTTP *http.Client
}

// NewClient 构造客户端。
func NewClient(base, version, proxy string, timeout time.Duration) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		BaseURL: strings.TrimRight(base, "/"),
		Version: version,
		Proxy:   proxy,
		Timeout: timeout,
		HTTP: &http.Client{
			Transport: transport,
			// 总时长不设上限：长思考 / 长输出不能被掐断。
			Timeout: 0,
		},
	}
}

// Headers 生成上游请求头。
func (c *Client) Headers(session string, extra map[string]string) http.Header {
	h := http.Header{}
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+session)
	// 双写：客户端 provider 用 useSessionAuth，上游两种拼写都接受。
	h.Set("token", session)
	h.Set("traceparent", "00-"+account.RandomHex(16)+"-"+account.RandomHex(8)+"-01")
	if c.Version != "" {
		h.Set("loomy-version", c.Version)
	}
	for k, v := range extra {
		if v != "" {
			h.Set(k, v)
		}
	}
	return h
}

// Response 一次上游响应的封装。
type Response struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// JSON 读取完整响应体。
func (r *Response) JSON() (map[string]any, error) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("上游响应不是 JSON（HTTP %d）: %s", r.Status, truncate(string(raw), 200))
	}
	return out, nil
}

// BodyText 读取完整响应体为字符串。
func (r *Response) BodyText() string {
	defer r.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	return string(raw)
}

// Do 发一次上游请求。
func (c *Client) Do(ctx context.Context, method, suffix, session string, payload any, extra map[string]string) (*Response, error) {
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(string(buf))
	}
	url := c.BaseURL + "/" + strings.TrimLeft(suffix, "/")
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header = c.Headers(session, extra)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	// 上游把业务错误（如登录态失效）塞进 200 的 body 里，状态码仍是 200。
	// 流式响应不能预读，其余预读整包后按错误信封改判状态码，交给调用方的
	// 状态码分类逻辑处理，避免错误被当成成功放行。
	if resp.StatusCode == http.StatusOK && !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		status := resp.StatusCode
		if errEnvelope(raw) {
			status = http.StatusBadGateway
		}
		return &Response{Status: status, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

const maxBodyBytes = 32 << 20

// errEnvelope 判定「HTTP 200 + 业务错误」响应：顶层有非零 code、且没有正常
// 响应的数据键（choices/output/object）。错误信封自己也带 data:{}，故不排除 data。
func errEnvelope(raw []byte) bool {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for _, k := range []string{"choices", "output", "object"} {
		if _, ok := obj[k]; ok {
			return false
		}
	}
	code, ok := obj["code"]
	if !ok {
		return false
	}
	s := strings.TrimSpace(fmt.Sprint(code))
	return s != "" && s != "0" && s != "200"
}

// GetJSON 便捷：GET 并解析 JSON。
func (c *Client) GetJSON(ctx context.Context, suffix, session string) (map[string]any, int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	resp, err := c.Do(ctx, http.MethodGet, suffix, session, nil, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.Status, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"_raw": string(raw)}, resp.Status, nil
	}
	return out, resp.Status, nil
}

// PostJSON 便捷：POST 并解析 JSON（状态码非 2xx 也解析，交给调用方分类）。
func (c *Client) PostJSON(ctx context.Context, suffix, session string, payload any, extra map[string]string) (map[string]any, int, error) {
	resp, err := c.Do(ctx, http.MethodPost, suffix, session, payload, extra)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, resp.Status, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"_raw": string(raw)}, resp.Status, nil
	}
	return out, resp.Status, nil
}

// ------------------------------------------------------------------ 端点

// Models 拉取模型目录。
func (c *Client) Models(ctx context.Context, session string) ([]config.Model, error) {
	resp, err := c.Do(ctx, http.MethodGet, "models", session, nil, nil)
	if err != nil {
		return nil, err
	}
	obj, err := resp.JSON()
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("/models HTTP %d", resp.Status)
	}
	list, _ := obj["data"].([]any)
	if list == nil {
		list, _ = obj["models"].([]any)
	}
	out := make([]config.Model, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		model := config.Model{
			ID:            str(m["id"]),
			Name:          str(m["name"]),
			Multiplier:    num(m["multiplier"]),
			ContextLength: int(num(m["context_length"])),
			MaxOutput:     int(num(m["max_output_tokens"])),
		}
		if caps, ok := m["capabilities"].(map[string]any); ok {
			for k, v := range caps {
				if b, _ := v.(bool); b {
					model.Capabilities = append(model.Capabilities, k)
				}
			}
		}
		if efforts, ok := m["reasoning_efforts"].([]any); ok {
			for _, e := range efforts {
				model.ReasoningTiers = append(model.ReasoningTiers, str(e))
			}
		}
		if model.ID != "" {
			out = append(out, model)
		}
	}
	return out, nil
}

// Quota 查询积分余额。上游只认 camelCase 的 pageSize（下划线会被静默忽略）。
func (c *Client) Quota(ctx context.Context, session string) (balance, daily, available *int64, err error) {
	// 该端点上游只挂 GET，POST 会直接 404（额度刷新曾因此永久失败）。
	suffix := "points/records?record_type=all&pageNo=1&pageSize=1"
	obj, status, err := c.GetJSON(ctx, suffix, session)
	if err != nil {
		return nil, nil, nil, err
	}
	if status != 200 {
		return nil, nil, nil, fmt.Errorf("/points/records HTTP %d", status)
	}
	data, _ := obj["data"].(map[string]any)
	if data == nil {
		return nil, nil, nil, nil
	}
	balance = ptrI64(data["balance"])
	daily = ptrI64(data["dailyBalance"])
	available = ptrI64(data["availableBalance"])
	if available == nil {
		var sum int64
		known := false
		if balance != nil {
			sum += *balance
			known = true
		}
		if daily != nil {
			sum += *daily
			known = true
		}
		if known {
			available = &sum
		}
	}
	return balance, daily, available, nil
}

// Points 查询积分流水汇总（面板额度页）。
func (c *Client) Points(ctx context.Context, session string, pages int) (map[string]any, error) {
	if pages < 1 {
		pages = 5
	}
	var records []map[string]any
	var first map[string]any
	seen := map[string]bool{}
	var debits, credits int64
	for p := 1; p <= pages; p++ {
		suffix := fmt.Sprintf("points/records?record_type=all&pageNo=%d&pageSize=100", p)
		obj, status, err := c.GetJSON(ctx, suffix, session)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("/points/records HTTP %d", status)
		}
		data, _ := obj["data"].(map[string]any)
		if data == nil {
			break
		}
		if p == 1 {
			first = data
		}
		list, _ := data["list"].([]any)
		if list == nil {
			list, _ = data["records"].([]any)
		}
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			key := str(m["ledgerId"])
			if key == "" {
				key = str(m["createdAt"]) + str(m["modelName"]) + str(m["pointsActual"])
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			dir := str(m["direction"])
			pts := int64(num(m["pointsActual"]))
			switch dir {
			case "debit":
				debits += pts
			case "credit":
				credits += pts
			}
			records = append(records, map[string]any{
				"direction":     dir,
				"source":        str(m["consumeSource"]),
				"model":         str(m["modelName"]),
				"points":        pts,
				"balance_after": m["balanceAfter"],
				"time":          str(m["createdAt"]),
				"desc":          truncate(str(m["description"]), 80),
			})
		}
		if len(list) < 100 {
			break
		}
	}
	if first == nil {
		first = map[string]any{}
	}
	return map[string]any{
		"balance":        first["balance"],
		"daily_balance":  first["dailyBalance"],
		"available":      first["availableBalance"],
		"daily_quota":    first["dailyQuota"],
		"daily_consumed": first["dailyConsumed"],
		"total":          first["totalBalance"],
		"records":        records,
		"debits":         debits,
		"credits":        credits,
		"pages":          pages,
	}, nil
}

// FirstLogin 触发每日首次登录奖励（幂等：已领过返回 alreadyProcessed）。
func (c *Client) FirstLogin(ctx context.Context, session string) (map[string]any, error) {
	obj, status, err := c.PostJSON(ctx, "points/first-login", session, map[string]any{}, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("/points/first-login HTTP %d", status)
	}
	return obj, nil
}

// Search 内置联网检索（腾讯搜索通道）。
func (c *Client) Search(ctx context.Context, session string, payload map[string]any) (map[string]any, int, error) {
	return c.PostJSON(ctx, "search/tencent", session, payload, nil)
}

// ---------------------------------------------------------------- helpers

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case json.Number:
		return t.String()
	}
	return ""
}

func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case string:
		var f float64
		_, _ = fmt.Sscanf(t, "%f", &f)
		return f
	case json.Number:
		f, _ := t.Float64()
		return f
	}
	return 0
}

func ptrI64(v any) *int64 {
	if v == nil {
		return nil
	}
	n := int64(num(v))
	return &n
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n]
}
