package core

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// ConfirmRequest 确认请求（规范 §5：request_id 一次性令牌、超时默认拒绝）。
type ConfirmRequest struct {
	ID        string         // request_id：卡片回调关联键
	Token     string         // 一次性确认令牌（随卡片按钮 value 下发，回调时校验）
	ChatID    string         // 发起会话（确认结果回复寻址）
	Requester Identity       // 发起人
	Tool      string         // 待执行工具
	Args      map[string]any // 待执行参数（确认后原样下发，不接受回调侧改参）
	Summary   string         // 给人看的操作摘要（双语）
	ExpiresAt time.Time      // 超时时间；超时即默认拒绝
}

// ConfirmOutcome 确认结果。
type ConfirmOutcome int

const (
	OutcomeInvalid   ConfirmOutcome = iota // request_id/令牌不匹配或已消费
	OutcomeExpired                         // 超时（默认拒绝）
	OutcomeRejected                        // 人点了取消
	OutcomeConfirmed                       // 人点了确认
)

func (o ConfirmOutcome) String() string {
	switch o {
	case OutcomeExpired:
		return "expired"
	case OutcomeRejected:
		return "rejected"
	case OutcomeConfirmed:
		return "confirmed"
	default:
		return "invalid"
	}
}

// pendingConfirm 存内状态。
type pendingConfirm struct {
	req *ConfirmRequest
	// used 一次性语义：无论确认/拒绝/超时，消费一次即作废（防重放，规范 §5.4）。
	used bool
}

// ConfirmStore 确认请求存取（内存实现；持久化属实现域，接口即语义）。
type ConfirmStore struct {
	mu      sync.Mutex
	pending map[string]*pendingConfirm
	now     func() time.Time // 测试可注入
}

// NewConfirmStore 构建内存 ConfirmStore。
func NewConfirmStore() *ConfirmStore {
	return &ConfirmStore{pending: map[string]*pendingConfirm{}, now: time.Now}
}

// newID 生成随机十六进制串。
func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand 失败属系统级故障，无法安全继续
	}
	return hex.EncodeToString(b)
}

// New 登记一个待确认请求，返回含 request_id 与一次性令牌的请求对象。
func (s *ConfirmStore) New(chatID string, requester Identity, tool string, args map[string]any, summary string, timeout time.Duration) *ConfirmRequest {
	req := &ConfirmRequest{
		ID:        newID(8),
		Token:     newID(16),
		ChatID:    chatID,
		Requester: requester,
		Tool:      tool,
		Args:      args,
		Summary:   summary,
		ExpiresAt: s.now().Add(timeout),
	}
	s.mu.Lock()
	s.pending[req.ID] = &pendingConfirm{req: req}
	s.mu.Unlock()
	return req
}

// Resolve 消费一次确认回调：校验 request_id + 令牌 + 未过期，返回请求与结果。
// 任何失败路径都消费请求（防重放试探）；回调里附带的参数一律忽略——
// 被执行的只能是登记时的 Args（规范 §5.3）。
func (s *ConfirmStore) Resolve(id, token string, approve bool) (*ConfirmRequest, ConfirmOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok || p.used {
		return nil, OutcomeInvalid
	}
	p.used = true
	if subtle.ConstantTimeCompare([]byte(p.req.Token), []byte(token)) != 1 {
		return nil, OutcomeInvalid
	}
	if s.now().After(p.req.ExpiresAt) {
		return p.req, OutcomeExpired
	}
	if !approve {
		return p.req, OutcomeRejected
	}
	return p.req, OutcomeConfirmed
}

// String 调试视图。
func (r *ConfirmRequest) String() string {
	return fmt.Sprintf("confirm %s: %s by %s/%s (expires %s)", r.ID, r.Tool, r.Requester.Platform, r.Requester.UserID, r.ExpiresAt.Format(time.RFC3339))
}
