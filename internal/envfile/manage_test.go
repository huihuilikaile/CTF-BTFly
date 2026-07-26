package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stringPointer(value string) *string { return &value }

func TestUpdatePreservesUnrelatedValuesAndProtectsSecretSyntax(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := "# keep this comment\nUNRELATED_VALUE=unchanged\nCTF_MODEL_DEEPSEEK_API_KEY=old\nCTF_MODEL_DEEPSEEK_API_KEY=duplicate\nREMOVE_ME=yes\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, map[string]*string{
		"CTF_MODELS":                 stringPointer("deepseek"),
		"CTF_MODEL_DEEPSEEK_API_KEY": stringPointer("secret value#1"),
		"REMOVE_ME":                  nil,
	}); err != nil {
		t.Fatal(err)
	}
	values, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["UNRELATED_VALUE"] != "unchanged" || values["CTF_MODELS"] != "deepseek" {
		t.Fatalf("unexpected values %#v", values)
	}
	if values["CTF_MODEL_DEEPSEEK_API_KEY"] != "secret value#1" {
		t.Fatalf("API key was not round-tripped safely: %q", values["CTF_MODEL_DEEPSEEK_API_KEY"])
	}
	if _, exists := values["REMOVE_ME"]; exists {
		t.Fatalf("removed key remains in %#v", values)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "# keep this comment") || strings.Count(string(content), "CTF_MODEL_DEEPSEEK_API_KEY=") != 1 {
		t.Fatalf("update did not preserve comment or remove duplicate: %s", content)
	}
}

func TestUpdateRejectsMultilineValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := Update(path, map[string]*string{"CTF_MODEL_TEST_API_KEY": stringPointer("bad\nvalue")}); err == nil {
		t.Fatal("expected newline value to be rejected")
	}
}
