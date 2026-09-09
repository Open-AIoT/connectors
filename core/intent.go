package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Intent 意图解析结果：二选一——
//   - Tool 非空：一次工具调用（Args 为工具参数）；
//   - Tool 为空：直接回复 Reply 文本（帮助/用法提示，不触达设备）。
type Intent struct {
	Tool  string
	Args  map[string]any
	Reply string
}

// IntentParser 意图解析接口（可插拔，规范 §1.3）。
// 一期内置 CommandParser；真实 LLM 意图识别由平台侧 AI 能力（飞书智能伙伴等）
// 或后续接入的模型负责——届时实现同一接口即可，管线不变。
type IntentParser interface {
	Parse(ctx context.Context, msg InboundMessage) (Intent, error)
}

// CommandParser 演示用命令解析器：固定中文指令 → 工具调用。
// 故意保持机械、可预测——演示环境里没有模型，意图解析不该引入不确定性。
//
// 支持的指令：
//
//	设备列表            → list_devices {}
//	设备状态 <id>       → get_device_overview {id}
//	开灯 <id>           → control_device {id, command:{power:1}}
//	关灯 <id>           → control_device {id, command:{power:0}}
//	亮度 <id> <1-100>   → control_device {id, command:{brightness:n}}
//	帮助                → 帮助文本
type CommandParser struct{}

// helpText 帮助与用法提示（双语描述由上游工具描述承担，此处面向平台用户）。
const helpText = `可用指令：
  设备列表            列出全部设备
  设备状态 <id>       查看单台设备状态
  开灯 <id>           打开指定灯（需确认）
  关灯 <id>           关闭指定灯（需确认）
  亮度 <id> <1-100>   设置灯亮度（需确认）
设备 id 可用“设备列表”查询。`

// Parse 实现 IntentParser。
func (CommandParser) Parse(_ context.Context, msg InboundMessage) (Intent, error) {
	fields := strings.Fields(strings.TrimSpace(msg.Text))
	if len(fields) == 0 {
		return Intent{Reply: helpText}, nil
	}
	switch fields[0] {
	case "设备列表":
		return Intent{Tool: "list_devices", Args: map[string]any{}}, nil
	case "设备状态":
		id, err := parseID(fields, 1)
		if err != nil {
			return Intent{Reply: "用法：设备状态 <id>，例：设备状态 1"}, nil
		}
		return Intent{Tool: "get_device_overview", Args: map[string]any{"id": id}}, nil
	case "开灯", "关灯":
		id, err := parseID(fields, 1)
		if err != nil {
			return Intent{Reply: "用法：" + fields[0] + " <id>，例：" + fields[0] + " 1"}, nil
		}
		power := 0
		if fields[0] == "开灯" {
			power = 1
		}
		return Intent{Tool: "control_device", Args: map[string]any{
			"id": id, "command": map[string]any{"power": power},
		}}, nil
	case "亮度":
		id, err := parseID(fields, 1)
		if err != nil {
			return Intent{Reply: "用法：亮度 <id> <1-100>，例：亮度 1 60"}, nil
		}
		if len(fields) < 3 {
			return Intent{Reply: "用法：亮度 <id> <1-100>，例：亮度 1 60"}, nil
		}
		n, err := strconv.Atoi(fields[2])
		if err != nil || n < 1 || n > 100 {
			return Intent{Reply: "亮度取值 1-100，例：亮度 1 60"}, nil
		}
		return Intent{Tool: "control_device", Args: map[string]any{
			"id": id, "command": map[string]any{"brightness": n},
		}}, nil
	case "帮助", "help":
		return Intent{Reply: helpText}, nil
	default:
		return Intent{Reply: fmt.Sprintf("无法识别的指令 %q，发送“帮助”查看可用指令。", fields[0])}, nil
	}
}

// parseID 取 fields[i] 为设备 ID（正整数）。
func parseID(fields []string, i int) (int64, error) {
	if len(fields) <= i {
		return 0, fmt.Errorf("missing id")
	}
	id, err := strconv.ParseInt(fields[i], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("bad id %q", fields[i])
	}
	return id, nil
}
