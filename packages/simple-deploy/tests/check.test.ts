import { mkdtempSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { describe, expect, test } from "bun:test";
import { checkManifest } from "../src/cli";

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), "simple-deploy-test-"));
  writeFileSync(join(root, "bun.lock"), "\n");
  return root;
}

function writeManifest(root: string, content: string) {
  writeFileSync(join(root, "simple-deploy.toml"), content);
}

describe("checkManifest", () => {
  test("accepts a no-build Bun app", () => {
    const root = fixture();
    writeManifest(
      root,
      `
name = "api"

[env.production]
server = "admin@100.x.y.z"
path = "/var/apps/api"
runtime = "bun"

[services.web]
command = "bun run src/server.ts"
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
`,
    );

    expect(checkManifest(root, "production").errors).toEqual([]);
  });

  test("rejects proxy routes to services without ports", () => {
    const root = fixture();
    writeManifest(
      root,
      `
name = "api"

[env.production]
server = "admin@100.x.y.z"
path = "/var/apps/api"
runtime = "bun"

[services.web]
command = "bun run worker"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
`,
    );

    expect(checkManifest(root, "production").errors).toContain(
      "[routes.app].service must reference a service with a port",
    );
  });

  test("requires build output when build is present", () => {
    const root = fixture();
    writeManifest(
      root,
      `
name = "web"

[build]
command = "bun run build"

[env.production]
server = "admin@100.x.y.z"
path = "/var/apps/web"
runtime = "bun"
`,
    );

    expect(checkManifest(root, "production").errors).toContain("[build].output must be a non-empty string");
  });

  test("validates include paths as real paths without globs", () => {
    const root = fixture();
    mkdirSync(join(root, "public"));
    writeManifest(
      root,
      `
name = "web"

[build]
command = "bun run build"
output = "dist"
include = ["public", "missing", "src/*.ts"]

[env.production]
server = "admin@100.x.y.z"
path = "/var/apps/web"
runtime = "bun"
`,
    );

    const errors = checkManifest(root, "production").errors;
    expect(errors).toContain("[build].include[1] does not exist: missing");
    expect(errors).toContain("[build].include[2] must be a relative path without '..' or globs");
  });

  test("allows static apps without lockfiles", () => {
    const root = mkdtempSync(join(tmpdir(), "simple-deploy-test-"));
    writeManifest(
      root,
      `
name = "site"

[build]
command = "cp -r public dist"
output = "dist"

[env.production]
server = "admin@100.x.y.z"
path = "/var/apps/site"
runtime = "static"

[routes.app]
host = "site.example.com"
type = "static"
`,
    );

    expect(checkManifest(root, "production").errors).toEqual([]);
  });

  test("rejects unsafe env paths", () => {
    const root = fixture();
    writeManifest(
      root,
      `
name = "api"

[env.production]
server = "admin@100.x.y.z"
path = "/var/apps/api; touch /tmp/pwned"
runtime = "bun"
`,
    );

    expect(checkManifest(root, "production").errors).toContain(
      "[env.production].path must be /var/apps/api",
    );
  });
});
