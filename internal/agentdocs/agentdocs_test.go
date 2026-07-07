package agentdocs

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateAgentDocs = flag.Bool("update-agent-docs", false, "rewrite docs/AGENT.md from the agentdocs renderer")

func TestAgentDocsGolden(t *testing.T) {
	want := RenderMarkdown()
	path := filepath.Join("..", "..", "docs", "AGENT.md")
	if *updateAgentDocs {
		if err := os.WriteFile(path, []byte(want), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s is stale; run go test ./internal/agentdocs -run TestAgentDocsGolden -update-agent-docs", path)
	}
}
