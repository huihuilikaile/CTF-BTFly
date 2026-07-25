package appdata

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// defaultAddress 只监听回环地址，默认不向局域网暴露控制平面。
// 17321 落在部分 Windows / Hyper-V 环境动态保留的 TCP 端口段中，
// 会导致 daemon 在尚未初始化 API 前就无法监听。选用较高且固定的
// 回环端口，仍保持本地优先的安全边界。
const defaultAddress = "127.0.0.1:18731"

// Paths 集中描述 daemon 运行时数据的所有关键落点。
// 统一解析可避免各模块自行拼接路径而产生目录边界不一致。
type Paths struct {
	Root       string
	Database   string
	Workspaces string
	Connection string
	Token      string
}

// Connection 是桌面进程发现 daemon 所需的最小连接信息。
type Connection struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
}

// Resolve 计算并创建应用数据目录。
// 默认使用可执行文件旁的 data/，也允许通过 CTF_AGENT_DATA_DIR 显式覆盖。
func Resolve() (Paths, error) {
	root := os.Getenv("CTF_AGENT_DATA_DIR")
	if root == "" {
		directory, err := ExecutableDir()
		if err != nil {
			return Paths{}, err
		}
		// CTF-BTFly 是便携的本地优先应用：把运行数据放在程序旁的 data/
		// 中，将 exe 与 .env 同数据库、日志、题目工作区分开。
		root = filepath.Join(directory, "data")
	}
	// 转换为绝对路径后再派生各子路径，确保 daemon 与桌面端得到相同位置。
	root, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve application data directory: %w", err)
	}
	paths := Paths{
		Root:       root,
		Database:   filepath.Join(root, "platform.db"),
		Workspaces: filepath.Join(root, "workspaces"),
		Connection: filepath.Join(root, "daemon.json"),
		Token:      filepath.Join(root, "daemon.token"),
	}
	// 工作区目录使用仅当前用户可访问的权限创建；父目录也会随之建立。
	if err := os.MkdirAll(paths.Workspaces, 0o700); err != nil {
		return Paths{}, err
	}
	return paths, nil
}

// Address 返回控制平面监听地址；未配置时坚持使用本机回环地址。
func Address() string {
	if value := os.Getenv("CTF_DAEMON_ADDRESS"); value != "" {
		return value
	}
	return defaultAddress
}

// LoadOrCreateToken 按“环境变量 → 本地令牌文件 → 安全随机生成”的顺序
// 获取 daemon 鉴权令牌。生成值为 32 字节随机数的十六进制表示。
func LoadOrCreateToken(path string) (string, error) {
	if value := os.Getenv("CTF_DAEMON_TOKEN"); value != "" {
		return value, nil
	}
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return string(data), nil
	}
	// crypto/rand 失败时绝不退化为弱随机值；调用方应停止启动。
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// WriteConnection 把 HTTP 地址和 Token 写入仅当前用户可读的发现文件。
func WriteConnection(path, address, token string) error {
	data, err := json.Marshal(Connection{BaseURL: "http://" + address, Token: token})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadConnection 读取桌面端与 daemon 共享的连接发现文件。
func ReadConnection(path string) (Connection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Connection{}, err
	}
	var connection Connection
	if err := json.Unmarshal(data, &connection); err != nil {
		return Connection{}, err
	}
	return connection, nil
}
