package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ctfagentpi/ctfagentpi/internal/platform"
)

// subtaskInput 是从父工作区复制到专项子工作区的受控输入。它只允许引用
// 已存在的普通文件；路径校验始终在 daemon 内完成。
type subtaskInput struct {
	ArtifactPaths []string
}

// copySubtaskInput 复制父附件、已归档请求和显式列出的普通 Artifact 到子工作区。
func (s *Service) copySubtaskInput(parent, child platform.Task, input subtaskInput, archivedRequest string) error {
	parentRoot := filepath.Join(s.workspaces, parent.ID)
	childRoot := filepath.Join(s.workspaces, child.ID)
	if err := os.MkdirAll(childRoot, 0o700); err != nil {
		return fmt.Errorf("create child workspace: %w", err)
	}
	if err := copyDirectory(filepath.Join(parentRoot, "attachments"), filepath.Join(childRoot, "attachments")); err != nil {
		return fmt.Errorf("copy parent attachments: %w", err)
	}
	if err := copyFile(archivedRequest, filepath.Join(childRoot, "handoff", "request.json")); err != nil {
		return fmt.Errorf("copy subtask request: %w", err)
	}
	for _, requested := range input.ArtifactPaths {
		source, err := resolveWorkspaceFile(parentRoot, requested)
		if err != nil {
			return fmt.Errorf("resolve subtask artifact %q: %w", requested, err)
		}
		target := filepath.Join(childRoot, "handoff", "input", filepath.FromSlash(requested))
		if err := copyFile(source, target); err != nil {
			return fmt.Errorf("copy subtask artifact %q: %w", requested, err)
		}
	}
	return nil
}

// copyOptionalFile 在源文件不存在时成功返回，存在时执行严格普通文件复制。
func copyOptionalFile(source, target string) error {
	if _, err := os.Stat(source); errorsIsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return copyFile(source, target)
}

// copyFile 拒绝符号链接与特殊文件，并以 0600 权限复制内容。
func copyFile(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// copyDirectory 递归复制普通文件，显式跳过符号链接和设备等特殊条目。
func copyDirectory(source, target string) error {
	if _, err := os.Stat(source); errorsIsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return copyFile(path, destination)
	})
}

func errorsIsNotExist(err error) bool { return err != nil && os.IsNotExist(err) }
