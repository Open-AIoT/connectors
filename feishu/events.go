// Package feishu 飞书适配层：把飞书开放平台的回调（消息事件、卡片按钮）
// 翻译成 core 的交互原语，把 core 的回复渲染成飞书消息/消息卡片。
// 平台差异全部在本包内收敛，core 不感知飞书。
package feishu

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Event 回调解析结果。Kind ∈ challenge / message / card_action。
type Event struct {
	Kind       string
	Challenge  string           // Kind=challenge：URL 验证回显
	Message    *InboundEventMsg // Kind=message
	CardAction *CardActionEvent // Kind=card_action
}

// InboundEventMsg 消息事件的关键字段。
type InboundEventMsg struct {
	ChatID    string
	MessageID string
	UserID    string // sender open_id
	Text      string // 已剥离 @ 提及
}

// CardActionEvent 卡片按钮回调的关键字段。
type CardActionEvent struct {
	OperatorID string            // 点击者 open_id
	Value      map[string]string // 按钮 value（request_id/token/decision）
}

// mentionRe @_user_N 提及占位（群聊里 @机器人 会带进文本）。
var mentionRe = regexp.MustCompile(`@_user_\d+\s*`)

// ParseEvent 解析（可能已解密的）回调体。token 为平台侧校验串
// （v2 在 header.token，v1 在顶层 token），校验责任在调用方（adapter）。
func ParseEvent(body []byte) (Event, string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return Event{}, "", errors.New("body is not a JSON object")
	}

	// URL 验证（v1/v2 均有 challenge + token 顶层字段）。
	if _, ok := raw["challenge"]; ok {
		var ev struct {
			Challenge string `json:"challenge"`
			Token     string `json:"token"`
		}
		if err := json.Unmarshal(body, &ev); err == nil && ev.Challenge != "" {
			return Event{Kind: "challenge", Challenge: ev.Challenge}, ev.Token, nil
		}
	}

	token := extractToken(raw)

	// v2 卡片按钮回调：header.event_type == "card.action.trigger"（事件订阅 2.0），
	// 或老版应用内直接回调（顶层 action 字段）。
	if _, hasAction := raw["action"]; hasAction {
		return parseCardAction(body, token)
	}
	if hdr, ok := raw["header"]; ok {
		var header struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal(hdr, &header); err != nil {
			return Event{}, "", errors.New("bad header")
		}
		switch header.EventType {
		case "im.message.receive_v1":
			return parseMessageV2(body, token)
		case "card.action.trigger":
			return parseCardAction(body, token)
		default:
			return Event{Kind: "ignored"}, token, nil
		}
	}
	return Event{Kind: "ignored"}, token, nil
}

// extractToken 取校验串：v2 header.token，v1 顶层 token。
func extractToken(raw map[string]json.RawMessage) string {
	if hdr, ok := raw["header"]; ok {
		var header struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(hdr, &header) == nil && header.Token != "" {
			return header.Token
		}
	}
	if t, ok := raw["token"]; ok {
		var s string
		if json.Unmarshal(t, &s) == nil {
			return s
		}
	}
	return ""
}

// parseMessageV2 im.message.receive_v1 → InboundEventMsg。
func parseMessageV2(body []byte, token string) (Event, string, error) {
	var ev struct {
		Event struct {
			Sender struct {
				SenderID struct {
					OpenID string `json:"open_id"`
				} `json:"sender_id"`
			} `json:"sender"`
			Message struct {
				MessageID   string `json:"message_id"`
				ChatID      string `json:"chat_id"`
				MessageType string `json:"message_type"`
				Content     string `json:"content"` // JSON 字符串：{"text":"..."}
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return Event{}, "", errors.New("bad message event")
	}
	if ev.Event.Message.MessageType != "text" {
		return Event{Kind: "ignored"}, token, nil // 一期只处理文本消息
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(ev.Event.Message.Content), &content); err != nil {
		return Event{}, "", errors.New("bad message content")
	}
	text := strings.TrimSpace(mentionRe.ReplaceAllString(content.Text, ""))
	if text == "" {
		return Event{Kind: "ignored"}, token, nil
	}
	return Event{
		Kind: "message",
		Message: &InboundEventMsg{
			ChatID:    ev.Event.Message.ChatID,
			MessageID: ev.Event.Message.MessageID,
			UserID:    ev.Event.Sender.SenderID.OpenID,
			Text:      text,
		},
	}, token, nil
}

// parseCardAction 卡片按钮回调（新老两种信封）。
func parseCardAction(body []byte, token string) (Event, string, error) {
	// 新信封（v2）：event.operator.open_id + event.action.value
	var v2 struct {
		Event struct {
			Operator struct {
				OpenID string `json:"open_id"`
			} `json:"operator"`
			Action struct {
				Value map[string]string `json:"value"`
			} `json:"action"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &v2); err == nil && v2.Event.Action.Value != nil {
		return Event{
			Kind: "card_action",
			CardAction: &CardActionEvent{
				OperatorID: v2.Event.Operator.OpenID,
				Value:      v2.Event.Action.Value,
			},
		}, token, nil
	}
	// 老信封：顶层 open_id + action.value
	var v1 struct {
		OpenID string `json:"open_id"`
		Action struct {
			Value map[string]string `json:"value"`
		} `json:"action"`
	}
	if err := json.Unmarshal(body, &v1); err != nil || v1.Action.Value == nil {
		return Event{}, "", errors.New("bad card action callback")
	}
	return Event{
		Kind: "card_action",
		CardAction: &CardActionEvent{
			OperatorID: v1.OpenID,
			Value:      v1.Action.Value,
		},
	}, token, nil
}
