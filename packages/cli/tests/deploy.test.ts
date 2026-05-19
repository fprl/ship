import { existsSync, mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { afterEach, describe, expect, test } from "bun:test";
import { main, type CommandResult, type CommandRunner } from "../src/cli";

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), "simple-vps-deploy-test-"));
  writeFileSync(join(root, "bun.lock"), "\n");
  writeFileSync(
    join(root, "simple-vps.toml"),
    `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
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
  return root;
}

async function runLocal(command: string[]): Promise<CommandResult> {
  const proc = Bun.spawn(command, { stdout: "pipe", stderr: "pipe" });
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  return { code, stdout, stderr };
}

function seedMockCheckout(command: string[]) {
  if (command[0] !== "sh") return;
  const match = command[2]?.match(/tar -x -C ([^ ]+)/);
  if (!match) return;
  mkdirSync(match[1], { recursive: true });
  writeFileSync(join(match[1], "bun.lock"), "\n");
}

afterEach(() => {
  process.exitCode = 0;
});

describe("deploy", () => {
  test("deploys a no-build Bun app as a SHA-addressed release", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        seedMockCheckout(command);
        if (command.join(" ") === "git -C " + root + " rev-parse HEAD") {
          return { code: 0, stdout: "a1b2c3d4e5f6\n", stderr: "" };
        }
        if (command.join(" ") === "git -C " + root + " status --porcelain") {
          return { code: 0, stdout: "", stderr: "" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["deploy", "production"], root, { runner });

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(joined).toContain("git -C " + root + " rev-parse HEAD");
    expect(joined).toContain("git -C " + root + " status --porcelain");
    expect(joined).toContain("ssh deploy@100.x.y.z test -d /var/apps/api/shared");
    expect(joined).toContain("ssh deploy@100.x.y.z install -d -m 2775 /var/apps/api/releases/a1b2c3d4e5f6");
    expect(joined.some((command) => command.startsWith("sh -c git -C " + root + " archive HEAD | tar -x -C "))).toBe(
      true,
    );
    expect(joined.some((command) => command.startsWith("rsync -az --delete "))).toBe(true);
    expect(joined).toContain("ssh deploy@100.x.y.z chmod 2775 /var/apps/api/releases/a1b2c3d4e5f6");
    expect(joined).toContain("ssh deploy@100.x.y.z ln -sfn /var/apps/api/shared/.env /var/apps/api/releases/a1b2c3d4e5f6/.env");
    expect(joined).toContain("ssh deploy@100.x.y.z ln -sfn /var/apps/api/shared/db /var/apps/api/releases/a1b2c3d4e5f6/db");
    expect(joined).toContain(
      "ssh deploy@100.x.y.z ln -sfn /var/apps/api/shared/storage /var/apps/api/releases/a1b2c3d4e5f6/storage",
    );
    expect(joined).toContain("ssh deploy@100.x.y.z ln -sfn /var/apps/api/shared/logs /var/apps/api/releases/a1b2c3d4e5f6/logs");
    expect(joined).toContain(
      "ssh deploy@100.x.y.z sudo simple-vps app run-as api --cwd /var/apps/api/releases/a1b2c3d4e5f6 -- bun install --production --frozen-lockfile",
    );
    expect(joined.some((command) => command.startsWith("rsync -az ") && command.includes("deploy@100.x.y.z:/tmp/simple-deploy/"))).toBe(
      true,
    );
    const unitUpload = commands.find((command) => command[0] === "rsync" && command[1] === "-az" && command[3]?.includes(":/tmp/simple-deploy/"));
    const unitRoot = unitUpload?.[2]?.replace(/\/$/, "");
    expect(readFileSync(join(unitRoot!, "simple-api-web.service"), "utf8")).toContain("Description=simple-vps: api/web");
    expect(joined.some((command) => command.includes("sudo simple-vps app install-unit api web "))).toBe(true);
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service start api web");
    expect(joined).toContain(
      "ssh deploy@100.x.y.z for i in $(seq 1 10); do status=$(curl -o /dev/null -s -w '%{http_code}' --max-time 2 http://127.0.0.1:3000/health || true); [ \"$status\" = \"200\" ] && exit 0; sleep 1; done; exit 1",
    );
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps route proxy api.example.com --port 3000 --app api");
    expect(joined.some((command) => command.includes("rm -rf --") && command.includes("/var/apps/api/releases"))).toBe(true);
  });

  test("rolls current back and does not publish routes when health check fails", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        seedMockCheckout(command);
        const joined = command.join(" ");
        if (joined === "git -C " + root + " rev-parse HEAD") return { code: 0, stdout: "a1b2c3d4e5f6\n", stderr: "" };
        if (joined === "git -C " + root + " status --porcelain") return { code: 0, stdout: "", stderr: "" };
        if (joined === "ssh deploy@100.x.y.z readlink -f /var/apps/api/current") {
          return { code: 0, stdout: "/var/apps/api/releases/oldsha\n", stderr: "" };
        }
        if (joined.includes("curl -o /dev/null -s -w '%{http_code}'")) {
          return { code: 22, stdout: "", stderr: "HTTP 500" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["deploy", "production"], root, { runner });

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(1);
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service stop api web");
    expect(joined).toContain("ssh deploy@100.x.y.z ln -sfn /var/apps/api/releases/oldsha /var/apps/api/current");
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service start api web");
    expect(joined).not.toContain("ssh deploy@100.x.y.z sudo simple-vps route proxy api.example.com --port 3000 --app api");
  });

  test("does not fail a successful deploy when release pruning fails", async () => {
    const root = fixture();
    const warnings: string[] = [];
    const originalError = console.error;
    const runner: CommandRunner = {
      async run(command) {
        seedMockCheckout(command);
        const joined = command.join(" ");
        if (joined === "git -C " + root + " rev-parse HEAD") return { code: 0, stdout: "a1b2c3d4e5f6\n", stderr: "" };
        if (joined === "git -C " + root + " status --porcelain") return { code: 0, stdout: "", stderr: "" };
        if (joined.includes("rm -rf --")) return { code: 1, stdout: "", stderr: "permission denied" };
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => warnings.push(String(message));
    try {
      await main(["deploy", "production"], root, { runner });
    } finally {
      console.error = originalError;
    }

    expect(process.exitCode).toBe(0);
    expect(warnings.join("\n")).toContain("Warning: deploy succeeded; pruning failed: failed to prune releases: permission denied");
  });

  test("refuses to deploy tracked dotenv files", async () => {
    const root = fixture();
    const errors: string[] = [];
    const originalError = console.error;
    const runner: CommandRunner = {
      async run(command) {
        const joined = command.join(" ");
        if (joined === "git -C " + root + " rev-parse HEAD") return { code: 0, stdout: "a1b2c3d4e5f6\n", stderr: "" };
        if (joined === "git -C " + root + " status --porcelain") return { code: 0, stdout: "", stderr: "" };
        if (command[0] === "sh") {
          const match = command[2]?.match(/tar -x -C ([^ ]+)/);
          if (match) {
            mkdirSync(match[1], { recursive: true });
            writeFileSync(join(match[1], "bun.lock"), "\n");
            writeFileSync(join(match[1], ".env"), "SECRET=1\n");
          }
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => {
      errors.push(String(message));
    };
    try {
      await main(["deploy", "production"], root, { runner });
    } finally {
      console.error = originalError;
    }

    expect(process.exitCode).toBe(1);
    expect(errors.join("\n")).toContain("refusing to deploy dotenv file: .env");
  });

  test("allows artifact dotenv files only with an explicit override", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-dotenv-test-"));
    writeFileSync(join(root, "worker.js"), `console.log("bundle");\n`);
    writeFileSync(
      join(root, "simple-vps.toml"),
      `
name = "worker"

[build]
command = "mkdir -p dist && cp worker.js dist/worker.js && printf SECRET=1 > dist/.env.production"
output = "dist"
install = false

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.worker]
command = "bun worker.js"
`,
    );
    await runLocal(["git", "-C", root, "init", "-q"]);
    await runLocal(["git", "-C", root, "config", "user.email", "smoke@example.com"]);
    await runLocal(["git", "-C", root, "config", "user.name", "Smoke"]);
    await runLocal(["git", "-C", root, "add", "."]);
    await runLocal(["git", "-C", root, "commit", "-q", "-m", "fixture"]);

    const errors: string[] = [];
    const warnings: string[] = [];
    const originalError = console.error;
    const runner: CommandRunner = {
      async run(command) {
        if (command[0] === "git" || command[0] === "sh") return runLocal(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => errors.push(String(message));
    try {
      await main(["deploy", "production"], root, { runner });
    } finally {
      console.error = originalError;
    }
    expect(process.exitCode).toBe(1);
    expect(errors.join("\n")).toContain("refusing to deploy dotenv file: .env.production");

    console.error = (message?: unknown) => warnings.push(String(message));
    try {
      await main(["deploy", "production", "--include-dotenv"], root, { runner });
    } finally {
      console.error = originalError;
    }
    expect(process.exitCode).toBe(0);
    expect(warnings.join("\n")).toContain("Warning: deploying dotenv file: .env.production");
  });

  test("allows dirty deploys with an explicitly marked release id", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        seedMockCheckout(command);
        const joined = command.join(" ");
        if (joined === "git -C " + root + " rev-parse HEAD") return { code: 0, stdout: "a1b2c3d4e5f6\n", stderr: "" };
        if (joined === "git -C " + root + " status --porcelain") return { code: 0, stdout: " M src/server.ts\n", stderr: "" };
        if (joined === "git -C " + root + " ls-tree -r --name-only HEAD") return { code: 0, stdout: "src/server.ts\n", stderr: "" };
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["deploy", "production", "--dirty"], root, { runner, now: () => new Date("2026-05-18T12:34:56Z") });

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(joined).toContain("ssh deploy@100.x.y.z install -d -m 2775 /var/apps/api/releases/a1b2c3d4e5f6-dirty-20260518123456");
    expect(
      joined.some((command) =>
        command.startsWith("sh -c tar -C " + root + " --exclude .git --exclude node_modules -cf - . | tar -x -C "),
      ),
    ).toBe(true);
  });

  test("deploys a build output artifact with explicit includes", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-build-test-"));
    mkdirSync(join(root, "public"), { recursive: true });
    writeFileSync(join(root, "bun.lock"), "\n");
    writeFileSync(join(root, "package.json"), `{"name":"web","version":"1.0.0"}\n`);
    writeFileSync(join(root, "server.js"), `console.log("built");\n`);
    writeFileSync(join(root, "public", "asset.txt"), "asset\n");
    writeFileSync(
      join(root, "simple-vps.toml"),
      `
name = "web"

[build]
command = "mkdir -p dist && cp server.js dist/server.js"
output = "dist"
include = ["public"]

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.web]
command = "bun server.js"
port = 3000
healthcheck = "/health"
`,
    );
    await runLocal(["git", "-C", root, "init", "-q"]);
    await runLocal(["git", "-C", root, "config", "user.email", "smoke@example.com"]);
    await runLocal(["git", "-C", root, "config", "user.name", "Smoke"]);
    await runLocal(["git", "-C", root, "add", "."]);
    await runLocal(["git", "-C", root, "commit", "-q", "-m", "fixture"]);

    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        if (command[0] === "git" || command[0] === "sh") return runLocal(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["deploy", "production"], root, { runner });

    const joined = commands.map((command) => command.join(" "));
    const rsyncCommand = commands.find((command) => command[0] === "rsync" && command[2] === "--delete");
    const artifactRoot = rsyncCommand?.[3]?.replace(/\/$/, "");
    expect(process.exitCode).toBe(0);
    expect(joined.some((command) => command.startsWith("sh -c cd ") && command.includes("mkdir -p dist"))).toBe(true);
    expect(artifactRoot).toBeDefined();
    expect(readFileSync(join(artifactRoot!, "server.js"), "utf8")).toContain("built");
    expect(readFileSync(join(artifactRoot!, "public", "asset.txt"), "utf8")).toBe("asset\n");
    expect(existsSync(join(artifactRoot!, "package.json"))).toBe(true);
    expect(existsSync(join(artifactRoot!, "bun.lock"))).toBe(true);
    expect(existsSync(join(artifactRoot!, "simple-vps.toml"))).toBe(false);
    expect(joined.some((command) => command.includes("sudo simple-vps app run-as web"))).toBe(true);
  });

  test("deploys a bundled build without server install", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-bundle-test-"));
    writeFileSync(join(root, "worker.js"), `console.log("bundle");\n`);
    writeFileSync(
      join(root, "simple-vps.toml"),
      `
name = "worker"

[build]
command = "mkdir -p dist && cp worker.js dist/worker.js"
output = "dist"
install = false

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.worker]
command = "bun worker.js"
`,
    );
    await runLocal(["git", "-C", root, "init", "-q"]);
    await runLocal(["git", "-C", root, "config", "user.email", "smoke@example.com"]);
    await runLocal(["git", "-C", root, "config", "user.name", "Smoke"]);
    await runLocal(["git", "-C", root, "add", "."]);
    await runLocal(["git", "-C", root, "commit", "-q", "-m", "fixture"]);

    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        if (command[0] === "git" || command[0] === "sh") return runLocal(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["deploy", "production"], root, { runner });

    const joined = commands.map((command) => command.join(" "));
    const rsyncCommand = commands.find((command) => command[0] === "rsync" && command[2] === "--delete");
    const artifactRoot = rsyncCommand?.[3]?.replace(/\/$/, "");
    expect(process.exitCode).toBe(0);
    expect(artifactRoot).toBeDefined();
    expect(readFileSync(join(artifactRoot!, "worker.js"), "utf8")).toContain("bundle");
    expect(existsSync(join(artifactRoot!, "simple-vps.toml"))).toBe(false);
    expect(joined.some((command) => command.includes("sudo simple-vps app run-as worker"))).toBe(false);
  });

  test("refuses artifact symlinks that point outside the artifact root", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-symlink-test-"));
    mkdirSync(join(root, "dist"), { recursive: true });
    writeFileSync(join(root, "dist", "server.js"), `console.log("bundle");\n`);
    symlinkSync("/etc/passwd", join(root, "dist", "leak"));
    writeFileSync(
      join(root, "simple-vps.toml"),
      `
name = "worker"

[build]
command = "true"
output = "dist"
install = false

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"

[services.worker]
command = "bun server.js"
`,
    );
    await runLocal(["git", "-C", root, "init", "-q"]);
    await runLocal(["git", "-C", root, "config", "user.email", "smoke@example.com"]);
    await runLocal(["git", "-C", root, "config", "user.name", "Smoke"]);
    await runLocal(["git", "-C", root, "add", "."]);
    await runLocal(["git", "-C", root, "commit", "-q", "-m", "fixture"]);

    const errors: string[] = [];
    const originalError = console.error;
    const runner: CommandRunner = {
      async run(command) {
        if (command[0] === "git" || command[0] === "sh") return runLocal(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => errors.push(String(message));
    try {
      await main(["deploy", "production"], root, { runner });
    } finally {
      console.error = originalError;
    }

    expect(process.exitCode).toBe(1);
    expect(errors.join("\n")).toContain("refusing to deploy unsafe symlink: leak -> /etc/passwd");
  });
});
