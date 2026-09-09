package core

import (
	"context"
	"errors"
)

// Credential 一次 MCP 调用应使用的凭据——身份映射的输出侧
// （授权与安全规范 §8.1：identity → 授权策略 → 凭据）。
type Credential struct {
	Token string // MCP 接入层 Bearer token
	Note  string // 映射策略注记（进审计，如 "strategy=shared"）
}

// ErrUnmapped 平台用户无映射（fail-closed：管线据此拒绝，规范 §4.3）。
var ErrUnmapped = errors.New("no credential mapped for this platform identity")

// IdentityMapper 身份映射接口：平台用户身份 → 凭据。
// 三策略（规范 §4.2）：shared（一期已实现）/ table / oauth（接口预留）。
// 实现方可以替换为对接自有 IdP 的版本，管线不感知差异。
type IdentityMapper interface {
	Map(ctx context.Context, id Identity) (Credential, error)
}

// SharedCredentialMapper 策略 a：所有平台用户共用连接器凭据。
// 身份不参与授权决策，仅进审计（chain.user）——授权与安全规范 §3.1 G1 的
// 已知局限（凭据即平台）在此如实保留，适用只读或小团队演示场景。
type SharedCredentialMapper struct{ Token string }

// Map 实现 IdentityMapper。空 token 视为未配置，fail-closed。
func (m SharedCredentialMapper) Map(_ context.Context, _ Identity) (Credential, error) {
	if m.Token == "" {
		return Credential{}, ErrUnmapped
	}
	return Credential{Token: m.Token, Note: "strategy=shared"}, nil
}
