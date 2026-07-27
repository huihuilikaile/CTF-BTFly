package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

const (
	maxFinalResultBytes = 64 << 10
	maxFlagEventBuffer  = 16 << 10
)

var (
	finalFlagHeading = regexp.MustCompile(`(?im)^[ \t]{0,3}#{1,6}[ \t]+(?:最终[ \t]*flag|final[ \t]+flag|flag)(?:[ \t]*[:：\-—][ \t]*)?$`)
	markdownHeading  = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	fencedCodeBlock  = regexp.MustCompile("(?s)(?:```|~~~)[^\\r\\n]*\\r?\\n(.*?)(?:```|~~~)")
	inlineCode       = regexp.MustCompile("`([^`\\r\\n]+)`")
	flagLabelLine    = regexp.MustCompile(`(?im)^[ \t]*(?:[-*+>][ \t]*)?(?:最终[ \t]*)?flag[ \t]*[:：=][ \t]*(.+?)[ \t]*$`)
	genericFlag      = regexp.MustCompile(`(?i)[a-z][a-z0-9_-]{0,31}\{[^\s{}\r\n]{1,512}\}`)
)

// FlagFinding 是后端统一的 Flag 识别结果。候选和已验证结果共用同一
// 结构，前端根据 Verified 明确区分，避免把工具输出直接显示为解题成功。
type FlagFinding struct {
	Value         string `json:"value"`
	Source        string `json:"source"`
	Confidence    int    `json:"confidence"`
	Verified      bool   `json:"verified"`
	FormatMatched bool   `json:"formatMatched"`
}

type expectedFlagMatcher struct {
	format string
	regexp *regexp.Regexp
}

// newExpectedFlagMatcher 把用户填写的 flag{...} 一类示例格式转成 RE2
// 安全的搜索表达式。除 .../…/* 占位符外，所有字符都按字面量处理。
func newExpectedFlagMatcher(format string) *expectedFlagMatcher {
	format = strings.TrimSpace(format)
	if format == "" {
		return nil
	}
	pattern := regexp.QuoteMeta(format)
	for _, marker := range []string{`\.\.\.`, `…`, `\*`} {
		pattern = strings.ReplaceAll(pattern, marker, `[^\r\n]{1,512}?`)
	}
	compiled, err := regexp.Compile(`(?i)` + pattern)
	if err != nil {
		return nil
	}
	return &expectedFlagMatcher{format: format, regexp: compiled}
}

func (matcher *expectedFlagMatcher) find(text string) []string {
	if matcher == nil || matcher.regexp == nil || text == "" {
		return nil
	}
	var result []string
	for _, match := range matcher.regexp.FindAllString(text, -1) {
		candidate := normalizeFlagValue(match)
		if candidate == "" || isFormatPlaceholder(candidate, matcher.format) {
			continue
		}
		result = appendUnique(result, candidate)
	}
	return result
}

func (matcher *expectedFlagMatcher) matches(value string) bool {
	if matcher == nil || matcher.regexp == nil {
		return false
	}
	match := matcher.regexp.FindString(value)
	return match != "" && len(match) == len(value) && !isFormatPlaceholder(value, matcher.format)
}

func isFormatPlaceholder(value, format string) bool {
	if !strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(format)) {
		return false
	}
	return strings.Contains(format, "...") || strings.Contains(format, "…") || strings.Contains(format, "*")
}

// flagsFromWriteupDetailed 支持中英文标题、多个代码块、行内代码和 Flag:
// 标签。预期格式用于优先精确提取，但最终章节的明确值不会因格式提示不准而丢失。
func flagsFromWriteupDetailed(writeup, format string) []FlagFinding {
	matcher := newExpectedFlagMatcher(format)
	var findings []FlagFinding
	for _, heading := range finalFlagHeading.FindAllStringIndex(writeup, -1) {
		section := writeup[heading[1]:]
		if next := markdownHeading.FindStringIndex(section); next != nil {
			section = section[:next[0]]
		}
		matched := matcher.find(section)
		for _, value := range matched {
			findings = appendFinding(findings, FlagFinding{
				Value: value, Source: "WRITEUP.md / 最终 Flag", Confidence: 96, Verified: true, FormatMatched: true,
			})
		}
		// 一旦预期格式在最终章节中命中，就不再把同章节的命令或证据代码
		// 当作候选；格式未命中时才启用兼容旧 Writeup 的宽松提取。
		if len(matched) == 0 {
			for _, value := range fallbackFinalSectionValues(section) {
				if containsFinding(findings, value) {
					continue
				}
				findings = appendFinding(findings, FlagFinding{
					Value: value, Source: "WRITEUP.md / 最终 Flag", Confidence: 90, Verified: true, FormatMatched: matcher.matches(value),
				})
			}
		}
	}
	return findings
}

// flagsFromWriteup 保留原有测试约定，实际识别统一委托给详细检测器。
func flagsFromWriteup(writeup string) []string {
	var result []string
	for _, finding := range flagsFromWriteupDetailed(writeup, "") {
		result = appendUnique(result, finding.Value)
	}
	return result
}

func fallbackFinalSectionValues(section string) []string {
	var result []string
	// 即使题目未填写预期格式，带 CTF 前缀和花括号的常见结果也比
	// 同章节中的命令、路径和摘要代码更可信。
	for _, match := range genericFlag.FindAllString(section, -1) {
		if value := normalizeFlagValue(match); value != "" {
			result = appendUnique(result, value)
		}
	}
	if len(result) > 0 {
		return result
	}
	for _, match := range flagLabelLine.FindAllStringSubmatch(section, -1) {
		if len(match) == 2 {
			if value := normalizeFlagValue(match[1]); value != "" {
				result = appendUnique(result, value)
			}
		}
	}
	if len(result) > 0 {
		return result
	}

	// 对无法用格式判断的任意 Flag，仅接受单行代码块，并选择最终章节
	// 中最后一个明确值；这兼容“先给验证命令、最后给结果”的 Writeup，
	// 同时避免把多行脚本的每一行都标成已验证结果。
	for _, block := range fencedCodeBlock.FindAllStringSubmatch(section, -1) {
		if len(block) != 2 {
			continue
		}
		value := normalizeFlagValue(block[1])
		if looksLikeStandaloneFlag(value) {
			result = []string{value}
		}
	}
	if len(result) > 0 {
		return result
	}
	for _, match := range inlineCode.FindAllStringSubmatch(section, -1) {
		if len(match) == 2 {
			if value := normalizeFlagValue(match[1]); looksLikeStandaloneFlag(value) {
				result = []string{value}
			}
		}
	}
	if len(result) > 0 {
		return result
	}
	plain := fencedCodeBlock.ReplaceAllString(section, "")
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(plain, "\r\n", "\n"), "\n") {
		if value := normalizeFlagValue(line); value != "" {
			lines = append(lines, value)
		}
	}
	if len(lines) == 1 && looksLikeStandaloneFlag(lines[0]) {
		result = append(result, lines[0])
	}
	return result
}

func normalizeFlagValue(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"- ", "* ", "+ ", "> ", "-\t", "*\t", "+\t", ">\t"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			break
		}
	}
	if match := flagLabelLine.FindStringSubmatch(value); len(match) == 2 {
		value = strings.TrimSpace(match[1])
	}
	value = strings.TrimSpace(strings.Trim(value, "`'\""))
	value = strings.TrimRight(value, " \t,，.;；。")
	lower := strings.ToLower(strings.TrimSpace(value))
	switch lower {
	case "", "未找到", "未找到 flag", "未发现", "not found", "none", "null", "n/a", "unknown":
		return ""
	}
	if len(value) > 1024 || strings.ContainsAny(value, "\r\n") || !utf8.ValidString(value) {
		return ""
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return ""
		}
	}
	return value
}

func looksLikeStandaloneFlag(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t") {
		return false
	}
	return strings.Contains(value, "{") || strings.ContainsAny(value, "_-=:") || len(value) >= 8
}

type finalResultEnvelope struct {
	Status string            `json:"status"`
	Flag   string            `json:"flag"`
	Flags  []json.RawMessage `json:"flags"`
}

type finalResultFlag struct {
	Value    string `json:"value"`
	Verified bool   `json:"verified"`
	Evidence string `json:"evidence"`
}

func flagsFromFinalResult(data []byte, format string) ([]FlagFinding, error) {
	var envelope finalResultEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	matcher := newExpectedFlagMatcher(format)
	solved := strings.EqualFold(strings.TrimSpace(envelope.Status), "solved")
	var values []finalResultFlag
	if value := normalizeFlagValue(envelope.Flag); value != "" {
		values = append(values, finalResultFlag{Value: value, Verified: solved})
	}
	for _, raw := range envelope.Flags {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			values = append(values, finalResultFlag{Value: value, Verified: solved})
			continue
		}
		var item finalResultFlag
		if json.Unmarshal(raw, &item) == nil {
			values = append(values, item)
		}
	}
	var findings []FlagFinding
	for _, item := range values {
		value := normalizeFlagValue(item.Value)
		if value == "" {
			continue
		}
		verified := item.Verified || solved
		confidence := 85
		if verified {
			confidence = 100
		}
		findings = appendFinding(findings, FlagFinding{
			Value: value, Source: "artifacts/final-result.json", Confidence: confidence,
			Verified: verified, FormatMatched: matcher.matches(value),
		})
	}
	return findings, nil
}

// DetectFlags 允许任务结束和 Writeup API 使用同一个权威检测入口。
func (s *Service) DetectFlags(ctx context.Context, taskID string) []FlagFinding {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil
	}
	root, err := s.taskWorkspace(ctx, taskID)
	if err != nil {
		return nil
	}
	var findings []FlagFinding
	if data, err := readBoundedWorkspaceFile(root, filepath.Join("artifacts", "final-result.json"), maxFinalResultBytes); err == nil {
		if result, parseErr := flagsFromFinalResult(data, task.FlagFormat); parseErr == nil {
			for _, finding := range result {
				findings = appendFinding(findings, finding)
			}
		} else {
			s.emitFlagDiagnostic(ctx, taskID, "artifacts/final-result.json", parseErr)
		}
	}
	if data, name, err := readWriteupCaseInsensitive(root); err == nil {
		for _, finding := range flagsFromWriteupDetailed(string(data), task.FlagFormat) {
			finding.Source = name + " / 最终 Flag"
			findings = appendFinding(findings, finding)
		}
	}
	for _, finding := range findings {
		s.emitFlagFinding(ctx, taskID, finding)
	}
	return findings
}

func readBoundedWorkspaceFile(root, requested string, limit int64) ([]byte, error) {
	path, err := resolveWorkspaceFile(root, filepath.ToSlash(requested))
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("file is not valid UTF-8")
	}
	return data, nil
}

func readWriteupCaseInsensitive(root string) ([]byte, string, error) {
	for _, name := range []string{"WRITEUP.md", "writeup.md"} {
		if data, err := readBoundedWorkspaceFile(root, name, maxWriteupFlagBytes); err == nil {
			return data, name, nil
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "WRITEUP.md") {
			data, err := readBoundedWorkspaceFile(root, entry.Name(), maxWriteupFlagBytes)
			return data, entry.Name(), err
		}
	}
	return nil, "", os.ErrNotExist
}

func (s *Service) scheduleFlagDetection(taskID string) {
	s.DetectFlags(context.Background(), taskID)
	go func() {
		for _, delay := range []time.Duration{200 * time.Millisecond, 800 * time.Millisecond, 2 * time.Second} {
			timer := time.NewTimer(delay)
			<-timer.C
			s.DetectFlags(context.Background(), taskID)
		}
	}()
}

func (s *Service) detectEventFlags(task platform.Task, event platform.Event) {
	text, source, confidence := flagTextFromEvent(event)
	kind := "agent"
	if strings.HasPrefix(event.Type, "tool.") {
		kind = "tool"
	}
	key := task.ID + "|" + kind + "|" + event.TurnID + "|" + event.ToolCallID
	completed := event.Type == "tool.completed" || event.Type == "agent.message.completed"
	if text == "" {
		if completed {
			s.mu.Lock()
			delete(s.flagBuffers, key)
			s.mu.Unlock()
		}
		return
	}
	matcher := newExpectedFlagMatcher(task.FlagFormat)
	s.mu.Lock()
	buffer := s.flagBuffers[key] + text
	if len(buffer) > maxFlagEventBuffer {
		buffer = buffer[len(buffer)-maxFlagEventBuffer:]
	}
	s.flagBuffers[key] = buffer
	if completed {
		delete(s.flagBuffers, key)
	}
	s.mu.Unlock()
	for _, value := range findStreamFlags(matcher, buffer) {
		s.emitFlagFinding(context.Background(), task.ID, FlagFinding{
			Value: value, Source: source, Confidence: confidence, Verified: false, FormatMatched: matcher.matches(value),
		})
	}
}

func findStreamFlags(matcher *expectedFlagMatcher, text string) []string {
	if matcher != nil {
		return matcher.find(text)
	}
	var result []string
	for _, match := range genericFlag.FindAllString(text, -1) {
		if value := normalizeFlagValue(match); value != "" {
			result = appendUnique(result, value)
		}
	}
	return result
}

func flagTextFromEvent(event platform.Event) (string, string, int) {
	if event.Type != "agent.message.delta" && event.Type != "agent.message.completed" && event.Type != "tool.output" && event.Type != "tool.completed" {
		return "", "", 0
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return "", "", 0
	}
	if event.Type == "agent.message.delta" {
		if inner, ok := payload["assistantMessageEvent"].(map[string]any); ok {
			if delta, ok := inner["delta"].(string); ok {
				return delta, "Agent 回复", 70
			}
		}
	}
	var parts []string
	for _, key := range []string{"output", "result", "partialResult", "text", "content", "message", "delta"} {
		if value, ok := payload[key]; ok {
			collectFlagText(value, &parts)
		}
	}
	if len(parts) == 0 {
		return "", "", 0
	}
	if strings.HasPrefix(event.Type, "tool.") {
		return strings.Join(parts, "\n"), "工具输出", 60
	}
	return strings.Join(parts, "\n"), "Agent 回复", 70
}

func collectFlagText(value any, result *[]string) {
	switch typed := value.(type) {
	case string:
		*result = append(*result, typed)
	case []any:
		for _, item := range typed {
			collectFlagText(item, result)
		}
	case map[string]any:
		for key, item := range typed {
			switch strings.ToLower(key) {
			case "text", "content", "output", "result", "message", "delta":
				collectFlagText(item, result)
			}
		}
	}
}

func (s *Service) emitFlagFinding(ctx context.Context, taskID string, finding FlagFinding) {
	finding.Value = normalizeFlagValue(finding.Value)
	if finding.Value == "" {
		return
	}
	s.seedFlagFindings(ctx, taskID)
	key := flagFindingKey(finding)
	s.mu.Lock()
	if s.flagFindings[taskID][key] {
		s.mu.Unlock()
		return
	}
	s.flagFindings[taskID][key] = true
	s.mu.Unlock()
	_, err := s.emit(ctx, platform.Event{TaskID: taskID, Source: flagEventSource(finding), Type: "flag.candidate", Payload: platform.JSONPayload(finding)})
	if err != nil {
		s.mu.Lock()
		delete(s.flagFindings[taskID], key)
		s.mu.Unlock()
	}
}

func (s *Service) seedFlagFindings(ctx context.Context, taskID string) {
	s.mu.Lock()
	if s.flagFindingLoaded[taskID] {
		if s.flagFindings[taskID] == nil {
			s.flagFindings[taskID] = make(map[string]bool)
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	loaded := make(map[string]bool)
	var after int64
	for {
		events, err := s.store.ListEvents(ctx, taskID, after, 5000)
		if err != nil {
			break
		}
		for _, event := range events {
			after = event.Sequence
			if event.Type != "flag.candidate" {
				continue
			}
			var finding FlagFinding
			if json.Unmarshal(event.Payload, &finding) == nil && finding.Value != "" {
				loaded[flagFindingKey(finding)] = true
			}
		}
		if len(events) < 5000 {
			break
		}
	}
	s.mu.Lock()
	if !s.flagFindingLoaded[taskID] {
		s.flagFindings[taskID] = loaded
		s.flagFindingLoaded[taskID] = true
	}
	if s.flagFindings[taskID] == nil {
		s.flagFindings[taskID] = make(map[string]bool)
	}
	s.mu.Unlock()
}

func flagFindingKey(finding FlagFinding) string {
	return fmt.Sprintf("%t|%s", finding.Verified, strings.ToLower(strings.TrimSpace(finding.Value)))
}

func flagEventSource(finding FlagFinding) string {
	if strings.HasPrefix(strings.ToLower(finding.Source), "writeup") {
		return "writeup"
	}
	if strings.Contains(finding.Source, "final-result.json") {
		return "result"
	}
	if finding.Source == "工具输出" {
		return "tool"
	}
	return "agent"
}

func (s *Service) emitFlagDiagnostic(ctx context.Context, taskID, source string, err error) {
	finding := FlagFinding{Value: "__diagnostic__:" + source}
	s.seedFlagFindings(ctx, taskID)
	key := flagFindingKey(finding)
	s.mu.Lock()
	if s.flagFindings[taskID][key] {
		s.mu.Unlock()
		return
	}
	s.flagFindings[taskID][key] = true
	s.mu.Unlock()
	_, _ = s.emit(ctx, platform.Event{TaskID: taskID, Source: "flag-detector", Type: "flag.detection_error", Payload: platform.JSONPayload(map[string]string{"source": source, "error": err.Error()})})
}

func appendFinding(findings []FlagFinding, finding FlagFinding) []FlagFinding {
	if finding.Value == "" {
		return findings
	}
	for index, existing := range findings {
		if strings.EqualFold(existing.Value, finding.Value) {
			if (finding.Verified && !existing.Verified) || finding.Confidence > existing.Confidence {
				findings[index] = finding
			}
			return findings
		}
	}
	return append(findings, finding)
}

func containsFinding(findings []FlagFinding, value string) bool {
	for _, finding := range findings {
		if strings.EqualFold(finding.Value, value) {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
}
