// Package config 连接器配置加载（配置模型见 docs/connector-spec.md §3）。
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 连接器实例配置：一份配置 = 一个"平台租户 × 设备范围"的绑定。
type Config struct {
	Listen  string        `yaml:"listen"` // HTTP 监听地址（真实飞书模式）
	MCP     MCPConfig     `yaml:"mcp"`
	Confirm ConfirmConfig `yaml:"confirm"`
	Feishu  FeishuConfig  `yaml:"feishu"`
}

// MCPConfig 上游 MCP 接入层。一期身份映射为策略 a（shared）：
// token 即连接器共享凭据（规范 §4.2；table/oauth 为接口预留）。
type MCPConfig struct {
	Endpoint string `yaml:"endpoint"` // 如 http://127.0.0.1:8081/mcp
	Token    string `yaml:"token"`    // 接入层 Bearer token
}

// ConfirmConfig 确认策略。
type ConfirmConfig struct {
	Timeout time.Duration `yaml:"timeout"` // 确认超时（默认 2m；超时默认拒绝）
	Tools   []string      `yaml:"tools"`   // 追加的需确认工具（与标注推导取并集，只收紧不放宽）
}

// FeishuConfig 飞书适配层。
type FeishuConfig struct {
	Mock              bool   `yaml:"mock"`               // true = 本地 REPL，不连飞书（快速上手）
	AppID             string `yaml:"app_id"`             // 企业自建应用 App ID
	AppSecret         string `yaml:"app_secret"`         // App Secret（配置不入库属部署义务）
	EncryptKey        string `yaml:"encrypt_key"`        // 事件订阅 Encrypt Key（空 = 明文回调）
	VerificationToken string `yaml:"verification_token"` // 事件订阅 Verification Token
	APIBase           string `yaml:"api_base"`           // 默认 https://open.feishu.cn（测试可指 mock）
}

// Load 读配置并填充默认值、校验必填项。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Listen:  "127.0.0.1:8090",
		Feishu:  FeishuConfig{APIBase: "https://open.feishu.cn"},
		Confirm: ConfirmConfig{Timeout: 2 * time.Minute},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.MCP.Endpoint == "" {
		return nil, fmt.Errorf("%s: mcp.endpoint is required", path)
	}
	if cfg.MCP.Token == "" {
		return nil, fmt.Errorf("%s: mcp.token is required（身份映射一期为共享凭据，不得为空）", path)
	}
	if cfg.Confirm.Timeout <= 0 {
		return nil, fmt.Errorf("%s: confirm.timeout must be positive", path)
	}
	if !cfg.Feishu.Mock {
		if cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" {
			return nil, fmt.Errorf("%s: feishu.app_id / app_secret are required（或开启 mock 模式本地体验）", path)
		}
	}
	return cfg, nil
}
