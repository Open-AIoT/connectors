package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandParser(t *testing.T) {
	p := CommandParser{}
	msg := func(text string) InboundMessage { return InboundMessage{Text: text} }

	cases := []struct {
		text     string
		wantTool string
		check    func(t *testing.T, in Intent)
	}{
		{"设备列表", "list_devices", nil},
		{"设备状态 1", "get_device_overview", func(t *testing.T, in Intent) {
			if in.Args["id"] != int64(1) {
				t.Fatalf("id: %v", in.Args["id"])
			}
		}},
		{"开灯 1", "control_device", func(t *testing.T, in Intent) {
			cmd := in.Args["command"].(map[string]any)
			if cmd["power"] != 1 {
				t.Fatalf("power: %v", cmd)
			}
		}},
		{"关灯 2", "control_device", func(t *testing.T, in Intent) {
			cmd := in.Args["command"].(map[string]any)
			if cmd["power"] != 0 {
				t.Fatalf("power: %v", cmd)
			}
		}},
		{"亮度 1 60", "control_device", func(t *testing.T, in Intent) {
			cmd := in.Args["command"].(map[string]any)
			if cmd["brightness"] != 60 {
				t.Fatalf("brightness: %v", cmd)
			}
		}},
		{"帮助", "", func(t *testing.T, in Intent) {
			if in.Reply == "" {
				t.Fatal("help reply empty")
			}
		}},
		{"开灯", "", nil},       // 缺 id → 用法提示
		{"亮度 1 200", "", nil}, // 超范围 → 用法提示
		{"随便说点啥", "", nil},    // 未识别 → 提示
		{"", "", nil},         // 空文本 → 帮助
	}
	for _, c := range cases {
		in, err := p.Parse(context.Background(), msg(c.text))
		if err != nil {
			t.Fatalf("%q: %v", c.text, err)
		}
		if in.Tool != c.wantTool {
			t.Fatalf("%q: tool %q, want %q", c.text, in.Tool, c.wantTool)
		}
		if c.wantTool == "" && in.Reply == "" {
			t.Fatalf("%q: expected direct reply", c.text)
		}
		if c.check != nil {
			c.check(t, in)
		}
	}
}

func TestConfirmStore(t *testing.T) {
	s := NewConfirmStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	idn := Identity{Platform: "feishu", UserID: "ou_1"}

	req := s.New("chat-1", idn, "control_device", map[string]any{"id": 1}, "summary", time.Minute)
	if req.ID == "" || req.Token == "" {
		t.Fatal("request id/token empty")
	}

	// 错误令牌：消费且无效。
	if _, oc := s.Resolve(req.ID, "wrong-token", true); oc != OutcomeInvalid {
		t.Fatalf("wrong token: %v", oc)
	}
	// 已消费：重放无效。
	if _, ok := s.pending[req.ID]; !ok {
		t.Fatal("pending missing")
	}
	if _, oc := s.Resolve(req.ID, req.Token, true); oc != OutcomeInvalid {
		t.Fatalf("replay: %v", oc)
	}

	// 正常确认。
	req2 := s.New("chat-1", idn, "control_device", map[string]any{"id": 1}, "s", time.Minute)
	got, oc := s.Resolve(req2.ID, req2.Token, true)
	if oc != OutcomeConfirmed || got.Tool != "control_device" {
		t.Fatalf("confirm: %v %v", oc, got)
	}

	// 拒绝。
	req3 := s.New("chat-1", idn, "control_device", nil, "s", time.Minute)
	if _, oc := s.Resolve(req3.ID, req3.Token, false); oc != OutcomeRejected {
		t.Fatalf("reject: %v", oc)
	}

	// 超时默认拒绝（确认方向也判超时）。
	req4 := s.New("chat-1", idn, "control_device", nil, "s", time.Minute)
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, oc := s.Resolve(req4.ID, req4.Token, true); oc != OutcomeExpired {
		t.Fatalf("expired: %v", oc)
	}

	// 未知 request_id。
	if _, oc := s.Resolve("nope", "nope", true); oc != OutcomeInvalid {
		t.Fatalf("unknown: %v", oc)
	}
}

func TestSharedCredentialMapper(t *testing.T) {
	if _, err := (SharedCredentialMapper{}).Map(context.Background(), Identity{}); err != ErrUnmapped {
		t.Fatalf("empty token should fail-closed: %v", err)
	}
	cred, err := (SharedCredentialMapper{Token: "t"}).Map(context.Background(), Identity{})
	if err != nil || cred.Token != "t" {
		t.Fatalf("map: %v %v", cred, err)
	}
}

func TestRenderResult(t *testing.T) {
	// 设备列表。
	r := RenderResult("list_devices", `{"data":[{"id":1,"name":"living-room-light","kind":"light"}],"next_cursor":""}`, false)
	if r.Card == nil || r.Card.Title != "设备列表" || len(r.Card.Fields) != 1 {
		t.Fatalf("list render: %+v", r)
	}

	// 控制回执。
	r = RenderResult("control_device", `{"data":{"device_id":1,"sent":1,"applied":{"power":1}}}`, false)
	if r.Card == nil || r.Card.Title != "命令已下发" {
		t.Fatalf("control render: %+v", r)
	}

	// 错误：problem JSON → 人话 + 错误码。
	r = RenderResult("get_device_overview", `{"type":"about:blank","title":"Not Found","status":404,"code":40401,"detail":"device 99 not found"}`, true)
	if r.Card != nil {
		t.Fatal("error reply should not carry card")
	}
	if want := "40401"; !strings.Contains(r.Text, want) {
		t.Fatalf("problem render missing code: %s", r.Text)
	}

	// 未识别工具：透传截断。
	r = RenderResult("unknown_tool", `{"whatever":true}`, false)
	if r.Text != `{"whatever":true}` {
		t.Fatalf("fallback: %s", r.Text)
	}
}
