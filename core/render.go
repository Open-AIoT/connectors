package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 渲染约定（规范 §6）：工具调用的 JSON 结果 → 平台无关 Reply。
// 渲染器只依赖结果形态约定（demo 后端三工具的返回结构 + problem JSON），
// 不含业务逻辑；未识别的结构原样透传（截断防爆）。

// maxRaw 未识别结果的透传上限。
const maxRaw = 2000

// RenderResult 工具调用结果 → Reply。isError 时按 problem JSON 呈现（规范 §7）。
func RenderResult(tool, text string, isError bool) Reply {
	if isError {
		return renderProblem(text)
	}
	switch tool {
	case "list_devices":
		return renderListDevices(text)
	case "get_device_overview":
		return renderOverview(text)
	case "control_device":
		return renderControl(text)
	default:
		return Reply{Text: truncate(text)}
	}
}

// problem 执行错误的 problem JSON 形态（与 MCP 绑定层 §8.2 同字段）。
type problem struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   int    `json:"code"`
	Detail string `json:"detail"`
}

// renderProblem 错误呈现：人话摘要 + 原始 code 保留（排障与审计对账）。
func renderProblem(text string) Reply {
	var p problem
	if err := json.Unmarshal([]byte(text), &p); err != nil || p.Code == 0 {
		return Reply{Text: "操作失败：" + truncate(text)}
	}
	return Reply{Text: fmt.Sprintf("操作失败：%s（错误码 %d）\n%s", p.Title, p.Code, p.Detail)}
}

// renderListDevices {"data":[{id,name,kind}...],"next_cursor":...} → 设备清单卡片。
func renderListDevices(text string) Reply {
	var out struct {
		Data []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"data"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return Reply{Text: truncate(text)}
	}
	if len(out.Data) == 0 {
		return Reply{Text: "没有匹配的设备。"}
	}
	var lines []string
	var fields []CardField
	for _, d := range out.Data {
		lines = append(lines, fmt.Sprintf("%d  %s（%s）", d.ID, d.Name, d.Kind))
		fields = append(fields, CardField{Label: fmt.Sprintf("%d", d.ID), Value: d.Name + "（" + d.Kind + "）"})
	}
	if out.NextCursor != "" {
		lines = append(lines, "（还有更多，本演示共两台设备）")
	}
	return Reply{Text: "设备列表：\n" + strings.Join(lines, "\n"),
		Card: &Card{Title: "设备列表", Fields: fields}}
}

// renderOverview {"data":{device:{...},state:{...}}} → 状态卡片。
func renderOverview(text string) Reply {
	var out struct {
		Data struct {
			Device struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
				Kind string `json:"kind"`
			} `json:"device"`
			State map[string]any `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return Reply{Text: truncate(text)}
	}
	fields := []CardField{{Label: "设备", Value: fmt.Sprintf("%d %s（%s）", out.Data.Device.ID, out.Data.Device.Name, out.Data.Device.Kind)}}
	var lines []string
	lines = append(lines, fields[0].Value)
	for k, v := range out.Data.State {
		line := fmt.Sprintf("%s: %v", k, v)
		lines = append(lines, line)
		fields = append(fields, CardField{Label: k, Value: fmt.Sprintf("%v", v)})
	}
	return Reply{Text: "设备状态：\n" + strings.Join(lines, "\n"),
		Card: &Card{Title: "设备状态", Fields: fields}}
}

// renderControl {"data":{device_id,sent,applied}} → 执行回执。
func renderControl(text string) Reply {
	var out struct {
		Data struct {
			DeviceID int64          `json:"device_id"`
			Sent     int            `json:"sent"`
			Applied  map[string]any `json:"applied"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return Reply{Text: truncate(text)}
	}
	applied, _ := json.Marshal(out.Data.Applied)
	return Reply{Text: fmt.Sprintf("命令已下发：设备 %d，受理 sent=%d，生效参数 %s", out.Data.DeviceID, out.Data.Sent, applied),
		Card: &Card{Title: "命令已下发", Fields: []CardField{
			{Label: "设备", Value: fmt.Sprintf("%d", out.Data.DeviceID)},
			{Label: "生效参数", Value: string(applied)},
		}}}
}

// truncate 截断到 maxRaw。
func truncate(s string) string {
	if len(s) > maxRaw {
		return s[:maxRaw] + "…（已截断）"
	}
	return s
}
