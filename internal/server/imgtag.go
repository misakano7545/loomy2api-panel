// imgtag.go 上游按模型认两种图片形态：个别 deepseek 系只认 <image_base64> 标记，
// 其余（GLM 等）认标准 image_url data URI —— 转错方向等于看不见图，所以按模型分派。
//
// 依据是直连 loomyad 的实测（2026-10）：同一张图喂 deepseek-v4-flash-0731，
// image_url 形态下模型看到的是会被截断的 base64 文本（回「图片以编码/截断的形式
// 发过来」），<image_base64> 标记形态下 prompt 从 164 token 涨到 7650 且能答出画面；
// 而 GLM-5.3-Flash 正好相反，喂标记会回「无法查看图片」。官方客户端里没有这个标记
// （解包 app.asar 零命中），它是上游网关自己的视觉 token 表示，只能按实测适配。
//
// 2026-11-11 收紧：上游已换代成 DeepSeek-V4.1-Flash（deepseek-v4-flash-0731 已 404），
// 新模型**不认**这个标记，而旧的「名字含 deepseek 就转标记」通配会把能看图的模型打成
// 看不见图。实测（同一张已知识别图）：
//   - 标记形态：prompt_tokens 涨到 109381（158334 字符 base64 被当纯文本内联），
//     模型答「无法解码或识别其中的画面内容」；
//   - www 图片 URL 形态（不转标记、原样透传 image_url）：prompt_tokens 只涨到 246，答对。
// 所以把通配收紧成 **显式白名单**：只有 image_tag_models 里列到的模型才转标记，
// 默认空 = 一律透传标准 image_url。要恢复旧行为，把模型 id 填进 image_tag_models。
package server

import "strings"

// prefersImageTag 该模型是否要把图片转成 <image_base64> 标记。
//
// allow 是显式白名单（config 的 image_tag_models），按子串匹配、大小写不敏感。
// 历史上这里是 strings.Contains(strings.ToLower(model), "deepseek") 的通配 ——
// 上游换代后那条规则会把 DeepSeek-V4.1-Flash 打成看不见图（见文件头实测），
// 所以改成白名单：不在名单里就原样透传 image_url。
func prefersImageTag(model string, allow []string) bool {
	if len(allow) == 0 {
		return false
	}
	lower := strings.ToLower(model)
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" && strings.Contains(lower, a) {
			return true
		}
	}
	return false
}

// rewriteImageTags 就地改写 messages 里的 data URI 图片块为 <image_base64> 标记，
// 返回转换的图片数。allow 为空或模型不在名单里时是零成本的空转。
//
// 标记并入相邻文本块：实测通过的形态是「文本 + 标记同一段」，独立 text 块没有实测依据。
// http(s) 图片 URL 不转：标记里放 URL 没有意义，上游认不认也没有实测，保持原样最保守。
func rewriteImageTags(body map[string]any, model string, allow []string) int {
	if body == nil || !prefersImageTag(model, allow) {
		return 0
	}
	msgs, _ := body["messages"].([]any)
	converted := 0
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		// 倒序一轮：替换/合并会改变下标，从后往前才不会被已处理的位置带偏。
		for i := len(parts) - 1; i >= 0; i-- {
			tag, ok := imageTagText(parts[i])
			if !ok {
				continue
			}
			parts = append(parts[:i], parts[i+1:]...)
			switch {
			case i > 0 && appendTextPart(parts[i-1], tag):
			case i < len(parts) && prependTextPart(parts[i], tag):
			default:
				parts = append(parts[:i], append([]any{map[string]any{"type": "text", "text": tag}}, parts[i:]...)...)
			}
			converted++
		}
		msg["content"] = parts
	}
	return converted
}

// imageTagText 单个图片块 → <image_base64>…</image_base64> 标记文本。
func imageTagText(part any) (string, bool) {
	p, ok := part.(map[string]any)
	if !ok {
		return "", false
	}
	switch strings.ToLower(str(p["type"])) {
	case "image_url", "input_image":
	default:
		return "", false
	}
	// 两种 url 形态都认：{"image_url":{"url":"data:…"}}（OpenAI 标准）
	// 与 {"image_url":"data:…"}（宽口径客户端的简化写法）。
	var url string
	switch v := p["image_url"].(type) {
	case string:
		url = v
	case map[string]any:
		url = str(v["url"])
	}
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "data:") {
		return "", false
	}
	meta, payload, ok := strings.Cut(url, ",")
	if !ok || !strings.Contains(strings.ToLower(meta), "base64") {
		return "", false
	}
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", false
	}
	return "<image_base64>" + payload + "</image_base64>", true
}

// appendTextPart 把标记并到 part 的文本末尾（不是 text 块返回 false）。
func appendTextPart(part any, tag string) bool {
	p, ok := part.(map[string]any)
	if !ok || strings.ToLower(str(p["type"])) != "text" {
		return false
	}
	text, ok := p["text"].(string)
	if !ok {
		return false
	}
	p["text"] = text + "\n" + tag
	return true
}

// prependTextPart 并到 part 的文本开头（前面没有文本块时用）。
func prependTextPart(part any, tag string) bool {
	p, ok := part.(map[string]any)
	if !ok || strings.ToLower(str(p["type"])) != "text" {
		return false
	}
	text, ok := p["text"].(string)
	if !ok {
		return false
	}
	p["text"] = tag + "\n" + text
	return true
}
