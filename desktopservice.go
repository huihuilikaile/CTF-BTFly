package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/appdata"
)

// DesktopService 是 Wails 暴露给前端的桌面原生服务。
// 互斥锁避免 React 初始化阶段的重复调用并发启动多个 daemon。
type DesktopService struct {
	mu sync.Mutex
}

// RunningTask 是退出检查返回给桌面端的最小任务摘要。
type RunningTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// ExitCheck 描述 daemon 是否允许 GUI 安全退出。
type ExitCheck struct {
	CanExit bool          `json:"canExit"`
	Running []RunningTask `json:"running,omitempty"`
}

// GetDaemonConnection 优先复用现有 daemon；健康检查失败时才启动同目录的 daemon。
// 返回值包含仅供本机 API 使用的地址与 Bearer Token，前端不会自行读取本地数据文件。
func (s *DesktopService) GetDaemonConnection() (appdata.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 解析连接文件位置，并先尝试复用已经健康运行的控制平面。
	paths, err := appdata.Resolve()
	if err != nil {
		return appdata.Connection{}, err
	}
	if connection, err := appdata.ReadConnection(paths.Connection); err == nil && daemonReady(connection.BaseURL) {
		return connection, nil
	}

	// 没有可复用实例时，查找随桌面程序发布的 daemon 可执行文件，
	// 并把标准输出、标准错误都追加到权限受限的本地日志。
	executable, err := findDaemonExecutable()
	if err != nil {
		return appdata.Connection{}, err
	}
	logFile, err := os.OpenFile(filepath.Join(paths.Root, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return appdata.Connection{}, err
	}
	command := exec.Command(executable)
	// daemon 与 CTF-BTFly.exe 一同发布。显式传递 GUI 同目录的 .env，
	// 避免打包运行时错误地依赖启动 shell 的当前工作目录或任何写死路径。
	if os.Getenv("CTF_AGENT_ENV_FILE") == "" {
		if envFile, executableErr := appdata.EnvironmentFile(); executableErr == nil {
			if info, statErr := os.Stat(envFile); statErr == nil && !info.IsDir() {
				command.Env = append(os.Environ(), "CTF_AGENT_ENV_FILE="+envFile)
			}
		}
	}
	command.Stdout = logFile
	command.Stderr = logFile
	prepareProcess(command)

	// Start 后立即释放进程句柄，使 daemon 生命周期独立于 GUI；
	// daemon 自身会通过 API 拒绝在任务运行期间退出。
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return appdata.Connection{}, fmt.Errorf("start daemon: %w", err)
	}
	_ = command.Process.Release()
	_ = logFile.Close()

	// daemon 初始化还要打开 SQLite、Docker 与 HTTP 服务，因此在八秒窗口内
	// 轮询连接文件和健康端点，而不是假设进程创建成功就代表服务可用。
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		connection, readErr := appdata.ReadConnection(paths.Connection)
		if readErr == nil && daemonReady(connection.BaseURL) {
			return connection, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return appdata.Connection{}, fmt.Errorf("daemon did not become ready; inspect %s", filepath.Join(paths.Root, "daemon.log"))
}

// PrepareExit 请求 daemon 关闭。daemon 会独立检查运行中任务，因此即使
// 前端状态过期，托盘动作也不能误停仍在工作的 Pi 沙箱。
func (s *DesktopService) PrepareExit() (ExitCheck, error) {
	// daemon 不存在或已不可达时，GUI 可以直接退出，不必把陈旧连接文件视为阻塞。
	paths, err := appdata.Resolve()
	if err != nil {
		return ExitCheck{}, err
	}
	connection, err := appdata.ReadConnection(paths.Connection)
	if err != nil || !daemonReady(connection.BaseURL) {
		return ExitCheck{CanExit: true}, nil
	}

	// 关闭接口必须携带 daemon Token，并设置短超时以免托盘操作长期卡住。
	request, err := http.NewRequest(http.MethodPost, connection.BaseURL+"/api/daemon/shutdown", nil)
	if err != nil {
		return ExitCheck{}, err
	}
	request.Header.Set("Authorization", "Bearer "+connection.Token)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return ExitCheck{}, fmt.Errorf("request daemon shutdown: %w", err)
	}
	defer response.Body.Close()

	// 将响应限制为 1 MiB，防止损坏或被替换的本地服务返回无限响应体。
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return ExitCheck{}, err
	}
	if response.StatusCode == http.StatusConflict {
		var payload struct {
			Tasks []RunningTask `json:"tasks"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return ExitCheck{}, fmt.Errorf("decode running tasks: %w", err)
		}
		return ExitCheck{CanExit: false, Running: payload.Tasks}, nil
	}

	// 统一提取 daemon 的结构化错误；没有 JSON 错误时回退到 HTTP 状态文本。
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		if strings.TrimSpace(payload.Error) == "" {
			payload.Error = response.Status
		}
		return ExitCheck{}, fmt.Errorf("daemon shutdown rejected: %s", payload.Error)
	}
	return ExitCheck{CanExit: true}, nil
}

// findDaemonExecutable 按“显式配置 → GUI 同目录 → 开发 bin 目录 → PATH”
// 的顺序查找控制平面程序，同时兼容 Windows 的 .exe 后缀。
func findDaemonExecutable() (string, error) {
	if configured := os.Getenv("CTF_DAEMON_EXECUTABLE"); configured != "" {
		return configured, nil
	}
	name := "ctfagent-daemon"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// 发布版本优先使用 GUI 自身目录下的 daemon；开发模式才继续检查 bin/ 和 PATH。
	candidates := make([]string, 0, 3)
	if directory, err := appdata.ExecutableDir(); err == nil {
		candidates = append(candidates, filepath.Join(directory, name))
	}
	candidates = append(candidates, filepath.Join("bin", name), name)

	// 转为绝对路径后再检查普通文件，避免把目录或无效相对路径交给 os/exec。
	for _, candidate := range candidates {
		if absolute, err := filepath.Abs(candidate); err == nil {
			if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
				return absolute, nil
			}
		}
	}
	return "", fmt.Errorf("%s was not found; run task daemon:build first", name)
}

// daemonReady 使用极短超时探测不需要鉴权的健康端点。
// 它只判断控制平面是否可接受请求，不读取任何业务状态。
func daemonReady(baseURL string) bool {
	client := http.Client{Timeout: 400 * time.Millisecond}
	response, err := client.Get(baseURL + "/health")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
