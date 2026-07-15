package agentdocs

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/ship/internal/config"
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

func TestShipTOMLExampleMatchesManifestValidation(t *testing.T) {
	doc := RenderMarkdown()
	const start = "<!-- BEGIN SHIP.TOML EXAMPLE -->\n```toml\n"
	const end = "\n```\n<!-- END SHIP.TOML EXAMPLE -->"
	startAt := strings.Index(doc, start)
	if startAt < 0 {
		t.Fatal("ship.toml example marker missing")
	}
	startAt += len(start)
	endAt := strings.Index(doc[startAt:], end)
	if endAt < 0 {
		t.Fatal("ship.toml example end marker missing")
	}
	example := doc[startAt : startAt+endAt]

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ship.toml"), []byte(example), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM alpine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	errors, warnings, err := config.CheckManifest(root, config.ProductionEnvName)
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 0 || len(warnings) != 0 {
		t.Fatalf("embedded ship.toml example failed validation: errors=%v warnings=%v", errors, warnings)
	}
}
