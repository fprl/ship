package boxmemo

import (
	"os"
	"path/filepath"
	"strings"
)

const DisplayPath = "~/.config/ship/boxes"

func Path() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "ship", "boxes"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ship", "boxes"), nil
}

func Read() ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dedupeLines(strings.Split(string(data), "\n")), nil
}

func Remember(host string) (bool, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return false, nil
	}
	path, err := Path()
	if err != nil {
		return false, err
	}
	boxes, err := Read()
	if err != nil {
		return false, err
	}
	for _, known := range boxes {
		if known == host {
			return false, nil
		}
	}
	boxes = append(boxes, host)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	content := strings.Join(boxes, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return false, err
	}
	return true, nil
}

func KnownBoxesCause(boxes []string) string {
	var b strings.Builder
	b.WriteString("known boxes (")
	b.WriteString(DisplayPath)
	b.WriteString("):")
	if len(boxes) == 0 {
		b.WriteString("\n  none known yet")
		return b.String()
	}
	for _, box := range boxes {
		b.WriteString("\n  ")
		b.WriteString(box)
	}
	return b.String()
}

func dedupeLines(lines []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}
