package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/agent"
	"github.com/ctfagentpi/ctfagentpi/internal/api"
	"github.com/ctfagentpi/ctfagentpi/internal/appdata"
	"github.com/ctfagentpi/ctfagentpi/internal/envfile"
	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
)

// Run 按依赖顺序装配并运行独立控制平面。
// 任一关键依赖初始化失败都会终止启动，避免暴露能力不完整的 API。
func Run() error {
	// 首先加载 daemon 自己的环境配置，真实模型密钥不会进入 GUI 进程。
	loadedEnv, err := envfile.Load()
	if err != nil {
		return err
	}
	if loadedEnv != "" {
		slog.Info("loaded daemon configuration from .env", "path", loadedEnv)
	}
	// 解析本地数据路径、生成鉴权令牌，并写出供 GUI 发现的连接文件。
	paths, err := appdata.Resolve()
	if err != nil {
		return err
	}
	token, err := appdata.LoadOrCreateToken(paths.Token)
	if err != nil {
		return fmt.Errorf("load daemon token: %w", err)
	}
	address := appdata.Address()
	if err := appdata.WriteConnection(paths.Connection, address, token); err != nil {
		return fmt.Errorf("write daemon connection: %w", err)
	}
	// SQLite、Docker 客户端与模型网关均由 daemon 长期持有，并在退出时逆序关闭。
	store, err := storage.Open(paths.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	sandboxes, err := sandbox.New()
	if err != nil {
		return err
	}
	defer sandboxes.Close()
	gateway, err := modelgateway.New(modelgateway.Config{
		UpstreamBaseURL:    os.Getenv("CTF_UPSTREAM_MODEL_BASE_URL"),
		UpstreamAPIKey:     os.Getenv("CTF_UPSTREAM_MODEL_API_KEY"),
		ModelID:            os.Getenv("CTF_MODEL_ID"),
		IncludeStreamUsage: streamUsageEnabled(os.Getenv("CTF_MODEL_INCLUDE_STREAM_USAGE")),
	})
	if err != nil {
		return err
	}
	// 模型网关能观察到每次沙箱请求，因此由它统一向 SQLite 写入 Token 账本。
	gateway.SetUsageRecorder(store)
	// 容器通过 host.docker.internal 回连宿主机网关，端口必须与监听地址一致。
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid daemon address %q: %w", address, err)
	}
	hub := eventhub.New()
	agents := agent.NewService(store, hub, sandboxes, gateway, paths.Workspaces, "http://host.docker.internal:"+port)
	// HTTP 服务在后台运行；主协程同时等待服务错误或操作系统终止信号。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := api.New(address, token, store, hub, agents, sandboxes, gateway)
	// API 端点只请求主循环关闭，主循环负责统一释放 HTTP、SQLite 和 Docker 资源。
	server.SetShutdownRequest(stop)
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	// queued 表示用户明确请求过启动。daemon 重启后在模型网关可用时继续按
	// 当前并发上限调度，避免用户必须逐一重新点击每道排队题目。
	if gateway.Configured() {
		go func() { _ = agents.DispatchQueued(context.Background()) }()
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		// 最多等待十秒让在途 HTTP/WebSocket 请求结束并释放监听端口。
		slog.Info("shutting down CTF-BTFly daemon")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// streamUsageEnabled 解析宽松的布尔开关；除明确的否定值外默认启用，
// 以便从 OpenAI 兼容流式响应中获得准确用量。
func streamUsageEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
