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

type DesktopService struct {
	mu sync.Mutex
}

type RunningTask struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type ExitCheck struct {
	CanExit bool          `json:"canExit"`
	Running []RunningTask `json:"running,omitempty"`
}

func (s *DesktopService) GetDaemonConnection() (appdata.Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := appdata.Resolve()
	if err != nil {
		return appdata.Connection{}, err
	}
	if connection, err := appdata.ReadConnection(paths.Connection); err == nil && daemonReady(connection.BaseURL) {
		return connection, nil
	}
	executable, err := findDaemonExecutable()
	if err != nil {
		return appdata.Connection{}, err
	}
	logFile, err := os.OpenFile(filepath.Join(paths.Root, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return appdata.Connection{}, err
	}
	command := exec.Command(executable)
	// The daemon is launched alongside CTF-BTFly.exe. Pass the GUI-adjacent
	// .env path explicitly so packaged usage never depends on the shell's
	// current working directory.
	if os.Getenv("CTF_AGENT_ENV_FILE") == "" {
		if appExecutable, executableErr := os.Executable(); executableErr == nil {
			envFile := filepath.Join(filepath.Dir(appExecutable), ".env")
			if info, statErr := os.Stat(envFile); statErr == nil && !info.IsDir() {
				command.Env = append(os.Environ(), "CTF_AGENT_ENV_FILE="+envFile)
			}
		}
	}
	command.Stdout = logFile
	command.Stderr = logFile
	prepareProcess(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return appdata.Connection{}, fmt.Errorf("start daemon: %w", err)
	}
	_ = command.Process.Release()
	_ = logFile.Close()
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

// PrepareExit asks the daemon to shut down. The daemon independently rejects
// this request when a task is running, so the tray cannot accidentally stop an
// active Pi sandbox.
func (s *DesktopService) PrepareExit() (ExitCheck, error) {
	paths, err := appdata.Resolve()
	if err != nil {
		return ExitCheck{}, err
	}
	connection, err := appdata.ReadConnection(paths.Connection)
	if err != nil || !daemonReady(connection.BaseURL) {
		return ExitCheck{CanExit: true}, nil
	}

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

func findDaemonExecutable() (string, error) {
	if configured := os.Getenv("CTF_DAEMON_EXECUTABLE"); configured != "" {
		return configured, nil
	}
	name := "ctfagent-daemon"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	appExecutable, _ := os.Executable()
	candidates := []string{filepath.Join(filepath.Dir(appExecutable), name), filepath.Join("bin", name), name}
	for _, candidate := range candidates {
		if absolute, err := filepath.Abs(candidate); err == nil {
			if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
				return absolute, nil
			}
		}
	}
	return "", fmt.Errorf("%s was not found; run task daemon:build first", name)
}

func daemonReady(baseURL string) bool {
	client := http.Client{Timeout: 400 * time.Millisecond}
	response, err := client.Get(baseURL + "/health")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
