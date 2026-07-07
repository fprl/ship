package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fprl/simple-vps/internal/config"
	"github.com/fprl/simple-vps/internal/names"
	"github.com/fprl/simple-vps/internal/utils"
)

const (
	initTemplateContainer = "container"
	initTemplateStatic    = "static"
	initTemplatePHP       = "php"
	initTemplateHono      = "hono"
)

type InitOptions struct {
	Template string
	Name     string
	Server   string
	Host     string
	Port     int
}

type InitResult struct {
	AppName    string
	Template   string
	Root       string
	ConfigPath string
	Created    []string
	Kept       []string
}

type initFile struct {
	Path string
	Body string
}

type normalizedInit struct {
	template string
	name     string
	server   string
	host     string
	port     int
}

// CmdInit scaffolds a v1 manifest plus a small runnable app when the chosen
// template needs one. Existing app files are never overwritten.
func CmdInit(root string, opts InitOptions) {
	result, err := RunInit(root, opts)
	if err != nil {
		utils.DieError(err, 1)
	}
	renderInitResult(result)
}

func RunInit(root string, opts InitOptions) (InitResult, error) {
	normalized, err := normalizeInitOptions(root, opts)
	if err != nil {
		return InitResult{}, err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return InitResult{}, err
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return InitResult{}, err
	}
	manifestExists := false
	if info, exists, err := lstatInitPath(absRoot, ManifestFile); err != nil {
		return InitResult{}, err
	} else if exists {
		if info.Mode()&os.ModeSymlink != 0 {
			return InitResult{}, operationError(fmt.Sprintf("%s already exists and is a symlink", ManifestFile), "ship init")
		}
		if info.IsDir() {
			return InitResult{}, operationError(fmt.Sprintf("%s already exists and is a directory", ManifestFile), "ship init")
		}
		manifestExists = true
	}
	manifestPath := filepath.Join(absRoot, ManifestFile)

	result := InitResult{
		AppName:    normalized.name,
		Template:   normalized.template,
		Root:       absRoot,
		ConfigPath: manifestPath,
	}

	files := initTemplateFiles(normalized)
	if normalized.template != initTemplateStatic {
		info, exists, err := lstatInitPath(absRoot, "Dockerfile")
		if err != nil {
			return InitResult{}, err
		}
		if exists {
			if info.Mode()&os.ModeSymlink != 0 {
				return InitResult{}, operationError("Dockerfile already exists and is a symlink", "ship init")
			}
			if info.IsDir() {
				return InitResult{}, operationError("Dockerfile already exists and is a directory", "ship init")
			}
			result.Kept = append(result.Kept, "Dockerfile")
			files = nil
		}
	}

	if err := preflightInitFiles(absRoot, files); err != nil {
		return InitResult{}, err
	}
	if manifestExists {
		result.Kept = append(result.Kept, ManifestFile)
	}
	for _, file := range files {
		created, err := writeInitFile(absRoot, file)
		if err != nil {
			return InitResult{}, err
		}
		if created {
			result.Created = append(result.Created, file.Path)
		} else {
			result.Kept = append(result.Kept, file.Path)
		}
	}

	if !manifestExists {
		manifest := initManifest(normalized)
		if err := writeNewInitFile(manifestPath, manifest); err != nil {
			return InitResult{}, err
		}
		result.Created = append([]string{ManifestFile}, result.Created...)
	}
	return result, nil
}

func normalizeInitOptions(root string, opts InitOptions) (normalizedInit, error) {
	template := strings.ToLower(strings.TrimSpace(opts.Template))
	if template == "" {
		template = initTemplateContainer
	}
	switch template {
	case initTemplateContainer, initTemplateStatic, initTemplatePHP, initTemplateHono:
	default:
		return normalizedInit{}, usageError(fmt.Sprintf("invalid template %q: expected container, static, php, or hono", opts.Template), "ship init --template container")
	}

	var name string
	if opts.Name != "" {
		name = strings.TrimSpace(opts.Name)
		if !names.AppRe.MatchString(name) {
			return normalizedInit{}, usageError(fmt.Sprintf("invalid app name %q: must match %s", opts.Name, names.AppPattern), "ship init --name <app>")
		}
	} else {
		name = defaultAppName(root)
		if pkgName := packageJSONName(root); pkgName != "" {
			name = pkgName
		}
		name = normalizeAppName(name)
	}
	if !names.AppRe.MatchString(name) {
		return normalizedInit{}, usageError(fmt.Sprintf("invalid app name %q: must match %s", name, names.AppPattern), "ship init --name <app>")
	}

	server := strings.TrimSpace(opts.Server)
	if server == "" {
		server = "deploy@example.com"
	}
	if !config.ValidateSshTarget(server) {
		return normalizedInit{}, usageError("--box must be an SSH target like deploy@example.com", "ship init --box deploy@example.com")
	}

	host := strings.ToLower(strings.TrimSpace(opts.Host))
	if host == "" {
		host = name + ".example.com"
	}
	host = strings.TrimSuffix(host, ".")
	if !config.ValidateHost(host) {
		return normalizedInit{}, usageError("--host must be a valid hostname", "ship init --host api.example.com")
	}

	port := opts.Port
	if template == initTemplateStatic {
		if port != 0 {
			return normalizedInit{}, usageError("--port is not used with --template static", "ship init --template static")
		}
		return normalizedInit{template: template, name: name, server: server, host: host}, nil
	}
	if port == 0 {
		port = 3000
		if template == initTemplatePHP {
			port = 8080
		}
	}
	if port < 1 || port > 65535 {
		return normalizedInit{}, usageError("--port must be between 1 and 65535", "ship init --port 3000")
	}

	return normalizedInit{
		template: template,
		name:     name,
		server:   server,
		host:     host,
		port:     port,
	}, nil
}

func packageJSONName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Name
}

func preflightInitFiles(root string, files []initFile) error {
	for _, file := range files {
		clean := filepath.Clean(file.Path)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return operationError(fmt.Sprintf("invalid init file path %q", file.Path), "ship init")
		}
		if info, exists, err := lstatInitPath(root, clean); err != nil {
			return err
		} else if exists {
			if info.Mode()&os.ModeSymlink != 0 {
				return operationError(fmt.Sprintf("%s already exists and is a symlink", filepath.ToSlash(clean)), "ship init")
			}
			if info.IsDir() {
				return operationError(fmt.Sprintf("%s already exists and is a directory", file.Path), "ship init")
			}
		}
		if err := preflightInitParentDirs(root, clean); err != nil {
			return err
		}
	}
	return nil
}

func preflightInitParentDirs(root string, rel string) error {
	parent := filepath.Dir(filepath.Clean(rel))
	if parent == "." {
		return nil
	}
	current := root
	parts := strings.Split(parent, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil {
			display := filepath.ToSlash(filepath.Join(parts[:i+1]...))
			if info.Mode()&os.ModeSymlink != 0 {
				return operationError(fmt.Sprintf("%s already exists and is a symlink", display), "ship init")
			}
			if !info.IsDir() {
				return operationError(fmt.Sprintf("%s already exists and is not a directory", display), "ship init")
			}
			continue
		}
		if os.IsNotExist(err) {
			continue
		}
		return err
	}
	return nil
}

func initTemplateFiles(init normalizedInit) []initFile {
	switch init.template {
	case initTemplateStatic:
		return []initFile{{
			Path: "dist/index.html",
			Body: staticIndexHTML(init.name),
		}}
	case initTemplatePHP:
		return []initFile{
			{Path: "Dockerfile", Body: phpDockerfile(init.port)},
			{Path: "public/index.php", Body: phpIndex(init.name)},
		}
	case initTemplateHono:
		return []initFile{
			{Path: "Dockerfile", Body: honoDockerfile(init.port)},
			{Path: "package.json", Body: honoPackageJSON(init.name)},
			{Path: "src/server.ts", Body: honoServer(init.name, init.port)},
		}
	default:
		return []initFile{
			{Path: "Dockerfile", Body: pythonDockerfile(init.port)},
			{Path: "server.py", Body: pythonServer(init.name, init.port)},
		}
	}
}

func writeInitFile(root string, file initFile) (bool, error) {
	path := filepath.Join(root, file.Path)
	if info, exists, err := lstatInitPath(root, file.Path); err != nil {
		return false, err
	} else if exists {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, operationError(fmt.Sprintf("%s already exists and is a symlink", file.Path), "ship init")
		}
		if info.IsDir() {
			return false, operationError(fmt.Sprintf("%s already exists and is a directory", file.Path), "ship init")
		}
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	if err := preflightInitParentDirs(root, file.Path); err != nil {
		return false, err
	}
	if err := writeNewInitFile(path, file.Body); err != nil {
		return false, err
	}
	return true, nil
}

func lstatInitPath(root string, rel string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(filepath.Join(root, rel))
	if err == nil {
		return info, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func writeNewInitFile(path string, body string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(body)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func initManifest(init normalizedInit) string {
	if init.template == initTemplateStatic {
		return fmt.Sprintf(`name = "%s"
box = "%s"

[routes]
"%s" = { static = "dist" }
`, init.name, init.server, init.host)
	}

	return fmt.Sprintf(`name = "%s"
box = "%s"
probe = "/health"

[env]
DATABASE_PATH = "/data/app.sqlite"

[processes]
web = { port = %d, resources = { memory = "256m", cpus = 0.5 } }

[routes]
"%s" = "web"
`, init.name, init.server, init.port, init.host)
}

func renderInitResult(result InitResult) {
	for _, path := range result.Created {
		fmt.Printf("Created %s\n", initDisplayPath(result.Root, path))
	}
	for _, path := range result.Kept {
		fmt.Printf("Kept existing %s\n", initDisplayPath(result.Root, path))
	}
	gitPrefix := initGitPrefix(result.Root)
	steps := []string{
		"review " + initDisplayPath(result.Root, ManifestFile),
	}
	if !initRootInsideGitWorktree(result.Root) {
		steps = append(steps, gitPrefix+"init")
	}
	steps = append(steps,
		gitPrefix+"add .",
		gitPrefix+"commit -m \"initial ship app\"",
	)
	fmt.Fprintln(os.Stderr, "Next:")
	for i, step := range steps {
		fmt.Fprintf(os.Stderr, "%d. %s\n", i+1, step)
	}
	fmt.Fprintln(os.Stderr, "next: ship")
}

func initGitPrefix(root string) string {
	if root == "" {
		return "git "
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "git -C " + utils.ShellEscape(root) + " "
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return "git -C " + utils.ShellEscape(root) + " "
	}
	if root == absCwd {
		return "git "
	}
	return "git -C " + utils.ShellEscape(root) + " "
}

func initRootInsideGitWorktree(root string) bool {
	if root == "" {
		root = "."
	}
	out, _, code, _ := runCommand("git", []string{"rev-parse", "--is-inside-work-tree"}, root)
	return code == 0 && strings.TrimSpace(out) == "true"
}

func initDisplayPath(root string, rel string) string {
	if root == "" {
		return rel
	}
	path := filepath.Join(root, rel)
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	display, err := filepath.Rel(cwd, path)
	if err != nil || display == "." || strings.HasPrefix(display, ".."+string(filepath.Separator)) || display == ".." {
		return path
	}
	return filepath.ToSlash(display)
}

func defaultAppName(root string) string {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return normalizeAppName(filepath.Base(root))
}

func normalizeAppName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if idx := strings.LastIndex(value, "/"); idx >= 0 {
		value = value[idx+1:]
	}

	var b strings.Builder
	prevDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}

	candidate := strings.Trim(b.String(), "-")
	if candidate == "" {
		candidate = "app"
	}
	if candidate[0] < 'a' || candidate[0] > 'z' {
		candidate = "app-" + candidate
	}
	if len(candidate) > 41 {
		candidate = strings.Trim(candidate[:41], "-")
	}
	if len(candidate) < 2 {
		candidate += "p"
	}
	if !names.AppRe.MatchString(candidate) {
		return "app"
	}
	return candidate
}

func pythonDockerfile(port int) string {
	return fmt.Sprintf(`FROM docker.io/library/python:3.12-alpine
WORKDIR /app
COPY server.py .
EXPOSE %d
CMD ["python", "/app/server.py"]
`, port)
}

func pythonServer(name string, port int) string {
	return fmt.Sprintf(`from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({
            "app": %q,
            "status": "running",
            "database_path": os.environ.get("DATABASE_PATH"),
        }).encode())


HTTPServer(("0.0.0.0", %d), Handler).serve_forever()
`, name, port)
}

func staticIndexHTML(name string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>%s on ship</title>
  </head>
  <body>
    <main>
      <h1>%s</h1>
      <p>static-ok</p>
    </main>
  </body>
</html>
`, name, name)
}

func phpDockerfile(port int) string {
	return fmt.Sprintf(`FROM docker.io/library/php:8.4-cli-alpine
WORKDIR /app
COPY public ./public
EXPOSE %d
CMD ["php", "-S", "0.0.0.0:%d", "-t", "public", "public/index.php"]
`, port, port)
}

func phpIndex(name string) string {
	return fmt.Sprintf(`<?php

$path = parse_url($_SERVER["REQUEST_URI"] ?? "/", PHP_URL_PATH) ?: "/";
$file = __DIR__ . $path;

if ($path !== "/" && is_file($file)) {
    return false;
}

if ($path === "/health") {
    header("Content-Type: text/plain");
    echo "ok";
    return;
}

header("Content-Type: application/json");
echo json_encode([
    "app" => "%s",
    "status" => "running",
    "database_path" => getenv("DATABASE_PATH") ?: null,
], JSON_UNESCAPED_SLASHES) . PHP_EOL;
`, name)
}

func honoDockerfile(port int) string {
	return fmt.Sprintf(`FROM oven/bun:1-alpine
WORKDIR /app
COPY package.json ./
RUN bun install --frozen-lockfile || bun install
COPY src ./src
EXPOSE %d
CMD ["bun", "run", "src/server.ts"]
`, port)
}

func honoPackageJSON(name string) string {
	return fmt.Sprintf(`{
  "name": "%s",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "bun run src/server.ts",
    "start": "bun run src/server.ts"
  },
  "dependencies": {
    "hono": "^4.7.11"
  }
}
`, name)
}

func honoServer(name string, port int) string {
	return fmt.Sprintf(`import { Hono } from "hono";
import { serve } from "bun";

const app = new Hono();

app.get("/health", (c) => c.text("ok"));
app.get("/", (c) => c.json({
  app: %q,
  status: "running",
  database_path: process.env.DATABASE_PATH || null,
}));

serve({
  fetch: app.fetch,
  port: %d,
  hostname: "0.0.0.0",
});
`, name, port)
}
