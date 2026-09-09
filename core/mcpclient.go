package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolInfo 工具元信息。确认策略依据：ReadOnlyHint=false 的工具即需确认
// （规范 §5.2：默认跟随 L0 经绑定层渲染的安全分级标注）。
type ToolInfo struct {
	Name         string
	ReadOnlyHint bool
}

// MCPCaller core 对上游 MCP 端点的依赖接口。测试可打桩；
// 生产实现为 SDKCaller（官方 MCP Go SDK，Streamable HTTP）。
type MCPCaller interface {
	// Tools 以 cred 凭据拉取工具清单及标注（用于构建确认策略）。
	Tools(ctx context.Context, cred Credential) ([]ToolInfo, error)
	// CallTool 以 cred 凭据调用工具，返回首个 text 块与 isError 标志。
	CallTool(ctx context.Context, cred Credential, name string, args map[string]any) (text string, isError bool, err error)
}

// SDKCaller 基于官方 MCP Go SDK 的 MCPCaller：Streamable HTTP + Bearer。
// 会话按凭据缓存复用（同一 token 一个 session）；工具清单按凭据缓存。
type SDKCaller struct {
	Endpoint string // 如 http://127.0.0.1:8081/mcp

	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
	tools    map[string][]ToolInfo
}

// NewSDKCaller 构建 SDKCaller。
func NewSDKCaller(endpoint string) *SDKCaller {
	return &SDKCaller{
		Endpoint: endpoint,
		sessions: map[string]*mcp.ClientSession{},
		tools:    map[string][]ToolInfo{},
	}
}

// bearerTransport 给每个请求加 Authorization: Bearer 头。
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

// session 取（或建）该凭据对应的会话。
func (c *SDKCaller) session(ctx context.Context, token string) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.sessions[token]; ok {
		return s, nil
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "openaiot-connector", Version: "0.1.0-dev"}, nil)
	s, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             c.Endpoint,
		HTTPClient:           &http.Client{Transport: bearerTransport{http.DefaultTransport, token}},
		MaxRetries:           -1, // 禁自动重试：错误立即上浮，由渲染层如实呈现
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp connect %s: %w", c.Endpoint, err)
	}
	c.sessions[token] = s
	return s, nil
}

// Tools 实现 MCPCaller（首次拉取后按凭据缓存；工具集变更需重启进程，一期取舍）。
func (c *SDKCaller) Tools(ctx context.Context, cred Credential) ([]ToolInfo, error) {
	c.mu.Lock()
	cached, ok := c.tools[cred.Token]
	c.mu.Unlock()
	if ok {
		return cached, nil
	}
	s, err := c.session(ctx, cred.Token)
	if err != nil {
		return nil, err
	}
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list: %w", err)
	}
	infos := make([]ToolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		info := ToolInfo{Name: t.Name, ReadOnlyHint: true}
		if t.Annotations != nil {
			info.ReadOnlyHint = t.Annotations.ReadOnlyHint
		}
		infos = append(infos, info)
	}
	c.mu.Lock()
	c.tools[cred.Token] = infos
	c.mu.Unlock()
	return infos, nil
}

// CallTool 实现 MCPCaller。
func (c *SDKCaller) CallTool(ctx context.Context, cred Credential, name string, args map[string]any) (string, bool, error) {
	s, err := c.session(ctx, cred.Token)
	if err != nil {
		return "", false, err
	}
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", false, fmt.Errorf("mcp tools/call %s: %w", name, err)
	}
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			return tc.Text, res.IsError, nil
		}
	}
	// 无 text 块：结构化内容序列化兜底（渲染层只认 text，一期取舍）。
	raw, _ := json.Marshal(res.StructuredContent)
	return string(raw), res.IsError, nil
}

// Close 关闭全部会话。
func (c *SDKCaller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sessions {
		_ = s.Close()
	}
	c.sessions = map[string]*mcp.ClientSession{}
}
