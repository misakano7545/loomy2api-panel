// classify.go 上游错误分类与账号处置语义。
package pool

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 错误分类。
type Fault int

const (
	FaultUnknown    Fault = iota
	FaultQuota            // 额度/积分耗尽 → 硬冷却
	FaultModelLimit       // 模型级限流 → 只锁该模型
	FaultRateLimit        // 频控 → 软冷却
	FaultNotFound         // 404 → 短冷却
	FaultSession          // 登录态失效 → 需重登
	FaultServer           // 5xx → 喂熔断
	FaultClient           // 客户端问题 → 不罚账号
)

var (
	reQuotaWords = []string{
		"积分不足", "余额不足", "额度不足", "额度已用完", "insufficient", "quota exceeded",
		"points", "balance",
	}
	reSessionWords = []string{
		"session not found", "session 失效", "session invalid", "unauthorized",
		"invalid token", "登录已过期", "登录已失效", "登录失效", "登录态", "token expired", "401", "100002",
	}
	reRateWords = []string{
		"rate limit", "too many requests", "frequency", "限流", "请求过于频繁", "qps",
	}
	reModelLimit = regexp.MustCompile(`(?i)(model[^,;]{0,24}(limit|exceed|unavailable)|模型[^,;]{0,12}(限流|超限|不可用)|queue\.full|6021)`)
	reResetAt    = regexp.MustCompile(`(?:将在|重置于|reset at|until)\s*(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(?::\d{2})?)`)
)

// Classify 判定一次失败的错误类别。判定优先级：
// 额度耗尽 → 登录态失效 → 模型级限流 → 限流文案 → 状态码兜底。
func Classify(status int, body string) Fault {
	lower := strings.ToLower(body)

	for _, w := range reQuotaWords {
		if strings.Contains(lower, w) && status >= 400 {
			// 「points」太泛（如 points_consumed 回显），只在 402/403/429 下当真。
			if w == "points" || w == "balance" {
				if status != 402 && status != 403 && status != 429 {
					continue
				}
			}
			return FaultQuota
		}
	}
	if status == 402 {
		return FaultQuota
	}

	if status == 401 {
		return FaultSession
	}
	for _, w := range reSessionWords {
		if w == "401" {
			continue
		}
		if strings.Contains(lower, w) {
			return FaultSession
		}
	}

	if status == 429 || status == 403 {
		if reModelLimit.MatchString(body) {
			return FaultModelLimit
		}
	}
	if reModelLimit.MatchString(body) && status >= 400 {
		return FaultModelLimit
	}

	if status == 429 {
		return FaultRateLimit
	}
	for _, w := range reRateWords {
		if strings.Contains(lower, w) {
			return FaultRateLimit
		}
	}

	switch {
	case status == 404:
		return FaultNotFound
	case status >= 500:
		return FaultServer
	case status >= 400:
		return FaultClient
	}
	return FaultUnknown
}

// ResetAtFromBody 从错误文案里解析上游给出的重置墙钟（解析失败返回 0）。
func ResetAtFromBody(body string) float64 {
	m := reResetAt.FindStringSubmatch(body)
	if len(m) < 2 {
		return 0
	}
	layouts := []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, m[1], time.Local); err == nil {
			return float64(t.Unix())
		}
	}
	return 0
}

// NextDailyReset 明日本地 04:00 的时间戳（额度耗尽硬冷却兜底）。
func NextDailyReset() float64 {
	now := time.Now()
	t := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return float64(t.Unix())
}

// AccountFault 该分类是否算「账号自身的问题」（换号可能有效）。
func AccountFault(f Fault) bool {
	switch f {
	case FaultQuota, FaultSession, FaultRateLimit, FaultModelLimit:
		return true
	}
	return false
}

// FatalRequest 该分类是否不该换号重试（客户端请求本身有问题）。
func FatalRequest(f Fault) bool {
	return f == FaultClient
}

// StatusFromBody 尽力从上游错误体里取 HTTP 状态（有些网关把状态塞进 body）。
func StatusFromBody(body string) int {
	for _, key := range []string{`"status":`, `"code":`} {
		if i := strings.Index(body, key); i >= 0 {
			rest := strings.TrimLeft(body[i+len(key):], " ")
			end := 0
			for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9') {
				end++
			}
			if end > 0 {
				if n, err := strconv.Atoi(rest[:end]); err == nil && n >= 100 && n < 600 {
					return n
				}
			}
		}
	}
	return 0
}
