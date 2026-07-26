package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// imageVersion 必须与 images/build.ps1 构建标签保持一致。
const imageVersion = "0.1.0"

// images 将受校验的题型映射到对应的专项 Pi 镜像。
var images = map[platform.Category]string{
	platform.CategoryWeb:       "ctf-agent-pi-web:" + imageVersion,
	platform.CategoryCrypto:    "ctf-agent-pi-crypto:" + imageVersion,
	platform.CategoryPwn:       "ctf-agent-pi-pwn:" + imageVersion,
	platform.CategoryReverse:   "ctf-agent-pi-reverse:" + imageVersion,
	platform.CategoryForensics: "ctf-agent-pi-forensics:" + imageVersion,
	platform.CategoryMisc:      "ctf-agent-pi-misc:" + imageVersion,
}

// ModelAccess 是发给单个沙箱的短期模型网关配置，不包含真实上游 API Key。
type ModelAccess struct {
	BaseURL        string
	Token          string
	ModelID        string
	SupportsImages bool
}

// StartConfig 汇总创建沙箱所需的题目、工作区、模型访问与资源上限。
type StartConfig struct {
	Task      platform.Task
	Workspace string
	Prompt    string
	Model     ModelAccess
	MaxMemory int64
	MaxCPUs   int64
	MaxPIDs   int64
	Network   bool
}

// Session 表示一个已附加 stdin/stdout/stderr 的 Pi RPC 容器会话。
type Session struct {
	ContainerID string
	Runtime     string
	Stdout      io.Reader
	Stderr      io.Reader
	input       io.Writer
	close       func()
}

// Send 将一个 RPC 对象编码为单行 JSON，并写入 Pi 的标准输入。
func (s *Session) Send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.input.Write(data)
	return err
}

// Health 是返回给 API/前端的 Docker 可用性及隔离运行时探测结果。
type Health struct {
	Available         bool     `json:"available"`
	ServerVersion     string   `json:"serverVersion,omitempty"`
	Runtimes          []string `json:"runtimes"`
	NormalRuntime     string   `json:"normalRuntime"`
	PwnRuntime        string   `json:"pwnRuntime"`
	IsolationWarnings []string `json:"isolationWarnings"`
}

// Manager 持有 Docker 客户端以及“任务 ID → 活跃会话”的进程内索引。
type Manager struct {
	client   *client.Client
	mu       sync.Mutex
	sessions map[string]*Session
}

// New 使用环境变量中的 Docker 配置创建支持 API 版本协商的客户端。
func New() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create Docker client: %w", err)
	}
	return &Manager{client: cli, sessions: make(map[string]*Session)}, nil
}

// Close 释放 Docker API 客户端。
func (m *Manager) Close() error { return m.client.Close() }

// ImageFor 返回题型对应的固定版本镜像名。
func ImageFor(category platform.Category) string { return images[category] }

// Health 查询 Docker Engine，并分别为普通题与 Pwn 题选择最佳隔离运行时。
func (m *Manager) Health(ctx context.Context) Health {
	info, err := m.client.Info(ctx)
	if err != nil {
		return Health{Available: false, IsolationWarnings: []string{err.Error()}}
	}

	// 排序使 API 输出稳定，便于前端展示和问题排查。
	runtimes := make([]string, 0, len(info.Runtimes))
	for name := range info.Runtimes {
		runtimes = append(runtimes, name)
	}
	sort.Strings(runtimes)
	normal := pickRuntime(runtimes, "runsc", "io.containerd.runsc.v1", "runc")
	pwn := pickRuntime(runtimes, "kata", "io.containerd.kata.v2", "runc")

	// 回退 runc 时明确发出警告：资源限制仍在，但隔离强度低于 gVisor/Kata。
	warnings := make([]string, 0, 2)
	if normal == "runc" {
		warnings = append(warnings, "gVisor/runsc is unavailable; normal tasks use constrained runc in development mode")
	}
	if pwn == "runc" {
		warnings = append(warnings, "Kata runtime is unavailable; Pwn tasks use runc + SYS_PTRACE in development mode")
	}
	return Health{
		Available: true, ServerVersion: info.ServerVersion, Runtimes: runtimes,
		NormalRuntime: normal, PwnRuntime: pwn, IsolationWarnings: warnings,
	}
}

// Start 创建、附加并启动一个专项 Pi RPC 容器，随后发送初始解题 Prompt。
func (m *Manager) Start(ctx context.Context, cfg StartConfig) (*Session, error) {
	// 工作区是唯一挂载到容器的宿主机目录，先规范化为绝对路径。
	if err := os.MkdirAll(cfg.Workspace, 0o700); err != nil {
		return nil, fmt.Errorf("create task workspace: %w", err)
	}
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}

	// 调用方未覆盖时使用 4 GiB、4 CPU、512 PID 的默认资源边界。
	if cfg.MaxMemory == 0 {
		cfg.MaxMemory = 4 << 30
	}
	if cfg.MaxCPUs == 0 {
		cfg.MaxCPUs = 4
	}
	if cfg.MaxPIDs == 0 {
		cfg.MaxPIDs = 512
	}

	health := m.Health(ctx)
	if !health.Available {
		return nil, fmt.Errorf("Docker is unavailable: %s", strings.Join(health.IsolationWarnings, "; "))
	}

	// 普通题优先 gVisor，Pwn 因调试/利用需求优先 Kata；镜像必须已在本地存在。
	runtimeName := health.NormalRuntime
	if cfg.Task.Category == platform.CategoryPwn {
		runtimeName = health.PwnRuntime
	}
	imageName := ImageFor(cfg.Task.Category)
	if _, _, err := m.client.ImageInspectWithRaw(ctx, imageName); err != nil {
		return nil, fmt.Errorf("required image %s is unavailable: %w", imageName, err)
	}

	// 容器只接收题目级短期 Token。真实供应商密钥始终留在宿主 daemon。
	containerConfig := &container.Config{
		Image:      imageName,
		User:       "root",
		WorkingDir: "/workspace",
		Cmd: []string{
			"pi", "--mode", "rpc", "--session-dir", "/workspace/.pi-sessions",
			"--provider", "ctf-gateway", "--model", cfg.Model.ModelID,
		},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		StdinOnce:    false,
		Tty:          false,
		Env: []string{
			"CTF_MODEL_BASE_URL=" + cfg.Model.BaseURL,
			"CTF_TASK_TOKEN=" + cfg.Model.Token,
			"CTF_MODEL_ID=" + cfg.Model.ModelID,
			"CTF_MODEL_SUPPORTS_IMAGES=" + strconv.FormatBool(cfg.Model.SupportsImages),
			"CTF_TASK_ID=" + cfg.Task.ID,
		},
		Labels: map[string]string{
			"com.ctfagentpi.managed": "true",
			"com.ctfagentpi.task":    cfg.Task.ID,
		},
	}

	// 默认移除全部 Linux capabilities、启用 no-new-privileges，并仅挂载题目工作区。
	// ReadonlyRootfs 为 false，是因为 Agent 需要在沙箱内安装临时解题工具。
	pidsLimit := cfg.MaxPIDs
	hostConfig := &container.HostConfig{
		Runtime:        runtimeName,
		AutoRemove:     false,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges=true"},
		ReadonlyRootfs: false,
		Resources: container.Resources{
			Memory: cfg.MaxMemory, NanoCPUs: cfg.MaxCPUs * 1_000_000_000, PidsLimit: &pidsLimit,
		},
		Mounts:     []mount.Mount{{Type: mount.TypeBind, Source: workspace, Target: "/workspace"}},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}
	if cfg.Network {
		hostConfig.NetworkMode = "bridge"
	} else {
		hostConfig.NetworkMode = "none"
	}
	// Pwn 分析需要调试子进程，仅对该题型恢复 SYS_PTRACE。
	if cfg.Task.Category == platform.CategoryPwn {
		hostConfig.CapAdd = []string{"SYS_PTRACE"}
	}

	created, err := m.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "ctfagentpi-"+cfg.Task.ID)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}

	// 在启动前附加标准流，避免漏掉 Pi 启动阶段的 RPC 或错误输出。
	attach, err := m.client.ContainerAttach(ctx, created.ID, container.AttachOptions{
		Stream: true, Stdin: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		_ = m.client.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("attach sandbox: %w", err)
	}
	if err := m.client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		attach.Close()
		_ = m.client.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("start sandbox: %w", err)
	}

	// Docker attach 使用多路复用流；StdCopy 将 stdout/stderr 拆到独立 Pipe，
	// 供 Agent 服务分别解析结构化 JSONL 与诊断文本。
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(stdoutWriter, stderrWriter, attach.Reader)
		_ = stdoutWriter.CloseWithError(copyErr)
		_ = stderrWriter.CloseWithError(copyErr)
	}()
	session := &Session{
		ContainerID: created.ID, Runtime: runtimeName, Stdout: stdoutReader, Stderr: stderrReader,
		input: attach.Conn,
		close: func() {
			attach.Close()
			_ = stdoutReader.Close()
			_ = stderrReader.Close()
		},
	}

	// 会话注册后，暂停、恢复和中止接口即可按任务 ID 找到同一 RPC 连接。
	m.mu.Lock()
	m.sessions[cfg.Task.ID] = session
	m.mu.Unlock()

	// 初始 Prompt 也是标准 Pi RPC 消息；发送失败时立即移除半创建容器。
	if err := session.Send(map[string]any{
		"id": "prompt-" + platform.NewID("rpc"), "type": "prompt", "message": cfg.Prompt,
	}); err != nil {
		_ = m.Stop(context.Background(), cfg.Task.ID, true)
		return nil, fmt.Errorf("send initial Pi prompt: %w", err)
	}
	return session, nil
}

// Abort 仅中止 Pi 当前回合，不立即删除容器或工作区。
func (m *Manager) Abort(ctx context.Context, taskID string) error {
	m.mu.Lock()
	session := m.sessions[taskID]
	m.mu.Unlock()
	if session == nil {
		return fmt.Errorf("task %s has no active sandbox", taskID)
	}
	return session.Send(map[string]string{"type": "abort"})
}

// Prompt 向现有 Pi RPC 会话发送新消息，不创建容器或新会话。
// 因此暂停后恢复仍保留模型对话上下文和挂载工作区。
func (m *Manager) Prompt(ctx context.Context, taskID, message string) error {
	m.mu.Lock()
	session := m.sessions[taskID]
	m.mu.Unlock()
	if session == nil {
		return fmt.Errorf("task %s has no active sandbox session to resume", taskID)
	}
	return session.Send(map[string]any{
		"id":      "resume-" + platform.NewID("rpc"),
		"type":    "prompt",
		"message": message,
	})
}

// Stop 关闭进程内会话并停止容器；remove=true 时同时强制删除容器卷。
func (m *Manager) Stop(ctx context.Context, taskID string, remove bool) error {
	m.mu.Lock()
	session := m.sessions[taskID]
	delete(m.sessions, taskID)
	m.mu.Unlock()
	if session == nil {
		return nil
	}

	// 先关闭 attach 流使读取协程退出，再给容器最多十秒优雅停止时间。
	session.close()
	timeout := 10
	err := m.client.ContainerStop(ctx, session.ContainerID, container.StopOptions{Timeout: &timeout})
	if remove {
		removeErr := m.client.ContainerRemove(ctx, session.ContainerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		if err == nil {
			err = removeErr
		}
	}
	return err
}

// Remove 关闭可能存在的内存会话并强制删除托管容器。
// containerID 来自 SQLite，因此 daemon 重启后仍可清理已结束任务实例。
func (m *Manager) Remove(ctx context.Context, taskID, containerID string) error {
	m.mu.Lock()
	session := m.sessions[taskID]
	delete(m.sessions, taskID)
	m.mu.Unlock()
	if session != nil {
		session.close()
		containerID = session.ContainerID
	}
	if containerID == "" {
		return nil
	}
	if err := m.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such container") {
			return nil
		}
		return fmt.Errorf("remove sandbox container: %w", err)
	}
	return nil
}

// pickRuntime 按调用方优先级返回第一个已安装运行时；均不可用时回退 runc。
func pickRuntime(available []string, preferred ...string) string {
	for _, candidate := range preferred {
		for _, runtimeName := range available {
			if runtimeName == candidate {
				return candidate
			}
		}
	}
	return "runc"
}
