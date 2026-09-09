// Package e2e 端到端测试：模拟飞书消息事件 → core 管线 → 真实 MCP 协议调用
// （Open-AIoT/mcp 仓的适配器 + 演示后端，以子进程方式启动）→ 飞书回复/卡片。
//
// 依赖兄弟仓库 mcp：默认取 ../../mcp（测试 cwd 为 e2e 包目录），
// 可用环境变量 OPENAIOT_MCP_DIR 覆盖；找不到时 t.Skip（CI 无兄弟仓不红）。
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Open-AIoT/connectors/core"
	"github.com/Open-AIoT/connectors/feishu"
)

const (
	backendToken = "demo-backend-token"
	mcpToken     = "demo-client-token"
)

// mcpDir 定位兄弟仓库 mcp。
func mcpDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("OPENAIOT_MCP_DIR")
	if dir == "" {
		dir = "../../mcp"
	}
	abs, err := filepath.Abs(dir)
	if err != nil || !fileExists(filepath.Join(abs, "go.mod")) {
		t.Skipf("mcp 兄弟仓库不存在（%s）：跳过端到端测试（设 OPENAIOT_MCP_DIR 指定）", dir)
	}
	return abs
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// freePort 取一个空闲端口。
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
}

// buildBinary 编译 mcp 仓的子命令到临时目录。
func buildBinary(t *testing.T, dir, pkg, name string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, data)
	}
	return out
}

// start 起子进程并等待 ready 探测成功。子进程输出落临时日志，超时时 dump 便于排障。
func start(t *testing.T, bin string, args []string, ready func() bool, what string) {
	t.Helper()
	logFile, err := os.Create(filepath.Join(t.TempDir(), what+".log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })
	cmd := exec.Command(bin, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", what, err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	logFile.Sync()
	data, _ := os.ReadFile(logFile.Name())
	t.Fatalf("%s not ready in 10s, log:\n%s", what, data)
}

// mockFeishuAPI 假飞书 API（记录发出的消息）。
type mockFeishuAPI struct {
	mu       sync.Mutex
	messages []map[string]any
}

func (m *mockFeishuAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"fake-tat","expire":7200}`))
	})
	mux.HandleFunc("POST /open-apis/im/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.messages = append(m.messages, body)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	})
	return mux
}

func (m *mockFeishuAPI) last(t *testing.T) map[string]any {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.messages) == 0 {
		t.Fatal("no message sent")
	}
	return m.messages[len(m.messages)-1]
}

// TestEndToEnd 飞书事件 → core → 真实 MCP（适配器 + demo-backend）→ 回复渲染。
func TestEndToEnd(t *testing.T) {
	dir := mcpDir(t)

	// ---- 起 demo-backend 与 MCP 适配器 ----
	backendPort := freePort(t)
	adapterPort := freePort(t)
	backendBin := buildBinary(t, dir, "./examples/demo-backend", "demo-backend")
	adapterBin := buildBinary(t, dir, "./cmd/openaiot-mcp", "openaiot-mcp")

	backendAddr := "127.0.0.1:" + backendPort
	start(t, backendBin, []string{"-addr", backendAddr, "-token", backendToken}, func() bool {
		resp, err := http.Get("http://" + backendAddr + "/v1/tools/openai.json")
		if err == nil {
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}
		return false
	}, "demo-backend")

	adapterCfg := fmt.Sprintf(`listen: "127.0.0.1:%s"
backend:
  base_url: "http://%s"
  auth_type: "bearer"
  token: "%s"
clients:
  - name: "e2e"
    token: "%s"
`, adapterPort, backendAddr, backendToken, mcpToken)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(adapterCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	mcpEndpoint := "http://127.0.0.1:" + adapterPort + "/mcp"
	start(t, adapterBin, []string{"-config", cfgPath}, func() bool {
		req, _ := http.NewRequest(http.MethodPost, mcpEndpoint,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}`))
		req.Header.Set("Content-Type", "application/json")
		// MCP Streamable HTTP 要求 Accept 同时含两种类型（SDK 服务端强制校验）。
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+mcpToken)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}
		return false
	}, "openaiot-mcp")

	// ---- 装配连接器（真 MCP 客户端 + 假飞书 API）----
	api := &mockFeishuAPI{}
	apiSrv := httptest.NewServer(api.handler())
	defer apiSrv.Close()

	caller := core.NewSDKCaller(mcpEndpoint)
	defer caller.Close()
	pipe := core.NewPipeline("feishu", core.CommandParser{}, core.SharedCredentialMapper{Token: mcpToken}, caller, nil)
	pipe.ConfirmTimeout = time.Minute
	if err := pipe.RefreshTools(context.Background(), core.Credential{Token: mcpToken}); err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}
	adapter := feishu.NewAdapter(pipe, feishu.NewClient(apiSrv.URL, "cli_x", "secret_x"), "vtok", "")
	cbSrv := httptest.NewServer(adapter)
	defer cbSrv.Close()

	postEvent := func(path, body string) (int, map[string]any) {
		t.Helper()
		resp, err := http.Post(cbSrv.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	message := func(text string) string {
		content, _ := json.Marshal(map[string]string{"text": text})
		ev := map[string]any{
			"schema": "2.0",
			"header": map[string]string{"event_type": "im.message.receive_v1", "token": "vtok"},
			"event": map[string]any{
				"sender":  map[string]any{"sender_id": map[string]string{"open_id": "ou_alice"}},
				"message": map[string]any{"message_id": "om_" + text, "chat_id": "oc_1", "message_type": "text", "content": string(content)},
			},
		}
		raw, _ := json.Marshal(ev)
		return string(raw)
	}

	// 1. 只读链路："设备列表" → 真实 MCP 调用 → 回复含两台虚拟设备。
	status, _ := postEvent("/feishu/events", message("设备列表"))
	if status != http.StatusOK {
		t.Fatalf("events: %d", status)
	}
	content, _ := api.last(t)["content"].(string)
	if !strings.Contains(content, "living-room-light") || !strings.Contains(content, "bedroom-thermometer") {
		t.Fatalf("list reply: %s", content)
	}

	// 2. 危险操作链路："开灯 1" → 确认卡片（未执行）→ 卡片回调确认 → 真实执行。
	status, _ = postEvent("/feishu/events", message("开灯 1"))
	if status != http.StatusOK {
		t.Fatalf("events: %d", status)
	}
	cardStr, _ := api.last(t)["content"].(string)
	var card map[string]any
	if err := json.Unmarshal([]byte(cardStr), &card); err != nil {
		t.Fatalf("confirm card not JSON: %v", err)
	}
	var value map[string]any
	for _, el := range card["elements"].([]any) {
		e := el.(map[string]any)
		if e["tag"] != "action" {
			continue
		}
		for _, a := range e["actions"].([]any) {
			v := a.(map[string]any)["value"].(map[string]any)
			if v["decision"] == "approve" {
				value = v
			}
		}
	}
	if value == nil {
		t.Fatalf("no approve button: %s", cardStr)
	}

	// 确认前设备仍是关的（经真实 MCP 验证）。
	assertPower(t, caller, false)

	cbBody, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]string{"event_type": "card.action.trigger", "token": "vtok"},
		"event": map[string]any{
			"operator": map[string]string{"open_id": "ou_bob"},
			"action":   map[string]any{"value": value},
		},
	})
	status, out := postEvent("/feishu/card", string(cbBody))
	if status != http.StatusOK {
		t.Fatalf("card callback: %d", status)
	}
	if toast := out["toast"].(map[string]any); toast["type"] != "success" {
		t.Fatalf("toast: %v", out)
	}

	// 执行结果发回原会话。
	content, _ = api.last(t)["content"].(string)
	if !strings.Contains(content, "命令已下发") {
		t.Fatalf("result reply: %s", content)
	}
	// 设备真实状态已变（经真实 MCP 验证）。
	assertPower(t, caller, true)
}

// assertPower 经真实 MCP 调用查灯的开光状态。
func assertPower(t *testing.T, caller *core.SDKCaller, want bool) {
	t.Helper()
	text, isErr, err := caller.CallTool(context.Background(), core.Credential{Token: mcpToken},
		"get_device_overview", map[string]any{"id": 1})
	if err != nil || isErr {
		t.Fatalf("overview: %v isErr=%v %s", err, isErr, text)
	}
	var out struct {
		Data struct {
			State struct {
				Power bool `json:"power"`
			} `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("overview not JSON: %v", err)
	}
	if out.Data.State.Power != want {
		t.Fatalf("power=%v, want %v: %s", out.Data.State.Power, want, text)
	}
}
