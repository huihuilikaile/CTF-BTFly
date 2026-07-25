package appdata

import (
	"path/filepath"
	"testing"
)

func TestEnvironmentFileRuleUsesExecutableDirectory(t *testing.T) {
	// EnvironmentFile 本身依赖真实进程；这里覆盖其底层路径规则，确保不会
	// 意外改回基于当前工作目录或写死的发布目录。
	executable := filepath.Join(t.TempDir(), "portable", "CTF-BTFly.exe")
	directory, err := directoryForExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Join(directory, ".env"), filepath.Join(filepath.Dir(executable), ".env"); got != want {
		t.Fatalf("environment file = %q, want %q", got, want)
	}
}
