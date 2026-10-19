package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHeadersRequired 三个必需头齐全且 Authorization 双写。
func TestHeadersRequired(t *testing.T) {
	c := NewClient("https://example.com/api/v1", "0.9.38", "", 10*time.Second)
	h := c.Headers("sess-abc", nil)

	if got := h.Get("Authorization"); got != "Bearer sess-abc" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.Get("token"); got != "sess-abc" {
		t.Errorf("token = %q（useSessionAuth 双写）", got)
	}
	if got := h.Get("loomy-version"); got != "0.9.38" {
		t.Errorf("loomy-version = %q", got)
	}
	tp := h.Get("traceparent")
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 {
		t.Errorf("traceparent 格式不对: %q（缺它上游会挂死到超时）", tp)
	}
	if parts[3] != "01" {
		t.Errorf("traceparent 采样标志 = %q", parts[3])
	}
}

// TestHeadersTraceparentUnique 每次请求的 traceparent 必须不同。
func TestHeadersTraceparentUnique(t *testing.T) {
	c := NewClient("https://example.com", "1.0.0", "", time.Second)
	a := c.Headers("s", nil).Get("traceparent")
	b := c.Headers("s", nil).Get("traceparent")
	if a == b {
		t.Error("traceparent 应每请求重新生成")
	}
}

// TestHeadersExtraOverrides 额外头能覆盖默认值。
func TestHeadersExtraOverrides(t *testing.T) {
	c := NewClient("https://example.com", "1.0.0", "", time.Second)
	h := c.Headers("s", map[string]string{"ChatId": "c1", "traceparent": "custom"})
	if h.Get("ChatId") != "c1" {
		t.Errorf("ChatId = %q", h.Get("ChatId"))
	}
	if h.Get("traceparent") != "custom" {
		t.Errorf("额外头应可覆盖 traceparent，实际 %q", h.Get("traceparent"))
	}
}

// TestHeadersSkipsEmptyExtra 空值额外头不应写入。
func TestHeadersSkipsEmptyExtra(t *testing.T) {
	c := NewClient("https://example.com", "1.0.0", "", time.Second)
	h := c.Headers("s", map[string]string{"ChatId": ""})
	if h.Get("ChatId") != "" {
		t.Error("空值额外头不应写入")
	}
}

// TestModelsParsing /models 解析成模型目录。
func TestModelsParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("路径 = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"imodel/spark-x","name":"Spark X2.5","multiplier":0.1,
			 "context_length":1048576,"max_output_tokens":8192,
			 "capabilities":{"text":true,"image":true,"audio":false},
			 "reasoning_efforts":["low","high"]}
		]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	models, err := c.Models(context.Background(), "sess")
	if err != nil {
		t.Fatalf("/models 失败: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("模型数 %d", len(models))
	}
	m := models[0]
	if m.ID != "imodel/spark-x" || m.Name != "Spark X2.5" {
		t.Errorf("模型元信息不对: %+v", m)
	}
	if m.Multiplier != 0.1 || m.ContextLength != 1048576 || m.MaxOutput != 8192 {
		t.Errorf("数值字段不对: %+v", m)
	}
	// 只收集 true 的能力
	if len(m.Capabilities) != 2 {
		t.Errorf("能力项应只收 true 的: %v", m.Capabilities)
	}
	if len(m.ReasoningTiers) != 2 {
		t.Errorf("思考档位未解析: %v", m.ReasoningTiers)
	}
}

// TestModelsNon200 /models 非 200 应返回错误。
func TestModelsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	if _, err := c.Models(context.Background(), "s"); err == nil {
		t.Error("非 200 应返回错误")
	}
}

// TestQuotaCamelCasePageSize 上游只认 camelCase 的 pageSize。
func TestQuotaCamelCasePageSize(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"data":{"balance":100,"dailyBalance":20,"availableBalance":120}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	balance, daily, available, err := c.Quota(context.Background(), "s")
	if err != nil {
		t.Fatalf("Quota 失败: %v", err)
	}
	if !strings.Contains(gotQuery, "pageSize=") {
		t.Errorf("必须用 camelCase pageSize（下划线会被静默忽略）: %q", gotQuery)
	}
	if strings.Contains(gotQuery, "page_size=") {
		t.Errorf("不应使用 page_size: %q", gotQuery)
	}
	if balance == nil || *balance != 100 {
		t.Errorf("balance = %v", balance)
	}
	if daily == nil || *daily != 20 {
		t.Errorf("daily = %v", daily)
	}
	if available == nil || *available != 120 {
		t.Errorf("available = %v", available)
	}
}

// TestQuotaDerivesAvailable 上游没给 availableBalance 时用 balance+daily 估算。
func TestQuotaDerivesAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"balance":70,"dailyBalance":30}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	_, _, available, err := c.Quota(context.Background(), "s")
	if err != nil {
		t.Fatalf("Quota 失败: %v", err)
	}
	if available == nil || *available != 100 {
		t.Errorf("available 应为 100，实际 %v", available)
	}
}

// TestQuotaEmptyData 空 data 不 panic。
func TestQuotaEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	b, d, a, err := c.Quota(context.Background(), "s")
	if err != nil {
		t.Fatalf("空 data 不应报错: %v", err)
	}
	if b != nil || d != nil || a != nil {
		t.Errorf("空 data 时应返回 nil: %v %v %v", b, d, a)
	}
}

// TestPointsPaginationAndDedup 积分流水分页 + 去重 + 收支汇总。
func TestPointsPaginationAndDedup(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// 第一页满 100 条 → 会翻第二页
			items := make([]map[string]any, 0, 100)
			for i := 0; i < 100; i++ {
				items = append(items, map[string]any{
					"ledgerId":  "l" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
					"direction": "debit", "pointsActual": 1, "modelName": "m",
				})
			}
			payload, _ := json.Marshal(map[string]any{"data": map[string]any{
				"balance": 500, "dailyBalance": 10, "availableBalance": 510,
				"list": items,
			}})
			_, _ = w.Write(payload)
			return
		}
		// 第二页：含一条与第一页重复的 ledgerId
		_, _ = io.WriteString(w, `{"data":{"list":[
			{"ledgerId":"dup","direction":"credit","pointsActual":100,"description":"签到"},
			{"ledgerId":"dup","direction":"credit","pointsActual":100,"description":"签到"}
		]}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	out, err := c.Points(context.Background(), "s", 5)
	if err != nil {
		t.Fatalf("Points 失败: %v", err)
	}
	if calls != 2 {
		t.Errorf("应翻 2 页（第一页满 100 条），实际 %d", calls)
	}
	records, _ := out["records"].([]map[string]any)
	if len(records) != 101 {
		t.Errorf("去重后应 101 条（100 + 1 条唯一 credit），实际 %d", len(records))
	}
	if out["credits"].(int64) != 100 {
		t.Errorf("credits = %v，期望 100（重复条目不应累计）", out["credits"])
	}
	if out["balance"] == nil {
		t.Errorf("balance 未返回: %v", out["balance"])
	}
}

// TestSearch 内置检索。
func TestSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/search/tencent") {
			t.Errorf("路径 = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"x"}]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	obj, status, err := c.Search(context.Background(), "s", map[string]any{"query": "天气"})
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if status != 200 {
		t.Errorf("状态码 %d", status)
	}
	if obj["results"] == nil {
		t.Errorf("结果未透传: %v", obj)
	}
}

// TestPostJSONNonJSONBody 上游返回非 JSON 时不报错（_raw 兜底，交给错误分类）。
func TestPostJSONNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	obj, status, err := c.PostJSON(context.Background(), "chat/completions", "s", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("非 JSON 不应报错: %v", err)
	}
	if status != 502 {
		t.Errorf("状态码 %d", status)
	}
	if obj["_raw"] == nil {
		t.Errorf("应把原文放进 _raw: %v", obj)
	}
}

// TestGetJSON 语法糖与 Do 等价。
func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("方法 = %s", r.Method)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1.0.0", "", 5*time.Second)
	obj, status, err := c.GetJSON(context.Background(), "models", "s")
	if err != nil {
		t.Fatalf("GetJSON 失败: %v", err)
	}
	if status != 200 || obj["ok"] != true {
		t.Errorf("响应不对: %d %v", status, obj)
	}
}
