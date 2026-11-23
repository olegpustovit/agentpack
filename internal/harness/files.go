package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const NoAttributionBody = `# Attribution policy

Do not add any AI-attribution lines to git commits, pull requests, or other artifacts you author.
Specifically, do not include:

- ` + "`Co-Authored-By: <model> <noreply@...>`" + ` trailers.
- ` + "`Generated with [agent name]`" + ` footers, banners, or similar credit lines.
- Tool/agent name signatures in commit messages or PR descriptions.

Write commit messages and PR descriptions as if a human author wrote them.
`

func CopySelectedEntries(sourceRoot, destinationRoot string, entries []string) error {
	for _, entry := range entries {
		source := filepath.Join(sourceRoot, entry)
		if _, err := os.Lstat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := copyTree(source, filepath.Join(destinationRoot, entry)); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		// Staged copies are edited by the harness even when the source is read-only.
		permissions := info.Mode().Perm() | 0o600
		if err := os.WriteFile(destination, data, permissions); err != nil {
			return err
		}
		return os.Chmod(destination, permissions)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// FingerprintFiles digests configuration inputs: absence, mode, size, link
// targets and file bytes, following links and walking directories. A socket,
// pipe or device is refused. Absolute paths stay out of the digest.
func FingerprintFiles(inputs []string) (string, error) {
	hash := sha256.New()
	active := map[string]bool{}
	var visit func(string, string) error
	visit = func(path, relative string) error {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(hash, "%q absent\n", relative)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%q mode=%s size=%d\n", relative, info.Mode(), info.Size())
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "link=%q\n", link)
			resolved, err := filepath.EvalSymlinks(path)
			if os.IsNotExist(err) {
				fmt.Fprintln(hash, "target absent")
				return nil
			}
			if err != nil {
				return err
			}
			return visit(resolved, relative+"/resolved")
		}
		if info.IsDir() {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			if active[absolute] {
				return fmt.Errorf("cyclic configuration symlink")
			}
			active[absolute] = true
			defer delete(active, absolute)
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := visit(filepath.Join(path, entry.Name()), relative+"/"+entry.Name()); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular configuration input %s", relative)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(hash, file)
		hash.Write([]byte{0})
		return err
	}
	for index, root := range inputs {
		if err := visit(root, fmt.Sprintf("input-%d", index)); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// DigestFile writes label and the bytes of path, or a missing marker, to hash.
func DigestFile(hash io.Writer, label, path string) {
	io.WriteString(hash, label+"\x00")
	data, err := os.ReadFile(path)
	if err != nil {
		io.WriteString(hash, "__missing__")
		return
	}
	hash.Write(data)
}

const guidanceBegin = "<!-- agentpack:guidance:begin -->"
const guidanceEnd = "<!-- agentpack:guidance:end -->"

func WriteGuidance(path, blob string) error {
	existing, _ := os.ReadFile(path)
	base := stripGuidance(string(existing))
	output := strings.TrimRight(base, "\n")
	if output == "" {
		output = "# AGENTS.md"
	}
	output += "\n\n" + guidanceBegin + "\n" + strings.TrimSpace(blob) + "\n" + guidanceEnd + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		return fmt.Errorf("write guidance %s: %w", path, err)
	}
	return nil
}

func stripGuidance(text string) string {
	begin := strings.Index(text, guidanceBegin)
	if begin < 0 {
		return text
	}
	relativeEnd := strings.Index(text[begin:], guidanceEnd)
	if relativeEnd < 0 {
		return text
	}
	end := begin + relativeEnd + len(guidanceEnd)
	return strings.TrimRight(text[:begin], "\n") + strings.TrimLeft(text[end:], "\n")
}

// ToolNames reads `tools: Read, Grep, Bash(git:*)` or the YAML list form
// and returns the bare tool names.
func ToolNames(value any) ([]string, bool) {
	var items []string
	switch typed := value.(type) {
	case string:
		depth, start := 0, 0
		for index, character := range typed {
			switch character {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 0 {
					items = append(items, typed[start:index])
					start = index + 1
				}
			}
		}
		items = append(items, typed[start:])
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				items = append(items, text)
			}
		}
	case []string:
		items = typed
	default:
		return nil, false
	}
	var names []string
	for _, item := range items {
		name, _, _ := strings.Cut(strings.TrimSpace(item), "(")
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names, true
}
