package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

const imageVersion = "0.1.0"

var images = map[platform.Category]string{
	platform.CategoryWeb:       "ctf-agent-pi-web:" + imageVersion,
	platform.CategoryCrypto:    "ctf-agent-pi-crypto:" + imageVersion,
	platform.CategoryPwn:       "ctf-agent-pi-pwn:" + imageVersion,
	platform.CategoryReverse:   "ctf-agent-pi-reverse:" + imageVersion,
	platform.CategoryForensics: "ctf-agent-pi-forensics:" + imageVersion,
	platform.CategoryMisc:      "ctf-agent-pi-misc:" + imageVersion,
}

type ModelAccess struct {
	BaseURL string
	Token   string
	ModelID string
}

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

type Session struct {
	ContainerID string
	Runtime     string
	Stdout      io.Reader
	Stderr      io.Reader
	input       io.Writer
	close       func()
}

func (s *Session) Send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.input.Write(data)
	return err
}

type Health struct {
	Available         bool     `json:"available"`
	ServerVersion     string   `json:"serverVersion,omitempty"`
	Runtimes          []string `json:"runtimes"`
	NormalRuntime     string   `json:"normalRuntime"`
	PwnRuntime        string   `json:"pwnRuntime"`
	IsolationWarnings []string `json:"isolationWarnings"`
}

type Manager struct {
	client   *client.Client
	mu       sync.Mutex
	sessions map[string]*Session
}

func New() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create Docker client: %w", err)
	}
	return &Manager{client: cli, sessions: make(map[string]*Session)}, nil
}

func (m *Manager) Close() error { return m.client.Close() }

func ImageFor(category platform.Category) string { return images[category] }

func (m *Manager) Health(ctx context.Context) Health {
	info, err := m.client.Info(ctx)
	if err != nil {
		return Health{Available: false, IsolationWarnings: []string{err.Error()}}
	}
	runtimes := make([]string, 0, len(info.Runtimes))
	for name := range info.Runtimes {
		runtimes = append(runtimes, name)
	}
	sort.Strings(runtimes)
	normal := pickRuntime(runtimes, "runsc", "io.containerd.runsc.v1", "runc")
	pwn := pickRuntime(runtimes, "kata", "io.containerd.kata.v2", "runc")
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

func (m *Manager) Start(ctx context.Context, cfg StartConfig) (*Session, error) {
	if err := os.MkdirAll(cfg.Workspace, 0o700); err != nil {
		return nil, fmt.Errorf("create task workspace: %w", err)
	}
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
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
	runtimeName := health.NormalRuntime
	if cfg.Task.Category == platform.CategoryPwn {
		runtimeName = health.PwnRuntime
	}
	imageName := ImageFor(cfg.Task.Category)
	if _, _, err := m.client.ImageInspectWithRaw(ctx, imageName); err != nil {
		return nil, fmt.Errorf("required image %s is unavailable: %w", imageName, err)
	}

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
			"CTF_TASK_ID=" + cfg.Task.ID,
		},
		Labels: map[string]string{
			"com.ctfagentpi.managed": "true",
			"com.ctfagentpi.task":    cfg.Task.ID,
		},
	}
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
	if cfg.Task.Category == platform.CategoryPwn {
		hostConfig.CapAdd = []string{"SYS_PTRACE"}
	}

	created, err := m.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "ctfagentpi-"+cfg.Task.ID)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
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
	m.mu.Lock()
	m.sessions[cfg.Task.ID] = session
	m.mu.Unlock()
	if err := session.Send(map[string]any{
		"id": "prompt-" + platform.NewID("rpc"), "type": "prompt", "message": cfg.Prompt,
	}); err != nil {
		_ = m.Stop(context.Background(), cfg.Task.ID, true)
		return nil, fmt.Errorf("send initial Pi prompt: %w", err)
	}
	return session, nil
}

func (m *Manager) Abort(ctx context.Context, taskID string) error {
	m.mu.Lock()
	session := m.sessions[taskID]
	m.mu.Unlock()
	if session == nil {
		return fmt.Errorf("task %s has no active sandbox", taskID)
	}
	return session.Send(map[string]string{"type": "abort"})
}

// Prompt continues an already-attached Pi RPC session. It intentionally does
// not create a container or a fresh session, which lets CTF-BTFly resume a paused
// task while retaining the conversation and mounted workspace.
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

func (m *Manager) Stop(ctx context.Context, taskID string, remove bool) error {
	m.mu.Lock()
	session := m.sessions[taskID]
	delete(m.sessions, taskID)
	m.mu.Unlock()
	if session == nil {
		return nil
	}
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

// Remove closes an in-memory session if present and forcibly removes the
// task's managed container. containerID is retained in SQLite so this also
// cleans up settled tasks after the daemon has been restarted.
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
