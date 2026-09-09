package feishu

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/Open-AIoT/connectors/core"
)

// Adapter 飞书适配层：HTTP 回调入口。
//   - POST {PathEvents} 事件订阅回调（URL 验证 + 消息事件）
//   - POST {PathCard}   消息卡片按钮回调（确认交互）
//
// 处理是同步的：演示链路单次 MCP 调用为毫秒级，远在飞书 3 秒回调预算内；
// 若未来接入慢速上游，应改为"先回 200 再异步处理"（一期取舍，见 README 已知差距）。
type Adapter struct {
	Pipeline          *core.Pipeline
	Client            *Client
	VerificationToken string // 事件订阅的 Verification Token（空 = 不校验，仅本地联调）
	EncryptKey        string // 事件订阅的 Encrypt Key（空 = 明文回调）
	PathEvents        string // 默认 /feishu/events
	PathCard          string // 默认 /feishu/card
}

// NewAdapter 构建 Adapter（路径取默认值）。
func NewAdapter(p *core.Pipeline, client *Client, verificationToken, encryptKey string) *Adapter {
	return &Adapter{
		Pipeline:          p,
		Client:            client,
		VerificationToken: verificationToken,
		EncryptKey:        encryptKey,
		PathEvents:        "/feishu/events",
		PathCard:          "/feishu/card",
	}
}

// ServeHTTP 实现 http.Handler。
func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case a.PathEvents:
		a.handleCallback(w, r, a.onMessage)
	case a.PathCard:
		a.handleCallback(w, r, a.onCardAction)
	default:
		http.NotFound(w, r)
	}
}

// handleCallback 公共回调骨架：读体 → 解密 → 校验 token → 解析 → 分发。
func (a *Adapter) handleCallback(w http.ResponseWriter, r *http.Request, dispatch func(context.Context, Event) any) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	// 加密回调：{"encrypt":"..."} → 解密后得到明文事件体。
	if a.EncryptKey != "" {
		var enc struct {
			Encrypt string `json:"encrypt"`
		}
		if err := json.Unmarshal(body, &enc); err != nil || enc.Encrypt == "" {
			http.Error(w, "expected encrypted callback", http.StatusBadRequest)
			return
		}
		body, err = Decrypt(a.EncryptKey, enc.Encrypt)
		if err != nil {
			slog.Warn("feishu decrypt failed", "err", err)
			http.Error(w, "decrypt failed", http.StatusBadRequest)
			return
		}
	}

	ev, token, err := ParseEvent(body)
	if err != nil {
		http.Error(w, "bad event: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Verification Token 校验（常数时间比较；配置了就必须匹配——fail-closed）。
	if a.VerificationToken != "" &&
		subtle.ConstantTimeCompare([]byte(a.VerificationToken), []byte(token)) != 1 {
		http.Error(w, "token mismatch", http.StatusUnauthorized)
		return
	}

	// URL 验证：回显 challenge。
	if ev.Kind == "challenge" {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": ev.Challenge})
		return
	}
	if ev.Kind == "ignored" {
		writeJSON(w, http.StatusOK, map[string]string{"msg": "ignored"})
		return
	}

	result := dispatch(r.Context(), ev)
	writeJSON(w, http.StatusOK, result)
}

// onMessage 消息事件 → core 管线 → 飞书回复。
func (a *Adapter) onMessage(ctx context.Context, ev Event) any {
	msg := core.InboundMessage{
		Platform:  "feishu",
		ChatID:    ev.Message.ChatID,
		UserID:    ev.Message.UserID,
		Text:      ev.Message.Text,
		MessageID: ev.Message.MessageID,
	}
	reply, err := a.Pipeline.HandleMessage(ctx, msg)
	if err != nil {
		reply = core.Reply{Text: "处理失败，请稍后重试。"}
	}
	if err := a.sendReply(ctx, msg.ChatID, reply); err != nil {
		slog.Warn("feishu send reply failed", "chat", msg.ChatID, "err", err)
	}
	return map[string]string{"msg": "ok"}
}

// onCardAction 卡片按钮回调 → 确认状态机 → 结果回复到原会话 + toast。
func (a *Adapter) onCardAction(ctx context.Context, ev Event) any {
	v := ev.CardAction.Value
	confirmer := core.Identity{Platform: "feishu", UserID: ev.CardAction.OperatorID}
	reply, chatID, err := a.Pipeline.HandleConfirmation(ctx, v["request_id"], v["token"], confirmer, v["decision"] == "approve")
	if err != nil {
		reply = core.Reply{Text: "处理失败，请稍后重试。"}
	}

	// 结果发回发起会话（管线从确认请求登记中取出）。
	if chatID != "" {
		if err := a.sendReply(ctx, chatID, reply); err != nil {
			slog.Warn("feishu send confirm result failed", "chat", chatID, "err", err)
		}
	}

	// toast 给点击者即时反馈（卡片本身一期不做局部更新，骨架取舍）。
	toastType := "info"
	if v["decision"] == "approve" {
		toastType = "success"
	}
	return map[string]any{"toast": map[string]string{"type": toastType, "content": firstLine(reply.Text)}}
}

// sendReply 发送 core.Reply（有卡片发 interactive，否则 text）。
func (a *Adapter) sendReply(ctx context.Context, chatID string, reply core.Reply) error {
	msgType, content, err := ReplyPayload(reply)
	if err != nil {
		return err
	}
	return a.Client.SendMessage(ctx, chatID, msgType, content)
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// firstLine 取首行（toast 文案宜短）。
func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
