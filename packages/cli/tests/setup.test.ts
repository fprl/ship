import { mkdtempSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { afterEach, describe, expect, test } from "bun:test";
import { main, type CommandRunner } from "../src/cli";

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), "simple-vps-setup-test-"));
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

describe("setup", () => {
  test("prepares the app over SSH using the Simple VPS server API", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["setup", "production"], root, { runner });

    expect(process.exitCode).toBe(0);
    expect(commands).toEqual([
      ["ssh", "deploy@100.x.y.z", "true"],
      ["ssh", "deploy@100.x.y.z", "command -v simple-vps"],
      ["ssh", "deploy@100.x.y.z", "command -v rsync"],
      ["ssh", "deploy@100.x.y.z", "command -v bun"],
      ["ssh", "deploy@100.x.y.z", "sudo simple-vps app create api"],
    ]);
  });

  test("points to Simple VPS install when the server API is missing", async () => {
    const root = fixture();
    const errors: string[] = [];
    const originalError = console.error;
    const runner: CommandRunner = {
      async run(command) {
        if (command[2] === "command -v simple-vps") {
          return { code: 1, stdout: "", stderr: "simple-vps not found" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => {
      errors.push(String(message));
    };
    try {
      await main(["setup", "production"], root, { runner });
    } finally {
      console.error = originalError;
    }

    expect(process.exitCode).toBe(1);
    expect(errors.join("\n")).toContain("rerun the Simple VPS install");
  });

  test("uses CI-provided SSH key and known_hosts for server commands", async () => {
    const root = fixture();
    const commands: string[][] = [];
    let keyContent = "";
    let keyMode = 0;
    let knownHostsContent = "";
    const previousKey = process.env.SIMPLE_VPS_SSH_KEY;
    const previousKnownHosts = process.env.SIMPLE_VPS_KNOWN_HOSTS;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        const keyIndex = command.indexOf("-i");
        const knownHostsOption = command.find((arg) => arg.startsWith("UserKnownHostsFile="));
        if (keyIndex !== -1 && knownHostsOption) {
          const keyPath = command[keyIndex + 1];
          const knownHostsPath = knownHostsOption.split("=", 2)[1] ?? "";
          keyContent = readFileSync(keyPath, "utf8");
          keyMode = statSync(keyPath).mode & 0o777;
          knownHostsContent = readFileSync(knownHostsPath, "utf8");
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    process.env.SIMPLE_VPS_SSH_KEY = "test-private-key";
    process.env.SIMPLE_VPS_KNOWN_HOSTS = "100.x.y.z ssh-ed25519 AAAA";
    try {
      await main(["setup", "production"], root, { runner });
    } finally {
      if (previousKey === undefined) delete process.env.SIMPLE_VPS_SSH_KEY;
      else process.env.SIMPLE_VPS_SSH_KEY = previousKey;
      if (previousKnownHosts === undefined) delete process.env.SIMPLE_VPS_KNOWN_HOSTS;
      else process.env.SIMPLE_VPS_KNOWN_HOSTS = previousKnownHosts;
    }

    const first = commands[0];
    expect(process.exitCode).toBe(0);
    expect(first).toContain("StrictHostKeyChecking=yes");
    expect(first).toContain("IdentitiesOnly=yes");
    expect(keyContent).toBe("test-private-key\n");
    expect(keyMode).toBe(0o600);
    expect(knownHostsContent).toBe("100.x.y.z ssh-ed25519 AAAA\n");
  });

  test("refuses CI SSH key without known_hosts", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const errors: string[] = [];
    const originalError = console.error;
    const previousKey = process.env.SIMPLE_VPS_SSH_KEY;
    const previousKnownHosts = process.env.SIMPLE_VPS_KNOWN_HOSTS;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    process.env.SIMPLE_VPS_SSH_KEY = "test-private-key";
    delete process.env.SIMPLE_VPS_KNOWN_HOSTS;
    console.error = (message?: unknown) => errors.push(String(message));
    try {
      await main(["setup", "production"], root, { runner });
    } finally {
      console.error = originalError;
      if (previousKey === undefined) delete process.env.SIMPLE_VPS_SSH_KEY;
      else process.env.SIMPLE_VPS_SSH_KEY = previousKey;
      if (previousKnownHosts === undefined) delete process.env.SIMPLE_VPS_KNOWN_HOSTS;
      else process.env.SIMPLE_VPS_KNOWN_HOSTS = previousKnownHosts;
    }

    expect(process.exitCode).toBe(1);
    expect(commands).toHaveLength(0);
    expect(errors.join("\n")).toContain("SIMPLE_VPS_KNOWN_HOSTS is required");
  });
});
