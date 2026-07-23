package main

import (
	"log/slog"
	"os"

	"github.com/ctfagentpi/ctfagentpi/internal/daemon"
)

func main() {
	if err := daemon.Run(); err != nil {
		slog.Error("CTF-BTFly daemon stopped", "error", err)
		os.Exit(1)
	}
}
