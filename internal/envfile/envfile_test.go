package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileUsesFileValuesForUnsetVariables(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	if err := os.WriteFile(path, []byte("CTF_ENVFILE_TEST_VALUE=from-file\nCTF_ENVFILE_TEST_QUOTED=\"quoted value\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CTF_ENVFILE_TEST_VALUE", "")
	t.Setenv("CTF_ENVFILE_TEST_QUOTED", "")
	if err := LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("CTF_ENVFILE_TEST_VALUE"); got != "from-file" {
		t.Fatalf("value = %q, want from-file", got)
	}
	if got := os.Getenv("CTF_ENVFILE_TEST_QUOTED"); got != "quoted value" {
		t.Fatalf("quoted value = %q, want quoted value", got)
	}
}

func TestLoadFileDoesNotOverrideProcessEnvironment(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	if err := os.WriteFile(path, []byte("CTF_ENVFILE_TEST_OVERRIDE=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CTF_ENVFILE_TEST_OVERRIDE", "from-process")
	if err := LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("CTF_ENVFILE_TEST_OVERRIDE"); got != "from-process" {
		t.Fatalf("value = %q, want from-process", got)
	}
}

func TestLoadFileRejectsInvalidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("not a valid environment line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadFile(path); err == nil {
		t.Fatal("expected malformed .env error")
	}
}
