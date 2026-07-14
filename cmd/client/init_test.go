package client

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInitWritesOnlyShipToml(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Example App")
	result, err := RunInit(root, InitOptions{
		Name:   "example-app",
		Server: "example.com",
		Host:   "api.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Created, []string{"ship.toml"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Created = %v, want %v", got, want)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ManifestFile {
		t.Fatalf("init wrote %v, want only %s", entries, ManifestFile)
	}
	body, err := os.ReadFile(filepath.Join(root, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name = "example-app"`, `box = "example.com"`, "[processes]", "web = {}", "[routes]", `"api.example.com" = "web"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("manifest missing %q:\n%s", want, body)
		}
	}
}

func TestRunInitWithoutHostOmitsRoutes(t *testing.T) {
	root := t.TempDir()
	if _, err := RunInit(root, InitOptions{Name: "api", Server: "example.com"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "[routes]") {
		t.Fatalf("manifest should omit routes without --host:\n%s", body)
	}
	for _, want := range []string{`name = "api"`, `box = "example.com"`, "[processes]", "web = {}"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("manifest missing %q:\n%s", want, body)
		}
	}
}

func TestRunInitUsesPackageJSONName(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@scope/My_App"}`), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := RunInit(root, InitOptions{Server: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if result.AppName != "my-app" {
		t.Fatalf("AppName = %q", result.AppName)
	}
}

func TestRunInitNeverOverwritesExistingFiles(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, ManifestFile)
	manifest := []byte("name = \"api\"\nbox = \"fake-vps\"\n")
	if err := os.WriteFile(manifestPath, manifest, 0644); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(root, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := RunInit(root, InitOptions{Name: "api", Server: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Kept, []string{ManifestFile}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Kept = %v, want %v", got, want)
	}
	gotManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotManifest) != string(manifest) {
		t.Fatalf("manifest was overwritten:\n%s", gotManifest)
	}
	gotDockerfile, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotDockerfile) != "FROM scratch\n" {
		t.Fatalf("Dockerfile was overwritten:\n%s", gotDockerfile)
	}
}

func TestRunInitRejectsInvalidExplicitName(t *testing.T) {
	_, err := RunInit(t.TempDir(), InitOptions{Name: "My App", Server: "example.com"})
	if err == nil || !strings.Contains(err.Error(), "invalid app name") {
		t.Fatalf("expected invalid explicit name error, got %v", err)
	}
}

func TestRunInitRejectsUserAtBox(t *testing.T) {
	_, err := RunInit(t.TempDir(), InitOptions{Name: "api", Server: "deploy@203.0.113.7"})
	if err == nil {
		t.Fatal("expected user@ box rejection")
	}
	for _, want := range []string{
		"--box must be a host; remove the user part",
		"next: ship init --box 203.0.113.7",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error:\n%v", want, err)
		}
	}
}

func TestRenderInitResultIncludesConfigPathOutsideCwd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app")
	result, err := RunInit(root, InitOptions{Name: "api", Server: "example.com"})
	if err != nil {
		t.Fatal(err)
	}

	out := captureInitOutput(t, result)
	for _, want := range []string{
		"git -C " + result.Root + " init",
		"git -C " + result.Root + " add .",
		"git -C " + result.Root + " commit -m \"initial ship app\"",
		"next: ship",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to include %q:\n%s", want, out)
		}
	}
}

func TestRenderInitResultDoesNotCreateNestedGitRepoInMonorepo(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	root := filepath.Join(repo, "apps", "api")
	result, err := RunInit(root, InitOptions{Name: "api", Server: "example.com"})
	if err != nil {
		t.Fatal(err)
	}

	out := captureInitOutput(t, result)
	if strings.Contains(out, "git -C "+result.Root+" init") {
		t.Fatalf("init output should not create nested git repo inside existing worktree:\n%s", out)
	}
	for _, want := range []string{
		"git -C " + result.Root + " add .",
		"git -C " + result.Root + " commit -m \"initial ship app\"",
		"next: ship",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to include %q:\n%s", want, out)
		}
	}
}

func captureInitOutput(t *testing.T, result InitResult) string {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	os.Stderr = w
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
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
