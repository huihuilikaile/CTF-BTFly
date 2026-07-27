package agent

import (
	"testing"
)

func TestFlagsFromWriteupDetailedPrefersExpectedFormat(t *testing.T) {
	writeup := `# Writeup

## Final Flag

验证命令：

` + "```bash\ncurl -s http://target/flag\n```" + `

最终结果：

` + "```text\npicoCTF{stream_safe_result}\n```"

	findings := flagsFromWriteupDetailed(writeup, "picoCTF{...}")
	if len(findings) != 1 {
		t.Fatalf("expected one format-matched finding, got %#v", findings)
	}
	if findings[0].Value != "picoCTF{stream_safe_result}" || !findings[0].Verified || !findings[0].FormatMatched {
		t.Fatalf("unexpected finding %#v", findings[0])
	}
}

func TestFlagsFromWriteupDetailedUsesLastSingleLineBlockWithoutFormat(t *testing.T) {
	writeup := "## 最终 Flag\n\n```bash\ncurl-target\n```\n\n```text\nCTF2026-final-value\n```\n"
	findings := flagsFromWriteupDetailed(writeup, "")
	if len(findings) != 1 || findings[0].Value != "CTF2026-final-value" {
		t.Fatalf("expected only the final single-line result, got %#v", findings)
	}
}

func TestFlagsFromWriteupDetailedSupportsAliasesAndLabels(t *testing.T) {
	tests := []struct {
		name    string
		writeup string
		want    string
	}{
		{name: "short heading and label", writeup: "## Flag\nFlag: CTF2026-final-value\n", want: "CTF2026-final-value"},
		{name: "Chinese heading and inline code", writeup: "### 最终flag：\n答案是 `flag{inline_value}`\n", want: "flag{inline_value}"},
		{name: "tilde fence", writeup: "## FINAL FLAG\n~~~text\nACTF{tilde_value}\n~~~\n", want: "ACTF{tilde_value}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			findings := flagsFromWriteupDetailed(test.writeup, "")
			if len(findings) != 1 || findings[0].Value != test.want || !findings[0].Verified {
				t.Fatalf("unexpected findings %#v", findings)
			}
		})
	}
}

func TestExpectedFlagMatcherHandlesSplitAndRejectsPlaceholder(t *testing.T) {
	matcher := newExpectedFlagMatcher("flag{...}")
	if values := matcher.find("格式示例 flag{...}"); len(values) != 0 {
		t.Fatalf("placeholder must not be emitted, got %#v", values)
	}
	first := "工具返回 flag{split_"
	second := "across_events}"
	values := findStreamFlags(matcher, first+second)
	if len(values) != 1 || values[0] != "flag{split_across_events}" {
		t.Fatalf("expected split flag after buffering, got %#v", values)
	}
	if values := findStreamFlags(nil, "result: picoCTF{generic_without_hint}"); len(values) != 1 || values[0] != "picoCTF{generic_without_hint}" {
		t.Fatalf("expected generic flag without format hint, got %#v", values)
	}
}

func TestFlagsFromFinalResult(t *testing.T) {
	data := []byte(`{"status":"solved","flags":[{"value":"flag{json_result}","verified":true,"evidence":"challenge accepted"}]}`)
	findings, err := flagsFromFinalResult(data, "flag{...}")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Value != "flag{json_result}" || findings[0].Confidence != 100 || !findings[0].Verified || !findings[0].FormatMatched {
		t.Fatalf("unexpected findings %#v", findings)
	}
	if _, err := flagsFromFinalResult([]byte(`{"status":`), ""); err == nil {
		t.Fatal("expected malformed structured result to fail")
	}
}
