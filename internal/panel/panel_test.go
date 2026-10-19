package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misakano7545/loomy2api-panel/internal/account"
	"github.com/misakano7545/loomy2api-panel/internal/config"
	"github.com/misakano7545/loomy2api-panel/internal/pool"
	"github.com/misakano7545/loomy2api-panel/internal/server"
	"github.com/misakano7545/loomy2api-panel/internal/upstream"
	"github.com/misakano7545/loomy2api-panel/internal/usage"
)

func newTestPanel(t *testing.T) (*Panel, *pool.Pool) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.AuthDir = filepath.Join(dir, "auths")
	cfg.StateFile = filepath.Join(dir, "state.json")
	cfg.LogRequests = false

	p := pool.New("")
	expire := time.Now().Add(7 * 24 * time.Hour).Unix()
	bal := int64(500)
	p.Add(&pool.Account{
		Name: "acc1", LoginID: "13800000000", Password: "pw",
		Enabled: true, Session: "sess-1", ExpireAt: expire, Available: &bal,
	})

	acc := account.NewClient("https://account.invalid", "APP", "ak", "sk", "")
	up := upstream.NewClient("https://upstream.invalid", "0.9.38", "", 5*time.Second)
	ledger := usage.New("", 100)
	models := server.NewModelCache(up)

	pl := New(cfg, p, acc, up, ledger, models, "test-1.0")
	return pl, p
}

// TestIndexServesHTML 面板首页返回 HTML 且带安全头。
func TestIndexServesHTML(t *testing.T) {
	pl, _ := newTestPanel(t)
	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Loomy2API") {
		t.Error("页面缺少标题")
	}
	if !strings.Contains(body, `src="app.js"`) {
		t.Error("页面应通过同源脚本加载 app.js（CSP script-src 'self'）")
	}
	// 页面不能有内联脚本或内联事件处理器（否则 CSP 会拦掉）
	if strings.Contains(body, "<script>") {
		t.Error("不应有内联脚本")
	}
	if strings.Contains(body, "onclick=") || strings.Contains(body, "onsubmit=") {
		t.Error("不应有内联事件处理器（CSP 会拦）")
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestSecurityHeaders 安全响应头齐全。
func TestSecurityHeaders(t *testing.T) {
	pl, _ := newTestPanel(t)
	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, want := range checks {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q，期望 %q", k, got, want)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP 缺少 %q：%s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Error("CSP 不应允许 unsafe-eval")
	}
}

// TestAppScriptServed app.js 与 style.css 可访问且 MIME 正确。
func TestAppScriptServed(t *testing.T) {
	pl, _ := newTestPanel(t)

	req := httptest.NewRequest(http.MethodGet, "/panel/app.js", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("app.js 状态码 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("app.js Content-Type = %q", ct)
	}
	if len(w.Body.Bytes()) < 1000 {
		t.Errorf("app.js 内容过短: %d 字节", len(w.Body.Bytes()))
	}

	req = httptest.NewRequest(http.MethodGet, "/panel/style.css", nil)
	w = httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("style.css 状态码 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "css") {
		t.Errorf("style.css Content-Type = %q", ct)
	}
}

// TestPanelAuthEnforced 配了 api_key 后数据接口必须鉴权。
func TestPanelAuthEnforced(t *testing.T) {
	pl, _ := newTestPanel(t)
	pl.cfg.APIKeys = []string{"sk-panel"}

	req := httptest.NewRequest(http.MethodGet, "/panel/api/overview", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺密钥时状态码 %d，期望 401", w.Code)
	}

	// 带上正确密钥应放行
	req = httptest.NewRequest(http.MethodGet, "/panel/api/overview", nil)
	req.Header.Set("Authorization", "Bearer sk-panel")
	w = httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("正确密钥时状态码 %d，期望 200", w.Code)
	}
}

// TestPanelPasswordOverridesAPIKey 设了 panel_password 就只认它。
func TestPanelPasswordOverridesAPIKey(t *testing.T) {
	pl, _ := newTestPanel(t)
	pl.cfg.APIKeys = []string{"sk-api"}
	pl.cfg.PanelPass = "panel-pw"

	// 用 api_key 应被拒
	req := httptest.NewRequest(http.MethodGet, "/panel/api/overview", nil)
	req.Header.Set("Authorization", "Bearer sk-api")
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("设了面板密码后 api_key 不应放行，状态码 %d", w.Code)
	}

	// 用面板密码应放行
	req = httptest.NewRequest(http.MethodGet, "/panel/api/overview", nil)
	req.Header.Set("Authorization", "Bearer panel-pw")
	w = httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("面板密码应放行，状态码 %d", w.Code)
	}
}

// TestOverviewPayload overview 返回账号汇总与用量窗口。
func TestOverviewPayload(t *testing.T) {
	pl, _ := newTestPanel(t)
	req := httptest.NewRequest(http.MethodGet, "/panel/api/overview", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("不是 JSON: %v", err)
	}
	if out["version"] != "test-1.0" {
		t.Errorf("version = %v", out["version"])
	}
	if out["total"] != float64(1) {
		t.Errorf("total = %v，期望 1", out["total"])
	}
	if out["usage"] == nil {
		t.Error("缺少 usage 窗口数据")
	}
	if out["hourly"] == nil {
		t.Error("缺少 hourly 趋势数据")
	}
}

// TestListAccountsNoSecrets 账号列表绝不能回显密码或完整 session。
func TestListAccountsNoSecrets(t *testing.T) {
	pl, _ := newTestPanel(t)
	req := httptest.NewRequest(http.MethodGet, "/panel/api/accounts", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, `"pw"`) || strings.Contains(body, "sess-1\"") {
		t.Errorf("响应泄露了凭证明文: %s", body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("不是 JSON: %v", err)
	}
	accounts := out["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("账号数 %d", len(accounts))
	}
	first := accounts[0].(map[string]any)
	if first["has_password"] != true {
		t.Error("has_password 应为 true（只报存在性）")
	}
}

// TestAddAccountRequiresCredential 缺凭证必须报错而不是建个空账号。
func TestAddAccountRequiresCredential(t *testing.T) {
	pl, p := newTestPanel(t)
	body := strings.NewReader(`{"name":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts", body)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["error"] == nil {
		t.Errorf("缺凭证应报错: %s", w.Body.String())
	}
	if p.Get("x") != nil {
		t.Error("不应创建空账号")
	}
}

// TestAddAccountWithSession 直接给 session 也能建账号（不联网）。
func TestAddAccountWithSession(t *testing.T) {
	pl, p := newTestPanel(t)
	body := strings.NewReader(`{"name":"manual","session":"my-session-token"}`)
	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts", body)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["ok"] != true {
		t.Fatalf("添加失败: %s", w.Body.String())
	}
	acc := p.Get("manual")
	if acc == nil {
		t.Fatal("账号未加入池")
	}
	if acc.Session != "my-session-token" {
		t.Errorf("session 未设置: %q", acc.Session)
	}
	if !acc.SessionValid() {
		t.Error("应设置 14 天有效期")
	}
	// 凭证应落盘
	if _, err := pool.LoadAccounts(pl.cfg.Resolve(pl.cfg.AuthDir)); err != nil {
		t.Errorf("账号目录读取失败: %v", err)
	}
}

// TestDisableEnableAccount 启用/禁用开关。
func TestDisableEnableAccount(t *testing.T) {
	pl, p := newTestPanel(t)

	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/acc1/disable", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if p.Get("acc1").Enabled {
		t.Error("禁用未生效")
	}

	req = httptest.NewRequest(http.MethodPost, "/panel/api/accounts/acc1/enable", nil)
	w = httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if !p.Get("acc1").Enabled {
		t.Error("启用未生效")
	}
}

// TestReviveClearsCooldown 解冻清掉冷却与熔断。
func TestReviveClearsCooldown(t *testing.T) {
	pl, p := newTestPanel(t)
	acc := p.Get("acc1")
	acc.Cool(600, pool.CoolSoft, "限流")
	acc.CoolModel("m", 600, "模型限流")
	if !acc.InCooldown() {
		t.Fatal("应处于冷却")
	}

	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/acc1/revive", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	if p.Get("acc1").InCooldown() {
		t.Error("解冻后不应还在冷却")
	}
	if p.Get("acc1").ModelCooled("m") {
		t.Error("模型冷却也应被清除")
	}
}

// TestRemoveAccount 移除账号并删除凭证文件。
func TestRemoveAccount(t *testing.T) {
	pl, p := newTestPanel(t)
	// 先落盘一份凭证
	_ = pool.SaveAccount(pl.cfg.Resolve(pl.cfg.AuthDir), p.Get("acc1"))
	before, _ := pool.LoadAccounts(pl.cfg.Resolve(pl.cfg.AuthDir))
	if len(before) != 1 {
		t.Fatalf("凭证文件数 %d", len(before))
	}

	req := httptest.NewRequest(http.MethodPost, "/panel/api/accounts/acc1/remove", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	if p.Get("acc1") != nil {
		t.Error("账号未从池中移除")
	}
	after, _ := pool.LoadAccounts(pl.cfg.Resolve(pl.cfg.AuthDir))
	if len(after) != 0 {
		t.Errorf("凭证文件未删除: %d", len(after))
	}
}

// TestConfigGetAndSave 配置读写往返，密钥脱敏。
func TestConfigGetAndSave(t *testing.T) {
	pl, _ := newTestPanel(t)
	// 让 Save 有地方写（config.Save 写的是 Load 时的路径）
	path := filepath.Join(t.TempDir(), "config.json")
	if err := configSaveSeed(pl.cfg, path); err != nil {
		t.Fatal(err)
	}
	pl.cfg.APIKeys = []string{"sk-abcdefghij"}

	req := httptest.NewRequest(http.MethodGet, "/panel/api/config", nil)
	req.Header.Set("Authorization", "Bearer sk-abcdefghij")
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	body := w.Body.String()
	if strings.Contains(body, "sk-abcdefghij") {
		t.Errorf("配置视图泄露了 api_key: %s", body)
	}

	req = httptest.NewRequest(http.MethodPost, "/panel/api/config",
		strings.NewReader(`{"pick_top_n":9,"strategy":"lru"}`))
	req.Header.Set("Authorization", "Bearer sk-abcdefghij")
	w = httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["error"] != nil {
		t.Fatalf("保存失败: %v", out["error"])
	}
	if pl.cfg.PickTopN != 9 {
		t.Errorf("热生效失败，pick_top_n = %d", pl.cfg.PickTopN)
	}
	// 池参数也应同步热更新
	if pl.pool.Options().PickTopN != 9 {
		t.Errorf("池参数未同步: %d", pl.pool.Options().PickTopN)
	}
}

// configSaveSeed 让配置指向可写文件（模拟真实首启：Load 到具体路径）。
func configSaveSeed(cfg *config.Config, path string) error {
	if err := config.EnsureAt(path); err != nil {
		return err
	}
	loaded, err := config.Load(path)
	if err != nil {
		return err
	}
	*cfg = *loaded
	return nil
}

// TestLogsEndpoint 日志接口返回数组。
func TestLogsEndpoint(t *testing.T) {
	pl, _ := newTestPanel(t)
	pl.Logf("测试一行")

	req := httptest.NewRequest(http.MethodGet, "/panel/api/logs", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("不是 JSON: %v", err)
	}
	lines, _ := out["lines"].([]any)
	if len(lines) == 0 {
		t.Error("日志为空")
	}
}

// TestRequestsEndpoint 请求记录接口可用。
func TestRequestsEndpoint(t *testing.T) {
	pl, _ := newTestPanel(t)
	pl.ledger.Record(usage.Record{
		Time: time.Now().Unix(), Account: "acc1", Model: "m", Status: 200,
		Prompt: 1, Completion: 2,
	})

	req := httptest.NewRequest(http.MethodGet, "/panel/api/requests?limit=10", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("不是 JSON: %v", err)
	}
	records, _ := out["records"].([]any)
	if len(records) != 1 {
		t.Errorf("记录数 %d，期望 1", len(records))
	}
}

// TestUnknownPanelPath 未定义路径走 404（而非 500）。
func TestUnknownPanelPath(t *testing.T) {
	pl, _ := newTestPanel(t)
	req := httptest.NewRequest(http.MethodGet, "/panel/api/nope", nil)
	w := httptest.NewRecorder()
	pl.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("状态码 %d，期望 404", w.Code)
	}
}
