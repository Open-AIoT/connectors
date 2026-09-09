package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// stubCaller 打桩 MCPCaller：记录调用，返回预置响应。
type stubCaller struct {
	tools []ToolInfo
	calls []string // "tool argjson"
	reply string
	isErr bool
}

func (s *stubCaller) Tools(context.Context, Credential) ([]ToolInfo, error) { return s.tools, nil }
func (s *stubCaller) CallTool(_ context.Context, _ Credential, name string, args map[string]any) (string, bool, error) {
	s.calls = append(s.calls, name+" "+mustJSON(args))
	return s.reply, s.isErr, nil
}

func newTestPipeline(caller *stubCaller) *Pipeline {
	p := NewPipeline("feishu", CommandParser{}, SharedCredentialMapper{Token: "t"}, caller, nil)
	p.ConfirmTimeout = time.Minute
	return p
}

func msg(text string) InboundMessage {
	return InboundMessage{Platform: "feishu", ChatID: "chat-1", UserID: "ou_1", Text: text, MessageID: "m1"}
}

// TestPipelineReadOnly 只读工具直接执行，不进确认。
func TestPipelineReadOnly(t *testing.T) {
	caller := &stubCaller{
		tools: []ToolInfo{{Name: "list_devices", ReadOnlyHint: true}},
		reply: `{"data":[{"id":1,"name":"living-room-light","kind":"light"}],"next_cursor":""}`,
	}
	p := newTestPipeline(caller)
	if err := p.RefreshTools(context.Background(), Credential{Token: "t"}); err != nil {
		t.Fatal(err)
	}

	reply, err := p.HandleMessage(context.Background(), msg("设备列表"))
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || !strings.HasPrefix(caller.calls[0], "list_devices") {
		t.Fatalf("calls: %v", caller.calls)
	}
	if reply.Card == nil || !strings.Contains(reply.Text, "living-room-light") {
		t.Fatalf("reply: %+v", reply)
	}
	if len(reply.Card.Actions) != 0 {
		t.Fatal("read-only reply should have no confirm actions")
	}
}

// TestPipelineConfirmFlow 危险操作：先确认卡片 → 确认后执行；未确认前零调用。
func TestPipelineConfirmFlow(t *testing.T) {
	caller := &stubCaller{
		tools: []ToolInfo{{Name: "control_device", ReadOnlyHint: false}},
		reply: `{"data":{"device_id":1,"sent":1,"applied":{"power":1}}}`,
	}
	p := newTestPipeline(caller)
	if err := p.RefreshTools(context.Background(), Credential{Token: "t"}); err != nil {
		t.Fatal(err)
	}

	reply, err := p.HandleMessage(context.Background(), msg("开灯 1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("control must not execute before confirmation: %v", caller.calls)
	}
	if reply.Card == nil || len(reply.Card.Actions) != 2 {
		t.Fatalf("confirm card: %+v", reply)
	}
	value := reply.Card.Actions[0].Value
	if value["request_id"] == "" || value["token"] == "" || value["decision"] != "approve" {
		t.Fatalf("confirm action value: %v", value)
	}

	// 确认（同事代点）：执行，结果含回执。
	reply2, chatID, err := p.HandleConfirmation(context.Background(), value["request_id"], value["token"],
		Identity{Platform: "feishu", UserID: "ou_2"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if chatID != "chat-1" {
		t.Fatalf("chatID: %q", chatID)
	}
	if len(caller.calls) != 1 || !strings.Contains(caller.calls[0], "control_device") {
		t.Fatalf("calls: %v", caller.calls)
	}
	if !strings.Contains(reply2.Text, "命令已下发") {
		t.Fatalf("reply: %s", reply2.Text)
	}

	// 令牌一次性：重放无效。
	if _, _, err := p.HandleConfirmation(context.Background(), value["request_id"], value["token"],
		Identity{Platform: "feishu", UserID: "ou_2"}, true); err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("replay executed the tool again: %v", caller.calls)
	}
}

// TestPipelineReject 取消：不执行。
func TestPipelineReject(t *testing.T) {
	caller := &stubCaller{tools: []ToolInfo{{Name: "control_device", ReadOnlyHint: false}}}
	p := newTestPipeline(caller)
	_ = p.RefreshTools(context.Background(), Credential{Token: "t"})

	reply, _ := p.HandleMessage(context.Background(), msg("开灯 1"))
	value := reply.Card.Actions[1].Value // 取消按钮
	if value["decision"] != "reject" {
		t.Fatalf("reject action: %v", value)
	}
	reply2, _, err := p.HandleConfirmation(context.Background(), value["request_id"], value["token"],
		Identity{Platform: "feishu", UserID: "ou_1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("rejected op executed: %v", caller.calls)
	}
	if !strings.Contains(reply2.Text, "已取消") {
		t.Fatalf("reply: %s", reply2.Text)
	}
}

// TestPipelineUnmapped 身份映射失败：拒绝且不触达上游。
func TestPipelineUnmapped(t *testing.T) {
	caller := &stubCaller{}
	p := NewPipeline("feishu", CommandParser{}, SharedCredentialMapper{Token: ""}, caller, nil)
	reply, err := p.HandleMessage(context.Background(), msg("设备列表"))
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("unmapped identity reached upstream: %v", caller.calls)
	}
	if !strings.Contains(reply.Text, "授权") {
		t.Fatalf("reply: %s", reply.Text)
	}
}

// TestPipelineExtraConfirmTools 配置追加的确认工具与标注推导取并集（只收紧）。
func TestPipelineExtraConfirmTools(t *testing.T) {
	caller := &stubCaller{
		tools: []ToolInfo{{Name: "list_devices", ReadOnlyHint: true}},
		reply: `{"data":[],"next_cursor":""}`,
	}
	p := newTestPipeline(caller)
	p.ExtraConfirmTools = []string{"list_devices"} // 管理员把只读工具也纳入确认
	_ = p.RefreshTools(context.Background(), Credential{Token: "t"})

	reply, _ := p.HandleMessage(context.Background(), msg("设备列表"))
	if len(caller.calls) != 0 {
		t.Fatal("extra confirm tool executed without confirmation")
	}
	if reply.Card == nil || len(reply.Card.Actions) != 2 {
		t.Fatalf("reply: %+v", reply)
	}
}
