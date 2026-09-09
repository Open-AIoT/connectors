// connector-feishu 飞书参考连接器入口。
//
// 两种运行模式：
//   - mock 模式（feishu.mock: true）：本地 REPL，不连飞书——在终端里体验
//     完整管线（指令 → 意图 → 确认 → MCP 调用 → 渲染），用于快速上手；
//   - 真实模式：起 HTTP 服务接收飞书事件订阅/卡片回调（接入步骤见 README）。
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Open-AIoT/connectors/core"
	"github.com/Open-AIoT/connectors/feishu"
	"github.com/Open-AIoT/connectors/internal/config"
)

func main() {
	cfgPath := flag.String("config", "configs/config.local.yaml", "config file path")
	flag.Parse()
	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	// 审计日志：JSON 行进 stderr（对齐 mcp 仓的做法；字段见 core pipeline）。
	audit := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	caller := core.NewSDKCaller(cfg.MCP.Endpoint)
	defer caller.Close()
	mapper := core.SharedCredentialMapper{Token: cfg.MCP.Token} // 策略 a：一期唯一实现

	pipe := core.NewPipeline("feishu", core.CommandParser{}, mapper, caller, audit)
	pipe.ConfirmTimeout = cfg.Confirm.Timeout
	pipe.ExtraConfirmTools = cfg.Confirm.Tools

	// 启动期拉工具清单构建确认集合：失败即拒绝启动——
	// 标注拿不到就意味着危险操作可能漏确认，带病上线不可接受（P5）。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = pipe.RefreshTools(ctx, core.Credential{Token: cfg.MCP.Token})
	cancel()
	if err != nil {
		return fmt.Errorf("startup tools/list failed (mcp %s): %w", cfg.MCP.Endpoint, err)
	}

	if cfg.Feishu.Mock {
		return runREPL(pipe)
	}

	client := feishu.NewClient(cfg.Feishu.APIBase, cfg.Feishu.AppID, cfg.Feishu.AppSecret)
	adapter := feishu.NewAdapter(pipe, client, cfg.Feishu.VerificationToken, cfg.Feishu.EncryptKey)

	httpSrv := &http.Server{Addr: cfg.Listen, Handler: adapter}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	slog.Info("connector-feishu listening", "addr", cfg.Listen, "mcp", cfg.MCP.Endpoint,
		"events", "http://<host>"+adapter.PathEvents, "card", "http://<host>"+adapter.PathCard)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// runREPL mock 模式：终端里模拟飞书用户。
// 指令即飞书里会发的文本；确认卡片打印为文本，用“确认/拒绝 <request_id> <token>”模拟点击按钮。
func runREPL(pipe *core.Pipeline) error {
	fmt.Println("=== connector-feishu mock 模式（本地模拟飞书，不连开放平台）===")
	fmt.Println("发送“帮助”查看指令；危险操作会要求确认，用“确认 <request_id> <token>”模拟点击按钮。")
	fmt.Println("输入 quit 退出。")

	scanner := bufio.NewScanner(os.Stdin)
	msgSeq := 0
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "quit" {
			return nil
		}
		msgSeq++

		// 确认/拒绝指令模拟卡片按钮回调。
		if fields := strings.Fields(line); len(fields) == 3 && (fields[0] == "确认" || fields[0] == "拒绝") {
			reply, _, err := pipe.HandleConfirmation(context.Background(), fields[1], fields[2],
				core.Identity{Platform: "feishu", UserID: "mock-user"}, fields[0] == "确认")
			if err != nil {
				fmt.Println("错误:", err)
				continue
			}
			printReply(reply)
			continue
		}

		reply, err := pipe.HandleMessage(context.Background(), core.InboundMessage{
			Platform:  "feishu",
			ChatID:    "mock-chat",
			UserID:    "mock-user",
			Text:      line,
			MessageID: fmt.Sprintf("mock-%d", msgSeq),
		})
		if err != nil {
			fmt.Println("错误:", err)
			continue
		}
		printReply(reply)
	}
}

// printReply 文本化打印回复（卡片渲染为可读文本，request_id/token 原样露出供确认指令使用）。
func printReply(r core.Reply) {
	fmt.Println(r.Text)
	if r.Card == nil {
		return
	}
	for _, a := range r.Card.Actions {
		verb := "拒绝"
		if a.Kind == "confirm" {
			verb = "确认"
		}
		fmt.Printf("  [按钮] %s → %s %s %s\n", a.Label, verb, a.Value["request_id"], a.Value["token"])
	}
}
