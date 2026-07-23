package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestNormalizePiTextAndToolEvents(t *testing.T) {
	text := normalize("task_test", []byte(`{"type":"message_update","turnId":"turn_1","assistantMessageEvent":{"type":"text_delta","delta":"checking"}}`))
	if text.Type != "agent.message.delta" || text.TurnID != "turn_1" {
		t.Fatalf("unexpected text event %#v", text)
	}
	var payload map[string]any
	if err := json.Unmarshal(text.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	tool := normalize("task_test", []byte(`{"type":"tool_execution_start","toolCallId":"call_1","toolName":"bash"}`))
	if tool.Type != "tool.started" || tool.ToolCallID != "call_1" {
		t.Fatalf("unexpected tool event %#v", tool)
	}
}

func TestFlagsFromWriteupUsesOnlyFinalFlagSection(t *testing.T) {
	writeup := "# 解题报告\n\n工具输出中出现 flag{noise}\n\n## 最终 Flag\n\n```text\nflag{verified}\n```\n\n## 复盘\n\nflag{also-noise}\n"
	flags := flagsFromWriteup(writeup)
	if len(flags) != 1 || flags[0] != "flag{verified}" {
		t.Fatalf("unexpected flags %#v", flags)
	}
	if flags := flagsFromWriteup("# 报告\nflag{noise}"); len(flags) != 0 {
		t.Fatalf("expected no flags outside final section, got %#v", flags)
	}
	if flags := flagsFromWriteup("## 最终 Flag\n\n```text\nCTF2026-verified-value\n```"); len(flags) != 1 || flags[0] != "CTF2026-verified-value" {
		t.Fatalf("expected arbitrary verified flag format, got %#v", flags)
	}
	if flags := flagsFromWriteup("## 最终 Flag\n\n未找到"); len(flags) != 0 {
		t.Fatalf("expected no flag without an explicit code block, got %#v", flags)
	}
}

func TestResolveAttachmentPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	path, relative, err := resolveAttachmentPath(root, "source/input.bin")
	if err != nil || relative != filepath.Join("source", "input.bin") || filepath.Dir(path) == "" {
		t.Fatalf("unexpected attachment target path=%q relative=%q err=%v", path, relative, err)
	}
	if _, _, err := resolveAttachmentPath(root, "../outside.bin"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
