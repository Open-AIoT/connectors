package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Client 飞书开放平台 API 客户端（骨架：tenant_access_token 获取/缓存 + 发消息）。
// APIBase 可指向 mock 服务器（测试与本地联调）。
type Client struct {
	APIBase    string // 如 https://open.feishu.cn
	AppID      string
	AppSecret  string
	HTTPClient *http.Client // nil = http.DefaultClient

	mu        sync.Mutex
	token     string
	tokenExpr time.Time
}

// NewClient 构建 Client。
func NewClient(apiBase, appID, appSecret string) *Client {
	return &Client{APIBase: apiBase, AppID: appID, AppSecret: appSecret}
}

// apiError 飞书 API 错误（code != 0）。
type apiError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (e *apiError) Error() string { return fmt.Sprintf("feishu api error %d: %s", e.Code, e.Msg) }

// tenantAccessToken 取 tenant_access_token（缓存，提前 60 秒视为过期）。
// 文档：POST /open-apis/auth/v3/tenant_access_token/internal
func (c *Client) tenantAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.tokenExpr) {
		defer c.mu.Unlock()
		return c.token, nil
	}
	c.mu.Unlock()

	var out struct {
		apiError
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	err := c.do(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "",
		map[string]string{"app_id": c.AppID, "app_secret": c.AppSecret}, &out)
	if err != nil {
		return "", err
	}
	if out.Code != 0 {
		return "", &out.apiError
	}
	c.mu.Lock()
	c.token = out.TenantAccessToken
	c.tokenExpr = time.Now().Add(time.Duration(out.Expire)*time.Second - 60*time.Second)
	c.mu.Unlock()
	return out.TenantAccessToken, nil
}

// SendMessage 向会话发消息。文档：POST /open-apis/im/v1/messages?receive_id_type=chat_id
func (c *Client) SendMessage(ctx context.Context, chatID, msgType, content string) error {
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return err
	}
	var out apiError
	err = c.do(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=chat_id", token,
		map[string]string{"receive_id": chatID, "msg_type": msgType, "content": content}, &out)
	if err != nil {
		return err
	}
	if out.Code != 0 {
		return &out
	}
	return nil
}

// do 一次 API 调用（token 空串 = 无鉴权端点）。
func (c *Client) do(ctx context.Context, method, path, token string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.APIBase+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu api %s: http %d: %s", path, resp.StatusCode, truncateBody(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("feishu api %s: bad response: %w", path, err)
		}
	}
	return nil
}

// truncateBody 错误信息截断（响应体可能很长）。
func truncateBody(b []byte) string {
	if len(b) > 512 {
		return string(b[:512]) + "…"
	}
	return string(b)
}
