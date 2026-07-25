package appdata

import (
	"fmt"
	"os"
	"path/filepath"
)

// ExecutableDir 返回当前实际运行的可执行文件所在目录，而不是当前工作目录。
// 便携版的 data/、.env 和随附 daemon 都以这里作为唯一的定位基准，避免
// 从源码目录、启动终端目录或任何写死的磁盘路径误读配置。
func ExecutableDir() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}

	// 尽量解析符号链接，使通过快捷方式或链接启动时仍定位到真实程序目录。
	// 某些平台不支持解析时保留 os.Executable 的结果，不影响正常启动。
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	return directoryForExecutable(executable)
}

// EnvironmentFile 返回当前可执行文件同目录的 .env 绝对路径。
// 调用方应按需检查文件是否存在；该函数不会回退到工作目录或父目录。
func EnvironmentFile() (string, error) {
	directory, err := ExecutableDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, ".env"), nil
}

func directoryForExecutable(executable string) (string, error) {
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("make executable path absolute: %w", err)
	}
	return filepath.Dir(absolute), nil
}
