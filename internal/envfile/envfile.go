// Package envfile loads local daemon configuration without exposing secrets to
// the desktop frontend or agent containers.
package envfile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Load discovers and loads one .env file. Existing process environment values
// always take precedence over file values.
//
// Packaged CTFAgentPi starts its daemon next to CTFAgentPi.exe and passes that
// directory explicitly. When the daemon is run by itself, its own directory is
// used. This keeps secrets independent from the current working directory.
func Load() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("CTF_AGENT_ENV_FILE")); explicit != "" {
		if err := LoadFile(explicit); err != nil {
			return "", fmt.Errorf("load CTF_AGENT_ENV_FILE: %w", err)
		}
		return explicit, nil
	}

	candidates := make([]string, 0, 1)
	if executable, err := os.Executable(); err == nil {
		directory := filepath.Dir(executable)
		candidates = append(candidates, filepath.Join(directory, ".env"))
	}

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

// LoadFile parses KEY=VALUE lines and adds only keys that were not already
// provided by the operating-system environment.
func LoadFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open .env file %s: %w", path, err)
	}
	defer file.Close()

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
			return fmt.Errorf("parse .env file %s:%d: expected KEY=VALUE", path, lineNumber)
		}
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

func parseValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}
