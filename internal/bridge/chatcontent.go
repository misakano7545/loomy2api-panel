// chatcontent.go 桥接层共用的 content 构造器：图片不许丢。
//
// 背景（2026-11-11）：两条桥接都把图片**静默**丢掉，客户端以为图发出去了，上游只看到一句文字 ——
//   - responses.go 的 ContentToText：input_image → 字面量 "[image]"；
//   - anthropic.go 的 BlocksToText：压根没有 image 分支。
// 表现就是模型回「我看不到图片 / 消息里没有附带图像」，而且不报错。
//
// 这里给出统一构造器：**没有图片时输出纯字符串（与旧行为逐字一致），一旦出现图片就输出标准
// chat/completions 的 content 数组**，图片一律转成 {"type":"image_url","image_url":{"url":…}}，
// data URI 与 http(s) URL 都原样透传。
package bridge

import (
	"fmt"
	"strings"
)

// ChatContent 按出现顺序拼 chat/completions 的 content。
type ChatContent struct {
	parts    []any
	texts    []string
	hasImage bool
}

// AddText 追加一段文本（空串忽略）。
func (c *ChatContent) AddText(s string) {
	if s == "" {
		return
	}
	c.texts = append(c.texts, s)
	c.push(map[string]any{"type": "text", "text": s}, false)
}

// AddImage 追加一张图片（空 URL 忽略）。
func (c *ChatContent) AddImage(url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	c.hasImage = true
	c.push(map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}, true)
}

// HasContent 有文本或有图片即视为有内容。
func (c *ChatContent) HasContent() bool { return len(c.texts) > 0 || c.hasImage }

// Value 无图返回纯字符串（旧行为），有图返回 content 数组。
func (c *ChatContent) Value() any {
	if !c.hasImage {
		return strings.Join(c.texts, "\n")
	}
	if len(c.parts) == 0 {
		return ""
	}
	return c.parts
}

// push 追加一块；相邻文本块合并，图片块永不合并。
func (c *ChatContent) push(part map[string]any, isImage bool) {
	if !isImage && len(c.parts) > 0 {
		if last, ok := c.parts[len(c.parts)-1].(map[string]any); ok && str(last["type"]) == "text" {
			last["text"] = str(last["text"]) + "\n" + str(part["text"])
			return
		}
	}
	c.parts = append(c.parts, part)
}

// AnthropicImageURL 从 Anthropic 的 image 块取 chat 可用的 URL。
//
// 支持 source.type=base64（media_type + data → data URI）与 source.type=url 两种。
func AnthropicImageURL(block map[string]any) (string, bool) {
	src, ok := block["source"].(map[string]any)
	if !ok {
		return "", false
	}
	switch str(src["type"]) {
	case "base64":
		data := strings.TrimSpace(str(src["data"]))
		if data == "" {
			return "", false
		}
		mt := strings.TrimSpace(str(src["media_type"]))
		if mt == "" {
			mt = "image/png"
		}
		return "data:" + mt + ";base64," + data, true
	case "url":
		u := strings.TrimSpace(str(src["url"]))
		return u, u != ""
	}
	return "", false
}

// ResponsesImageURL 从 Responses 的 input_image 条目取 chat 可用的 URL。
//
// 两种 url 形态都认：{"image_url":"data:…"}（规范写法）与 {"image_url":{"url":"…"}}（宽口径客户端）。
func ResponsesImageURL(item map[string]any) (string, bool) {
	switch v := item["image_url"].(type) {
	case string:
		s := strings.TrimSpace(v)
		return s, s != ""
	case map[string]any:
		s := strings.TrimSpace(str(v["url"]))
		return s, s != ""
	}
	return "", false
}

// ChatContentFromResponses 整段 Responses content（字符串或条目数组）→ chat content。
func ChatContentFromResponses(content any) any {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		cc := &ChatContent{}
		for _, raw := range c {
			switch item := raw.(type) {
			case string:
				cc.AddText(item)
			case map[string]any:
				switch str(item["type"]) {
				case "input_text", "output_text", "text", "":
					cc.AddText(str(item["text"]))
				case "input_image":
					if u, ok := ResponsesImageURL(item); ok {
						cc.AddImage(u)
					}
				}
			}
		}
		return cc.Value()
	}
	return fmt.Sprint(content)
}

// ChatContentFromAnthropic 整段 Anthropic content（字符串或块数组）→ chat content。
//
// 供 tool_result 的 content、以及整段场景使用；逐块循环见 ToOpenAI。
func ChatContentFromAnthropic(content any) any {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		cc := &ChatContent{}
		for _, raw := range c {
			switch b := raw.(type) {
			case string:
				cc.AddText(b)
			case map[string]any:
				switch str(b["type"]) {
				case "text":
					cc.AddText(str(b["text"]))
				case "image":
					if u, ok := AnthropicImageURL(b); ok {
						cc.AddImage(u)
					}
				case "tool_result":
					// 工具结果里也可能带图（截图类工具）：递归保住。
					if v, ok := ChatContentFromAnthropic(b["content"]).([]any); ok {
						for _, p := range v {
							if pm, ok := p.(map[string]any); ok {
								if str(pm["type"]) == "image_url" {
									if iu, ok := pm["image_url"].(map[string]any); ok {
										cc.AddImage(str(iu["url"]))
										continue
									}
								}
								cc.AddText(str(pm["text"]))
							}
						}
					} else {
						cc.AddText(fmt.Sprint(ChatContentFromAnthropic(b["content"])))
					}
				}
			}
		}
		return cc.Value()
	}
	return fmt.Sprint(content)
}

// assistantText 取 assistant 的正文。
//
// 上游绝大多数返回字符串，但 OpenAI 兼容实现里也有返回 content 数组的；只认字符串
// 会把正文静默丢成空回复（客户端看到一条空消息）。两种形态都认。
func assistantText(message map[string]any) string {
	switch content := message["content"].(type) {
	case string:
		return content
	case []any:
		var out []string
		for _, raw := range content {
			switch p := raw.(type) {
			case string:
				if p != "" {
					out = append(out, p)
				}
			case map[string]any:
				if t := str(p["text"]); t != "" {
					out = append(out, t)
				}
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}
