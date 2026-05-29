package client

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fprl/simple-vps/internal/config"
)

func TestRunInitTemplatesCreateValidManifests(t *testing.T) {
	tests := []struct {
		template string
		want     []string
		notWant  []string
	}{
		{
			template: "container",
			want:     []string{"simple-vps.toml", "Dockerfile", "server.py"},
		},
		{
			template: "static",
			want:     []string{"simple-vps.toml", "dist/index.html"},
			notWant:  []string{"Dockerfile"},
		},
		{
			template: "php",
			want:     []string{"simple-vps.toml", "Dockerfile", "public/index.php"},
		},
		{
			template: "hono",
			want:     []string{"simple-vps.toml", "Dockerfile", "package.json", "src/server.ts"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "Example App")
			result, err := RunInit(root, InitOptions{
				Template: tt.template,
				Name:     "example-app",
				Server:   "deploy@example.com",
				Host:     tt.template + ".example.com",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Template != tt.template {
				t.Fatalf("template = %q", result.Template)
			}
			for _, path := range tt.want {
				if _, err := os.Stat(filepath.Join(root, path)); err != nil {
					t.Fatalf("expected %s: %v", path, err)
				}
			}
			for _, path := range tt.notWant {
				if _, err := os.Stat(filepath.Join(root, path)); err == nil {
					t.Fatalf("did not expect %s", path)
				}
			}
			errors, warnings, err := config.CheckManifest(root, "production")
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) > 0 || len(errors) > 0 {
				t.Fatalf("manifest validation warnings=%v errors=%v", warnings, errors)
			}
		})
	}
}

func TestRunInitUsesPackageJSONName(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@scope/My_App"}`), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := RunInit(root, InitOptions{Template: "static", Server: "deploy@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AppName != "my-app" {
		t.Fatalf("AppName = %q", result.AppName)
	}

	manifest, err := os.ReadFile(filepath.Join(root, "simple-vps.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `name = "my-app"`) {
		t.Fatalf("manifest did not use package name:\n%s", manifest)
	}
}

func TestRunInitDoesNotOverwriteExistingAppFiles(t *testing.T) {
	root := t.TempDir()
	dockerfile := filepath.Join(root, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := RunInit(root, InitOptions{Template: "container", Name: "api", Server: "deploy@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result.Kept, "Dockerfile") {
		t.Fatalf("expected Dockerfile to be kept, result=%+v", result)
	}
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "FROM scratch\n" {
		t.Fatalf("Dockerfile was overwritten:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "server.py")); err == nil {
		t.Fatal("server.py should not be created when Dockerfile already exists")
	}
}

func TestRunInitPreflightsBeforeWritingFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "server.ts"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := RunInit(root, InitOptions{Template: "hono", Name: "api", Server: "deploy@example.com"})
	if err == nil || !strings.Contains(err.Error(), "src/server.ts already exists and is a directory") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	for _, path := range []string{"simple-vps.toml", "Dockerfile", "package.json"} {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			t.Fatalf("%s should not be written after preflight failure", path)
		}
	}
}

func TestRunInitRejectsSymlinkScaffoldPaths(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(t.TempDir(), "outside.py"), filepath.Join(root, "server.py")); err != nil {
			t.Fatal(err)
		}

		_, err := RunInit(root, InitOptions{Template: "container", Name: "api", Server: "deploy@example.com"})
		if err == nil || !strings.Contains(err.Error(), "server.py already exists and is a symlink") {
			t.Fatalf("expected symlink error, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "simple-vps.toml")); err == nil {
			t.Fatal("manifest should not be written after symlink preflight failure")
		}
	})

	t.Run("parent", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "src")); err != nil {
			t.Fatal(err)
		}

		_, err := RunInit(root, InitOptions{Template: "hono", Name: "api", Server: "deploy@example.com"})
		if err == nil || !strings.Contains(err.Error(), "src already exists and is a symlink") {
			t.Fatalf("expected parent symlink error, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "simple-vps.toml")); err == nil {
			t.Fatal("manifest should not be written after parent symlink preflight failure")
		}
	})
}

func TestRunInitRejectsInvalidExplicitName(t *testing.T) {
	_, err := RunInit(t.TempDir(), InitOptions{Template: "static", Name: "My App", Server: "deploy@example.com"})
	if err == nil || !strings.Contains(err.Error(), "invalid app name") {
		t.Fatalf("expected invalid explicit name error, got %v", err)
	}
}

func TestRenderInitResultIncludesConfigPathOutsideCwd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app")
	result, err := RunInit(root, InitOptions{Template: "static", Name: "api", Server: "deploy@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	out := captureInitOutput(t, result)
	want := "simple-vps check --config " + result.ConfigPath + " --env production"
	if !strings.Contains(out, want) {
		t.Fatalf("expected output to include %q:\n%s", want, out)
	}
}

func TestRunInitRejectsExistingManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "simple-vps.toml"), []byte("name = \"api\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := RunInit(root, InitOptions{Template: "static", Server: "deploy@example.com"})
	if err == nil || !strings.Contains(err.Error(), "simple-vps.toml already exists") {
		t.Fatalf("expected existing manifest error, got %v", err)
	}
}

func TestRunInitRejectsStaticPort(t *testing.T) {
	_, err := RunInit(t.TempDir(), InitOptions{Template: "static", Port: 3000})
	if err == nil || !strings.Contains(err.Error(), "--port is not used") {
		t.Fatalf("expected static port error, got %v", err)
	}
}

func TestRunInitCanSetInternalTLS(t *testing.T) {
	root := t.TempDir()
	if _, err := RunInit(root, InitOptions{
		Template: "php",
		Name:     "api",
		Server:   "deploy@example.com",
		Host:     "api.example.com",
		TLS:      "internal",
	}); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "simple-vps.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `tls = "internal"`) {
		t.Fatalf("manifest missing internal TLS:\n%s", manifest)
	}
}

func TestRunInitGeneratedContainerTemplatesBuildWhenRequested(t *testing.T) {
	if os.Getenv("SIMPLE_VPS_TEST_INIT_BUILDS") != "1" {
		t.Skip("set SIMPLE_VPS_TEST_INIT_BUILDS=1 to build generated container templates")
	}
	builder := initTemplateBuilder(t)
	for _, template := range []string{"container", "php", "hono"} {
		t.Run(template, func(t *testing.T) {
			root := t.TempDir()
			if _, err := RunInit(root, InitOptions{
				Template: template,
				Name:     "api",
				Server:   "deploy@example.com",
				Host:     template + ".example.com",
			}); err != nil {
				t.Fatal(err)
			}
			image := "simple-vps-init-test-" + template + "-" + strings.ToLower(t.Name())
			image = strings.NewReplacer("/", "-", "_", "-").Replace(image)
			t.Cleanup(func() {
				_ = exec.Command(builder, "rmi", "-f", image).Run()
			})
			cmd := exec.Command(builder, "build", "-t", image, root)
			var output bytes.Buffer
			cmd.Stdout = io.MultiWriter(os.Stdout, &output)
			cmd.Stderr = io.MultiWriter(os.Stderr, &output)
			if err := cmd.Run(); err != nil {
				t.Fatalf("%s build failed: %v\n%s", builder, err, output.String())
			}
		})
	}
}

func initTemplateBuilder(t *testing.T) string {
	t.Helper()
	if requested := os.Getenv("SIMPLE_VPS_TEST_INIT_BUILDER"); requested != "" {
		path, err := exec.LookPath(requested)
		if err != nil {
			t.Fatalf("SIMPLE_VPS_TEST_INIT_BUILDER=%s not found", requested)
		}
		return path
	}
	for _, candidate := range []string{"podman", "docker"} {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path
		}
	}
	t.Skip("podman or docker not available")
	return ""
}

func contains(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func captureInitOutput(t *testing.T, result InitResult) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
	}()
	renderInitResult(result)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
