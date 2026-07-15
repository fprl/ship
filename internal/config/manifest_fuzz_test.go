package config

import (
	"os"
	"path/filepath"
	"testing"
)

const readmeManifestSeed = `name = "taskflow"
box = "203.0.113.7"
production_branch = "main"

[processes]
web = "npx react-router-serve build/server/index.js"
worker = { cmd = "node build/worker.js", preview = false }

[routes]
"taskflow.app" = "web"
"taskflow.app/docs" = { static = "docs/dist" }
"www.taskflow.app" = { redirect = "taskflow.app" }

[env]
LOG_LEVEL = "info"
DATABASE_URL = "@secret"
SMTP_URL = "@secret"

[env.preview]
LOG_LEVEL = "debug"
POSTHOG_KEY = "phc_test456"

[preview]
base = "preview.taskflow.app"
aliases = true

release = "npx drizzle-kit migrate"
probe = "/healthz"
webhook = "https://ntfy.sh/..."
`

func FuzzManifest(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(validContainerManifest()),
		[]byte(readmeManifestSeed),
		[]byte("name = \"api\"\nbox = \"example.com\"\n[processes]\nweb = { port = 3000 }\n"),
		[]byte("name = \"site\"\nbox = \"example.com\"\n[routes]\n\"site.example.com\" = { static = \"dist\" }\n"),
		[]byte("name = \"api\"\nbox = \"example.com\"\n[preview]\nbase = \"https://preview.example.com\"\n"),
	} {
		f.Add(seed)
	}
	for _, path := range []string{
		"../../examples/astro-static/ship.toml",
		"../../examples/django-sqlite/ship.toml",
		"../../examples/hono-bun-api/ship.toml",
		"../../examples/mixed-api-docs/ship.toml",
		"../../examples/php-plain/ship.toml",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read manifest seed %q: %v", path, err)
		}
		f.Add(data)
	}

	root := f.TempDir()
	manifestPath := filepath.Join(root, "ship.toml")
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(manifestPath, data, 0644); err != nil {
			t.Fatalf("write fuzz manifest: %v", err)
		}
		_, _ = ReadManifest(root)
	})
}
