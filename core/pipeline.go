package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Pipeline 平台无关处理管线（P10：薄适配层——只做四件事：
// 收消息、身份映射、调 MCP、渲染回复；授权判定与业务语义全部下沉到上游）。
//
// 主链路：消息 → 身份映射 → 意图解析 →（危险操作先确认）→ MCP tools/call → 渲染。
type Pipeline struct {
	Platform       string         // 本平台标识（审计 chain.platform）
	Parser         IntentParser   // 意图解析（可插拔）
	Mapper         IdentityMapper // 身份映射（可插拔，规范 §4）
	Caller         MCPCaller      // 上游 MCP 端点
	Confirm        *ConfirmStore  // 确认状态机
	ConfirmTimeout time.Duration  // 确认超时（超时默认拒绝）
	// ExtraConfirmTools 配置侧追加的需确认工具（与标注推导取并集——
	// 管理员可收紧、不得放宽，规范 §5.2）。
	ExtraConfirmTools []string

	Audit *slog.Logger // 审计输出（JSON 行；nil 静默）

	confirmTools map[string]bool // 需确认集合（RefreshTools 构建；nil=仅用配置列表）
}

// NewPipeline 装配管线。
func NewPipeline(platform string, parser IntentParser, mapper IdentityMapper, caller MCPCaller, audit *slog.Logger) *Pipeline {
	return &Pipeline{
		Platform:       platform,
		Parser:         parser,
		Mapper:         mapper,
		Caller:         caller,
		Confirm:        NewConfirmStore(),
		ConfirmTimeout: 2 * time.Minute,
		Audit:          audit,
	}
}

// RefreshTools 拉取工具清单并构建确认集合：readOnlyHint=false 即需确认
// （跟随上游安全分级标注），与配置追加列表取并集。启动期调用一次；
// 失败不致命——管线回落到仅配置列表（fail-safe 方向：可能漏确认 → 故
// 启动方应当把 RefreshTools 失败视为致命错误，见 cmd 入口）。
func (p *Pipeline) RefreshTools(ctx context.Context, cred Credential) error {
	tools, err := p.Caller.Tools(ctx, cred)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, t := range tools {
		if !t.ReadOnlyHint {
			set[t.Name] = true
		}
	}
	for _, name := range p.ExtraConfirmTools {
		set[name] = true
	}
	p.confirmTools = set
	return nil
}

// requiresConfirm 该工具是否需人在环路确认。
func (p *Pipeline) requiresConfirm(tool string) bool {
	if p.confirmTools != nil {
		return p.confirmTools[tool]
	}
	for _, name := range p.ExtraConfirmTools {
		if name == tool {
			return true
		}
	}
	return false
}

// HandleMessage 处理一条入站消息，返回回复。error 仅为管线内部故障；
// 用户可见的失败一律包装为 Reply 文本（规范 §7：错误如实呈现给用户）。
func (p *Pipeline) HandleMessage(ctx context.Context, msg InboundMessage) (Reply, error) {
	idn := Identity{Platform: msg.Platform, UserID: msg.UserID}

	// 1. 身份映射（fail-closed）。
	cred, err := p.Mapper.Map(ctx, idn)
	if err != nil {
		p.audit(idn, msg.ChatID, "", "", "denied", err.Error())
		return Reply{Text: "你尚未获得本机器人的使用授权，请联系管理员。"}, nil
	}

	// 2. 意图解析。
	intent, err := p.Parser.Parse(ctx, msg)
	if err != nil {
		return Reply{Text: "指令解析失败，发送“帮助”查看可用指令。"}, nil
	}
	if intent.Tool == "" {
		return Reply{Text: intent.Reply}, nil
	}

	// 3. 危险操作 → 先登记确认，回复确认卡片。
	if p.requiresConfirm(intent.Tool) {
		summary := fmt.Sprintf("%s %s", intent.Tool, mustJSON(intent.Args))
		req := p.Confirm.New(msg.ChatID, idn, intent.Tool, intent.Args, summary, p.ConfirmTimeout)
		p.audit(idn, msg.ChatID, intent.Tool, "", "confirm_requested", "request_id="+req.ID)
		return Reply{
			Text: fmt.Sprintf("该操作会改变设备状态，需要人工确认：\n%s\n有效期至 %s。",
				summary, req.ExpiresAt.Format("15:04:05")),
			Card: confirmCard(req),
		}, nil
	}

	// 4. 只读操作直接执行。
	return p.execute(ctx, cred, idn, msg.ChatID, intent.Tool, intent.Args, "")
}

// HandleConfirmation 处理确认回调（卡片按钮）。confirmer 为点击者身份，
// 允许与发起人不同（群聊场景同事代为确认），确认者身份进审计。
// 返回的 chatID 为发起会话（适配层据此把结果回复到原会话）。
func (p *Pipeline) HandleConfirmation(ctx context.Context, requestID, token string, confirmer Identity, approve bool) (Reply, string, error) {
	req, outcome := p.Confirm.Resolve(requestID, token, approve)
	switch outcome {
	case OutcomeInvalid:
		return Reply{Text: "确认链接无效或已使用。"}, "", nil
	case OutcomeExpired:
		p.audit(req.Requester, req.ChatID, req.Tool, "", "confirm_expired", "request_id="+requestID)
		return Reply{Text: "确认已超时，操作未执行（超时默认拒绝）。如需执行请重新发起指令。"}, req.ChatID, nil
	case OutcomeRejected:
		p.audit(confirmer, req.ChatID, req.Tool, "", "confirm_rejected", "request_id="+requestID)
		return Reply{Text: "已取消，操作未执行。"}, req.ChatID, nil
	}

	// 已确认：以发起人映射的凭据执行（确认只是人在环路环节，不放大权限）。
	cred, err := p.Mapper.Map(ctx, req.Requester)
	if err != nil {
		return Reply{Text: "发起人凭据已失效，操作未执行。"}, req.ChatID, nil
	}
	reply, err := p.execute(ctx, cred, req.Requester, req.ChatID, req.Tool, req.Args,
		fmt.Sprintf("confirmed_by=%s/%s request_id=%s", confirmer.Platform, confirmer.UserID, requestID))
	return reply, req.ChatID, err
}

// execute 调 MCP 工具并渲染结果。
func (p *Pipeline) execute(ctx context.Context, cred Credential, idn Identity, chatID, tool string, args map[string]any, note string) (Reply, error) {
	text, isError, err := p.Caller.CallTool(ctx, cred, tool, args)
	if err != nil {
		p.audit(idn, chatID, tool, "", "transport_error", note+" err="+err.Error())
		return Reply{Text: "上游服务暂时不可用，请稍后重试。"}, nil
	}
	result := "ok"
	if isError {
		result = "tool_error"
	}
	p.audit(idn, chatID, tool, cred.Note, result, note)
	return RenderResult(tool, text, isError), nil
}

// confirmCard 确认卡片：操作摘要 + 确认/取消按钮（value 携带 request_id 与令牌）。
func confirmCard(req *ConfirmRequest) *Card {
	return &Card{
		Title: "操作确认（会改变设备状态）",
		Fields: []CardField{
			{Label: "操作", Value: req.Tool},
			{Label: "参数", Value: mustJSON(req.Args)},
			{Label: "发起人", Value: req.Requester.UserID},
			{Label: "有效期至", Value: req.ExpiresAt.Format("2006-01-02 15:04:05")},
		},
		Actions: []CardAction{
			{Label: "确认执行", Kind: "confirm", Value: map[string]string{
				"request_id": req.ID, "token": req.Token, "decision": "approve"}},
			{Label: "取消", Kind: "reject", Value: map[string]string{
				"request_id": req.ID, "token": req.Token, "decision": "reject"}},
		},
	}
}

// audit 审计行（字段对齐授权与安全规范 §7.1：chain.platform/user/session/confirm）。
func (p *Pipeline) audit(idn Identity, chatID, tool, credNote, result, note string) {
	if p.Audit == nil {
		return
	}
	p.Audit.Info("connector_call",
		"chain.platform", p.Platform,
		"chain.user", idn.Platform+"/"+idn.UserID,
		"chain.session", chatID,
		"operation", tool,
		"credential", credNote, // 策略注记，绝不落凭据本体（规范 §7.3）
		"result", result,
		"note", note,
	)
}

// mustJSON 序列化（渲染用途；失败返回占位符）。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
