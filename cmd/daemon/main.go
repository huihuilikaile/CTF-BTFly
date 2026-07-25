package main

import (
	"log/slog"
	"os"

	"github.com/ctfagentpi/ctfagentpi/internal/daemon"
)

// main 是独立控制平面可执行文件的最小入口。
// 所有依赖装配与优雅关闭逻辑都集中在 daemon.Run，便于测试和复用。
func main() {
	if err := daemon.Run(); err != nil {
		slog.Error("CTF-BTFly daemon stopped", "error", err)
		os.Exit(1)
	}
}
