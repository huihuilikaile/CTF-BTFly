// Package envfile 负责加载 daemon 的本地配置，同时避免把真实密钥暴露给
// 桌面前端或 Agent 容器。
package envfile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctfagentpi/ctfagentpi/internal/appdata"
)

// Load 发现并加载一个 .env 文件，已有进程环境变量始终优先。
//
// 打包程序会显式传入 GUI 旁的 .env；单独启动 daemon 时则检查 daemon
// 自身目录，从而让密钥位置不依赖当前工作目录。
func Load() (string, error) {
	// 显式路径拥有最高优先级；配置过但文件无效时直接报错，避免静默使用空配置。
	if explicit := strings.TrimSpace(os.Getenv("CTF_AGENT_ENV_FILE")); explicit != "" {
		if err := LoadFile(explicit); err != nil {
			return "", fmt.Errorf("load CTF_AGENT_ENV_FILE: %w", err)
		}
		return explicit, nil
	}

	// 当前只检查 daemon 自身可执行文件同目录，切勿回退扫描父目录，以免误加载
	// 其他项目密钥。桌面端启动 daemon 时会把 GUI 同目录 .env 显式传入。
	candidates := make([]string, 0, 1)
	if environmentFile, err := appdata.EnvironmentFile(); err == nil {
		candidates = append(candidates, environmentFile)
	}

	// 去重后逐个检查候选；文件不存在是正常情况，其他文件系统错误必须上报。
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		if _, err := os.Stat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("inspect .env file %s: %w", candidate, err)
		}
		if err := LoadFile(candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", nil
}

// LoadFile 解析 KEY=VALUE 行，只设置操作系统环境中尚不存在的键。
func LoadFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open .env file %s: %w", path, err)
	}
	defer file.Close()

	// Scanner 默认行长较小，这里允许最多 1 MiB，以容纳较长的模型配置，
	// 同时对异常配置文件保留明确上限。
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4*1024), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		// 兼容 UTF-8 BOM、空行、整行注释和 shell 风格的 export 前缀。
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !validKey(key) {
			return fmt.Errorf("parse .env file %s:%d: expected KEY=VALUE", path, lineNumber)
		}
		// 仅剥离成对的最外层引号；本解析器不执行变量展开或 shell 转义，
		// 因而不会把配置内容当成命令运行。
		value = parseValue(value)
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set %s from .env: %w", key, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read .env file %s: %w", path, err)
	}
	return nil
}

// validKey 将变量名限制为字母或下划线开头，后续可包含数字。
func validKey(key string) bool {
	if key == "" {
		return false
	}
	for index, character := range key {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

// parseValue 去除首尾空白及一对单/双引号，其他字符按原样保留。
func parseValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}
