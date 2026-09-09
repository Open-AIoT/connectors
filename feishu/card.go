package feishu

import (
	"encoding/json"

	"github.com/Open-AIoT/connectors/core"
)

// BuildCard 平台无关 core.Card → 飞书消息卡片 JSON（interactive 消息内容）。
// 按钮 value 原样携带 core.CardAction.Value（request_id/令牌/decision），
// 飞书点击后以卡片回调原样回传——一次性令牌由此闭环。
func BuildCard(c *core.Card) map[string]any {
	template := "blue"
	elements := []map[string]any{}
	for _, f := range c.Fields {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": "**" + f.Label + "：** " + f.Value,
			},
		})
	}
	if len(c.Actions) > 0 {
		template = "orange" // 待确认卡片用醒目色
		actions := []map[string]any{}
		for _, a := range c.Actions {
			btnType := "default"
			switch a.Kind {
			case "confirm":
				btnType = "primary"
			case "reject":
				btnType = "danger"
			}
			actions = append(actions, map[string]any{
				"tag":   "button",
				"text":  map[string]any{"tag": "plain_text", "content": a.Label},
				"type":  btnType,
				"value": a.Value,
			})
		}
		elements = append(elements, map[string]any{"tag": "action", "actions": actions})
	}
	return map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": template,
			"title":    map[string]any{"tag": "plain_text", "content": c.Title},
		},
		"elements": elements,
	}
}

// ReplyPayload core.Reply → 飞书发消息的 (msg_type, content JSON 字符串)。
// 有卡片发 interactive；无卡片发 text（降级规则：文本永远兜底）。
func ReplyPayload(r core.Reply) (msgType string, content string, err error) {
	if r.Card != nil {
		cardJSON, err := json.Marshal(BuildCard(r.Card))
		if err != nil {
			return "", "", err
		}
		return "interactive", string(cardJSON), nil
	}
	textJSON, err := json.Marshal(map[string]string{"text": r.Text})
	if err != nil {
		return "", "", err
	}
	return "text", string(textJSON), nil
}
