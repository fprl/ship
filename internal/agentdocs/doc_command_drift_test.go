package agentdocs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPublicDocShipCommandsUseDocumentedVerbs(t *testing.T) {
	root := filepath.Join("..", "..")
	paths := []string{
		"README.md",
		"docs/AGENT.md",
		"docs/getting-started.md",
		"docs/positioning.md",
		"docs/release-checklist.md",
		"docs/security-model.md",
		"docs/smoke-real-box.md",
	}
	examples, err := filepath.Glob(filepath.Join(root, "examples", "*", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range examples {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.ToSlash(rel))
	}
	slices.Sort(paths)

	verbs := map[string]bool{}
	for _, verb := range VerbNames() {
		verbs[verb] = true
	}

	for _, rel := range paths {
		path := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range shellFenceBlocks(string(data)) {
			for _, line := range strings.Split(block, "\n") {
				for _, invocation := range shipInvocations(line) {
					verb := invocationVerb(invocation, verbs)
					if !verbs[verb] {
						t.Fatalf("%s documents unknown ship verb %q in line: %s", rel, verb, strings.TrimSpace(line))
					}
				}
			}
		}
	}
}

func shellFenceBlocks(markdown string) []string {
	var blocks []string
	var current strings.Builder
	inFence := false
	shellFence := false
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "```") {
			if inFence {
				if shellFence {
					blocks = append(blocks, current.String())
				}
				current.Reset()
				inFence = false
				shellFence = false
				continue
			}
			info := strings.TrimSpace(strings.TrimPrefix(line, "```"))
			inFence = true
			shellFence = info == "" || info == "bash" || info == "sh" || info == "shell"
			continue
		}
		if inFence && shellFence {
			current.WriteString(line)
			current.WriteByte('\n')
		}
	}
	return blocks
}

func shipInvocations(line string) []string {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	var out []string
	for _, segment := range shellSegments(line) {
		fields := strings.Fields(strings.TrimSpace(segment))
		for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		command := cleanShellToken(fields[0])
		if command == "ship" || filepath.Base(command) == "ship" {
			out = append(out, strings.Join(fields[1:], " "))
		}
	}
	return out
}

func shellSegments(line string) []string {
	line = strings.ReplaceAll(line, "&&", "|")
	split := strings.FieldsFunc(line, func(r rune) bool {
		return r == '|' || r == ';'
	})
	var out []string
	for _, item := range split {
		item = strings.TrimSpace(strings.Trim(item, "()"))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func invocationVerb(rest string, verbs map[string]bool) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "ship"
	}
	first := cleanShellToken(fields[0])
	if strings.HasPrefix(first, "-") {
		return "ship"
	}
	if len(fields) > 2 {
		second := cleanShellToken(fields[1])
		third := cleanShellToken(fields[2])
		if verbs[first+" "+second+" "+third] {
			return first + " " + second + " " + third
		}
	}
	if len(fields) > 1 {
		second := cleanShellToken(fields[1])
		if verbs[first+" "+second] {
			return first + " " + second
		}
	}
	return first
}

func cleanShellToken(value string) string {
	return strings.Trim(value, "`'\"()[]{}.,")
}
