package appdata

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const defaultAddress = "127.0.0.1:17321"

type Paths struct {
	Root       string
	Database   string
	Workspaces string
	Connection string
	Token      string
}

type Connection struct {
	BaseURL string `json:"baseUrl"`
	Token   string `json:"token"`
}

func Resolve() (Paths, error) {
	root := os.Getenv("CTF_AGENT_DATA_DIR")
	if root == "" {
		executable, err := os.Executable()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve executable path: %w", err)
		}
		// CTF-BTFly is a portable local-first application: keep generated runtime
		// data in a dedicated data directory next to the packaged executable.
		// This keeps CTF-BTFly.exe, ctfagent-daemon.exe and .env separate from the
		// database, logs and task workspaces. CTF_AGENT_DATA_DIR remains
		// available for users who intentionally want another location.
		root = filepath.Join(filepath.Dir(executable), "data")
	}
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
	if err := os.MkdirAll(paths.Workspaces, 0o700); err != nil {
		return Paths{}, err
	}
	return paths, nil
}

func Address() string {
	if value := os.Getenv("CTF_DAEMON_ADDRESS"); value != "" {
		return value
	}
	return defaultAddress
}

func LoadOrCreateToken(path string) (string, error) {
	if value := os.Getenv("CTF_DAEMON_TOKEN"); value != "" {
		return value, nil
	}
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		return string(data), nil
	}
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

func WriteConnection(path, address, token string) error {
	data, err := json.Marshal(Connection{BaseURL: "http://" + address, Token: token})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

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
