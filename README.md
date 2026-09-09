<p align="center">
  <img src="docs/assets/logo.png" alt="Open AIoT（开放AIoT）" width="120">
</p>

# Open AIoT（开放AIoT）平台连接器（connectors）

**把 AI 平台上的对话变成对真实设备的操作** —— 飞书等第三方平台的接入规范与参考连接器。

用户在飞书等平台上用自然语言管理、控制符合 Open AIoT（开放AIoT）标准的设备；连接器负责平台侧的消息/卡片交互、身份映射与危险操作确认，设备能力经由 Open AIoT 的 MCP 绑定层（[Open-AIoT/mcp](https://github.com/Open-AIoT/mcp)）到达。

> 项目状态：Phase 1 试点（飞书优先，已定决策）。参考连接器骨架已可本地跑通。

## 仓库结构

```
├── docs/connector-spec.md   # 连接器规范 v0.1 草案（配置模型/身份映射/确认/渲染/错误）
├── core/                    # 平台无关层：交互原语 + 管线（消息→意图→MCP 调用→渲染）
├── feishu/                  # 飞书适配层：事件回调、消息卡片、卡片按钮确认
├── cmd/connector-feishu/    # 连接器入口（mock REPL / 真实飞书两种模式）
├── internal/config/         # 配置加载
├── configs/                 # 配置示例（本地配置 *.local.yaml 已被 gitignore）
└── e2e/                     # 端到端测试（起真实 mcp 适配器 + 演示后端）
```

## 快速上手（本地 mock 模式，10 分钟）

不连飞书开放平台，在终端里体验完整链路：指令 → 意图解析 →（危险操作确认）→ 真实 MCP 调用 → 渲染回复。

前置：本仓与 [mcp 仓](https://github.com/Open-AIoT/mcp) 并列在同一目录下（如 `openaiot/connectors` 与 `openaiot/mcp`）。

```bash
# 1. 起演示后端（两台虚拟设备：客厅灯、卧室温湿度计）
cd ../mcp
go run ./examples/demo-backend          # 127.0.0.1:8080，token 默认 demo-backend-token

# 2. 另开终端：起 MCP 适配器（L1 绑定层参考实现）
cd ../mcp
cp configs/config.example.yaml configs/config.local.yaml
go run ./cmd/openaiot-mcp -config configs/config.local.yaml   # 127.0.0.1:8081/mcp

# 3. 另开终端：起连接器 mock 模式
cd ../connectors
cp configs/config.example.yaml configs/config.local.yaml      # feishu.mock: true
go run ./cmd/connector-feishu -config configs/config.local.yaml
```

在 REPL 里试：

```
> 设备列表                  # 只读：直接返回两台虚拟设备
> 开灯 1                    # 危险操作：返回确认请求（卡片以文本模拟）
  [按钮] 确认执行 → 确认 5f3a… 9c21…
> 确认 5f3a… 9c21…          # 模拟点击"确认执行"按钮 → 真实下发命令
> 设备状态 1                # 验证灯已开
```

`go test ./e2e/` 会自动化以上整条链路（含真实 MCP 协议往返）。

## 真实飞书接入

1. **创建企业自建应用**：登录[飞书开放平台](https://open.feishu.cn/) → 开发者后台 → 创建企业自建应用，启用「机器人」能力。
2. **取凭据**：应用详情页拿到 **App ID / App Secret**；事件订阅设置里自定 **Encrypt Key** 与 **Verification Token**。
3. **配置事件订阅**：请求地址填 `https://<你的公网地址>/feishu/events`（首次保存时飞书会做 URL 验证，连接器已实现）；订阅事件 `im.message.receive_v1`（接收消息）。
4. **配置卡片回调**：消息卡片请求网址填 `https://<你的公网地址>/feishu/card`。
5. **发布应用**：创建版本并提交发布（企业自建应用由管理员审核通过）。
6. **填配置启动**：把凭据填入 `configs/config.local.yaml` 的 `feishu.*`（**勿提交**，该文件已被 gitignore），`mock: false`，启动连接器。本地调试可用内网穿透（如 ngrok）获得公网地址。

在群里 @机器人 或单聊发送"设备列表"、"开灯 1"即可；危险操作会收到带「确认执行 / 取消」按钮的消息卡片。

## 测试

```bash
go build ./... && go vet ./... && go test ./...
```

- `core/`、`feishu/`：单元测试（飞书事件与 API 全部 mock）；
- `e2e/`：端到端测试——启动兄弟仓库 mcp 的演示后端与适配器（真实 MCP 协议往返），模拟飞书事件与卡片回调全流程。依赖 `../mcp`（可用 `OPENAIOT_MCP_DIR` 环境变量覆盖）；找不到时自动 skip。

## 安全要点（规范摘录）

- **确认必经**：`readOnlyHint=false` 的工具必须先经卡片按钮人工确认；确认令牌一次性、超时默认拒绝、被执行参数为登记时快照（防回调改参）。
- **凭据不出边界**：上游凭据只存连接器配置；平台消息/卡片/回调中不出现凭据本体。
- **身份映射**：一期为共享凭据策略（全体飞书用户共用凭据，用户身份进审计）；映射表 / OAuth 自助授权为接口预留（见 docs/connector-spec.md §4）。

## 已知取舍（一期骨架）

确认存内存（重启失效）；消息未去重；确认卡片不做局部更新（以新消息 + toast 反馈）；回调同步处理（飞书 3 秒预算内）；意图解析为机械命令解析器，LLM 意图识别经 `IntentParser` 接口插拔接入。完整清单见 docs/connector-spec.md §10。

## 声明

**Open AIoT（开放AIoT）**——"Open"即"开放"。本项目是芯步（ThingBoot）主导的开放 AIoT 标准与生态品牌，与 OpenAI 公司无任何关联。

## License

[Apache-2.0](LICENSE)

---

*Open AIoT platform connectors — bring device control into chat platforms like Feishu. Not affiliated with OpenAI.*
