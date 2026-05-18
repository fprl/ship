import { mkdtempSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { afterEach, describe, expect, test } from "bun:test";
import { main, type CommandRunner } from "../src/cli";

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), "simple-deploy-deploy-test-"));
  writeFileSync(join(root, "bun.lock"), "\n");
  writeFileSync(
    join(root, "simple-deploy.toml"),
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
  return root;
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
    expect(joined).toContain("ssh admin@100.x.y.z test -d /var/apps/api/shared");
    expect(joined).toContain("ssh admin@100.x.y.z mkdir -p /var/apps/api/releases/a1b2c3d4e5f6");
    expect(joined.some((command) => command.startsWith("sh -c git -C " + root + " archive HEAD | tar -x -C "))).toBe(
      true,
    );
    expect(joined.some((command) => command.startsWith("rsync -az --delete "))).toBe(true);
    expect(joined).toContain("ssh admin@100.x.y.z ln -sfn /var/apps/api/shared/.env /var/apps/api/releases/a1b2c3d4e5f6/.env");
    expect(joined).toContain("ssh admin@100.x.y.z ln -sfn /var/apps/api/shared/db /var/apps/api/releases/a1b2c3d4e5f6/db");
    expect(joined).toContain(
      "ssh admin@100.x.y.z ln -sfn /var/apps/api/shared/storage /var/apps/api/releases/a1b2c3d4e5f6/storage",
    );
    expect(joined).toContain("ssh admin@100.x.y.z ln -sfn /var/apps/api/shared/logs /var/apps/api/releases/a1b2c3d4e5f6/logs");
    expect(joined).toContain(
      "ssh admin@100.x.y.z sudo simple-vps app run-as api --cwd /var/apps/api/releases/a1b2c3d4e5f6 -- bun install --production --frozen-lockfile",
    );
    expect(joined.some((command) => command.startsWith("rsync -az ") && command.includes("admin@100.x.y.z:/tmp/simple-deploy/"))).toBe(
      true,
    );
    expect(joined.some((command) => command.includes("sudo simple-vps app install-unit api web "))).toBe(true);
    expect(joined).toContain("ssh admin@100.x.y.z sudo simple-vps app service start api web");
    expect(joined).toContain(
      "ssh admin@100.x.y.z for i in $(seq 1 10); do status=$(curl -o /dev/null -s -w '%{http_code}' --max-time 2 http://127.0.0.1:3000/health || true); [ \"$status\" = \"200\" ] && exit 0; sleep 1; done; exit 1",
    );
    expect(joined).toContain("ssh admin@100.x.y.z sudo simple-vps route proxy api.example.com --port 3000 --app api");
  });

  test("rolls current back and does not publish routes when health check fails", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        const joined = command.join(" ");
        if (joined === "git -C " + root + " rev-parse HEAD") return { code: 0, stdout: "a1b2c3d4e5f6\n", stderr: "" };
        if (joined === "git -C " + root + " status --porcelain") return { code: 0, stdout: "", stderr: "" };
        if (joined === "ssh admin@100.x.y.z readlink -f /var/apps/api/current") {
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
    expect(joined).toContain("ssh admin@100.x.y.z sudo simple-vps app service stop api web");
    expect(joined).toContain("ssh admin@100.x.y.z ln -sfn /var/apps/api/releases/oldsha /var/apps/api/current");
    expect(joined).toContain("ssh admin@100.x.y.z sudo simple-vps app service start api web");
    expect(joined).not.toContain("ssh admin@100.x.y.z sudo simple-vps route proxy api.example.com --port 3000 --app api");
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
        if (joined === "git -C " + root + " ls-tree -r --name-only HEAD") return { code: 0, stdout: ".env\nsrc/server.ts\n", stderr: "" };
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
});
