// Package core 连接器的平台无关层：定义交互原语（入站消息、身份、回复、确认）
// 与消息处理管线。core 不 import 任何平台 SDK——平台差异全部收敛到适配层
// （连接器规范 docs/connector-spec.md §1 的分层约定；规划原则 P11）。
package core

// InboundMessage 平台无关的入站消息原语：适配层把平台事件翻译成它。
type InboundMessage struct {
	Platform  string // 平台标识，如 "feishu"（审计 chain.platform）
	ChatID    string // 会话标识（群聊/单聊），回复寻址用
	UserID    string // 平台用户标识（飞书 open_id 等）
	Text      string // 用户指令文本（已剥离 @ 提及等平台噪音）
	MessageID string // 平台消息 ID（去重与审计关联）
}

// Identity 平台用户身份。授权与安全规范 §8.1：identity = 平台 + 平台用户 ID 二元组。
type Identity struct {
	Platform string
	UserID   string
}

// Card 平台无关的卡片结构：适配层映射为平台原生组件（飞书消息卡片等）；
// 平台不支持卡片时降级为 Text（降级规则见规范 §5）。
type Card struct {
	Title   string
	Fields  []CardField
	Actions []CardAction // 空 = 纯展示卡片
}

// CardField 卡片里的一个键值行。
type CardField struct {
	Label string
	Value string
}

// CardAction 卡片按钮。Value 由适配层原样回传（确认回调的 request_id/令牌等）。
type CardAction struct {
	Label string            // 按钮文案（双语就绪）
	Kind  string            // "confirm"（主按钮）/ "reject"（危险样式）
	Value map[string]string // 回传载荷：request_id、token、decision
}

// Reply 连接器对一条入站消息的回复。Text 必有（降级兜底），Card 可选。
type Reply struct {
	Text string
	Card *Card
}
