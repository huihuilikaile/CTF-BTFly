package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
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
	gateway, err := modelgateway.NewLivePool(modelPoolConfigFromEnv())
	if err != nil {
		return err
	}
	gateway.SetUsageRecorder(store)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid daemon address %q: %w", address, err)
	}
	hub := eventhub.New()
	agents := agent.NewService(store, hub, sandboxes, gateway, paths.Workspaces, "http://host.docker.internal:"+port)
	gateway.SetErrorReporter(agents)
	// HTTP 服务在后台运行；主协程同时等待服务错误或操作系统终止信号。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := api.New(address, token, store, hub, agents, sandboxes, gateway)
	// “重新读取并检测”原子切换到最新模型池；LivePool 会保留仍持有任务
	// Token 的旧池，因此新任务立即可选新模型，运行中任务也不会中断。
	server.SetModelConfigProbe(func(ctx context.Context, profile string) (modelgateway.ProbeStatus, error) {
		status, reloadErr := reloadLatestModelConfig(ctx, gateway, profile)
		if reloadErr == nil && gateway.Configured() {
			go func() { _ = agents.DispatchQueued(context.Background()) }()
		}
		return status, reloadErr
	})
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

// modelPoolConfigFromEnv keeps startup behavior on the process environment while
// sharing the exact multi-model parser used for a fresh .env connection probe.
func modelPoolConfigFromEnv() modelgateway.PoolConfig {
	return modelgateway.PoolConfigFromLookup(os.Getenv)
}

// reloadLatestModelConfig reads the current .env, atomically publishes the new
// pool, then probes the requested profile. LivePool keeps any old pool that
// still owns task-scoped tokens, so running tasks remain connected.
func reloadLatestModelConfig(ctx context.Context, gateway *modelgateway.LivePool, profile string) (modelgateway.ProbeStatus, error) {
	path, err := envfile.ConfigFile()
	if err != nil {
		return modelgateway.ProbeStatus{}, err
	}
	values, err := envfile.Read(path)
	if err != nil {
		return modelgateway.ProbeStatus{}, err
	}
	lookup := os.Getenv
	if _, statErr := os.Stat(path); statErr == nil {
		lookup = func(key string) string { return values[key] }
	} else if !os.IsNotExist(statErr) {
		return modelgateway.ProbeStatus{}, fmt.Errorf("inspect .env file %s: %w", path, statErr)
	}
	if err := gateway.Reload(modelgateway.PoolConfigFromLookup(lookup)); err != nil {
		return modelgateway.ProbeStatus{}, err
	}
	_ = gateway.Probe(ctx, profile)
	return gateway.ProbeStatus(profile), nil
}
