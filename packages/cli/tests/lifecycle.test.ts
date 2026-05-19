import { mkdtempSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { afterEach, describe, expect, test } from "bun:test";
import { main, type CommandRunner } from "../src/cli";

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), "simple-vps-lifecycle-test-"));
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

afterEach(() => {
  process.exitCode = 0;
});

describe("lifecycle commands", () => {
  test("status prints current release, service states, and app routes", async () => {
    const root = fixture();
    const output: string[] = [];
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command) {
        const joined = command.join(" ");
        if (joined === "ssh deploy@100.x.y.z readlink -f /var/apps/api/current 2>/dev/null || true") {
          return { code: 0, stdout: "/var/apps/api/releases/a1b2c3d4\n", stderr: "" };
        }
        if (joined === "ssh deploy@100.x.y.z sudo simple-vps route list --json") {
          return {
            code: 0,
            stdout: JSON.stringify({ routes: [{ host: "api.example.com", type: "proxy", port: 3000, app: "api" }] }),
            stderr: "",
          };
        }
        if (joined === "ssh deploy@100.x.y.z sudo simple-vps app service is-active api web") {
          return { code: 0, stdout: "active\n", stderr: "" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["status", "production"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(output).toEqual(["api (production)", "current: a1b2c3d4", "service web: active", "route api.example.com: proxy"]);
  });

  test("logs reads journalctl and tail uses passthrough", async () => {
    const root = fixture();
    const output: string[] = [];
    const commands: Array<{ command: string[]; passthrough: boolean }> = [];
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command, options) {
        commands.push({ command, passthrough: options?.passthrough === true });
        return { code: 0, stdout: "line one\nline two\n", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["logs", "production", "web"], root, { runner });
      await main(["logs", "production", "web", "--tail"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(output).toEqual(["line one\nline two"]);
    expect(commands[0]).toEqual({
      command: ["ssh", "deploy@100.x.y.z", "journalctl -u simple-api-web.service -n 200 --no-pager"],
      passthrough: false,
    });
    expect(commands[1]).toEqual({
      command: ["ssh", "deploy@100.x.y.z", "journalctl -u simple-api-web.service -f"],
      passthrough: true,
    });
  });

  test("rollback activates the previous successful release without touching routes", async () => {
    const root = fixture();
    const output: string[] = [];
    const commands: string[][] = [];
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        const joined = command.join(" ");
        if (joined.includes("for dir in $(ls -1dt /var/apps/api/releases/*")) {
          return { code: 0, stdout: "/var/apps/api/releases/oldsha\n", stderr: "" };
        }
        if (joined === "ssh deploy@100.x.y.z readlink -f /var/apps/api/current") {
          return { code: 0, stdout: "/var/apps/api/releases/newsha\n", stderr: "" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["rollback", "production"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service stop api web");
    expect(joined).toContain("ssh deploy@100.x.y.z ln -sfn /var/apps/api/releases/oldsha /var/apps/api/current");
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service start api web");
    expect(joined).toContain("ssh deploy@100.x.y.z touch /var/apps/api/releases/oldsha/.simple-deploy-success");
    expect(joined).not.toContain("ssh deploy@100.x.y.z sudo simple-vps route proxy api.example.com --port 3000 --app api");
    expect(output).toEqual(["Rolled back api to oldsha (production)"]);
  });

  test("rollback accepts an explicit release", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["rollback", "production", "a1b2c3d4"], root, { runner });

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(joined).toContain("ssh deploy@100.x.y.z test -d /var/apps/api/releases/a1b2c3d4");
    expect(joined).toContain("ssh deploy@100.x.y.z ln -sfn /var/apps/api/releases/a1b2c3d4 /var/apps/api/current");
    expect(joined).toContain("ssh deploy@100.x.y.z touch /var/apps/api/releases/a1b2c3d4/.simple-deploy-success");
  });

  test("destroy removes routes and units but preserves app data by default", async () => {
    const root = fixture();
    const output: string[] = [];
    const commands: string[][] = [];
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["destroy", "production", "--yes"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service stop api web || true");
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service disable api web || true");
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app uninstall-unit api web");
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app daemon-reload");
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps route remove --app api");
    expect(joined).toContain("ssh deploy@100.x.y.z rm -f /var/apps/api/current");
    expect(joined).not.toContain("ssh deploy@100.x.y.z sudo simple-vps app destroy api");
    expect(output).toEqual(["Destroyed api (production), preserved /var/apps/api/shared and /var/apps/api/releases"]);
  });

  test("destroy purge requires yes and a typed app confirmation", async () => {
    const root = fixture();
    const errors: string[] = [];
    const originalError = console.error;
    const runner: CommandRunner = {
      async run() {
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => errors.push(String(message));
    try {
      await main(["destroy", "production", "--purge", "--yes"], root, { runner });
    } finally {
      console.error = originalError;
    }

    expect(process.exitCode).toBe(1);
    expect(errors.join("\n")).toContain("destroy --purge requires --yes --confirm api");
  });

  test("destroy purge removes app data after confirmation", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["destroy", "production", "--purge", "--yes", "--confirm", "api"], root, { runner });

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app destroy api");
  });

  test("restart restarts one service and health-checks it", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["restart", "production", "web"], root, { runner });

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(joined).toContain("ssh deploy@100.x.y.z sudo simple-vps app service restart api web");
    expect(joined).toContain(
      "ssh deploy@100.x.y.z for i in $(seq 1 10); do status=$(curl -o /dev/null -s -w '%{http_code}' --max-time 2 http://127.0.0.1:3000/health || true); [ \"$status\" = \"200\" ] && exit 0; sleep 1; done; exit 1",
    );
  });
});
