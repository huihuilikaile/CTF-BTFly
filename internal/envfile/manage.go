package envfile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctfagentpi/ctfagentpi/internal/appdata"
)

// ConfigFile resolves the sole .env file used by the daemon and its desktop launcher.
func ConfigFile() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("CTF_AGENT_ENV_FILE")); explicit != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("resolve CTF_AGENT_ENV_FILE: %w", err)
		}
		return filepath.Clean(path), nil
	}
	return appdata.EnvironmentFile()
}

// Read parses a .env file without changing the process environment. A missing file
// is a valid empty configuration so the model editor can create the first one.
func Read(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("open .env file %s: %w", path, err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4*1024), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !validKey(key) {
			return nil, fmt.Errorf("parse .env file %s:%d: expected KEY=VALUE", path, lineNumber)
		}
		values[key] = parseValue(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read .env file %s: %w", path, err)
	}
	return values, nil
}

// Update atomically changes only supplied keys while preserving comments and
// unrelated settings. nil removes a key; values with a newline are rejected.
func Update(path string, updates map[string]*string) error {
	if len(updates) == 0 {
		return nil
	}
	for key := range updates {
		if !validKey(key) {
			return fmt.Errorf("invalid .env key %q", key)
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read .env file %s: %w", path, err)
	}
	lines := []string{}
	if len(contents) > 0 {
		lines = strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
	}

	seen := make(map[string]bool, len(updates))
	output := make([]string, 0, len(lines)+len(updates))
	for _, line := range lines {
		key, found := managedLineKey(line)
		value, change := updates[key]
		if !found || !change {
			output = append(output, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if value == nil {
			continue
		}
		encoded, err := encodeManagedValue(*value)
		if err != nil {
			return fmt.Errorf("encode %s: %w", key, err)
		}
		output = append(output, key+"="+encoded)
	}
	keys := make([]string, 0, len(updates))
	for key := range updates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if seen[key] || updates[key] == nil {
			continue
		}
		encoded, err := encodeManagedValue(*updates[key])
		if err != nil {
			return fmt.Errorf("encode %s: %w", key, err)
		}
		output = append(output, key+"="+encoded)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create .env directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary .env: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary .env: %w", err)
	}
	if _, err := temporary.WriteString(strings.Join(output, "\n") + "\n"); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary .env: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary .env: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace .env file: %w", err)
	}
	return nil
}

func managedLineKey(line string) (string, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	line = strings.TrimPrefix(line, "export ")
	key, _, found := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	return key, found && validKey(key)
}

func encodeManagedValue(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("newlines are not allowed")
	}
	if value == "" || !strings.ContainsAny(value, " \t#'\"") {
		return value, nil
	}
	if !strings.Contains(value, "\"") {
		return "\"" + value + "\"", nil
	}
	if !strings.Contains(value, "'") {
		return "'" + value + "'", nil
	}
	return "", errors.New("value contains both quote characters")
}
