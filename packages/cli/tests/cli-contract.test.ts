import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { main, type CommandRunner } from "../src/cli";

function writeManifest(root: string) {
  writeFileSync(join(root, "bun.lock"), "\n");
  writeFileSync(
    join(root, "simple-vps.toml"),
    `
name = "api"

[env.production]
server = "deploy@100.x.y.z"
runtime = "bun"
`,
  );
}

afterEach(() => {
  process.exitCode = 0;
});

describe("public CLI contract", () => {
  test("help advertises simple-vps commands only", async () => {
    const lines: string[] = [];
    const originalError = console.error;
    console.error = (message?: unknown) => lines.push(String(message));
    try {
      await main(["--help"]);
    } finally {
      console.error = originalError;
    }

    const output = lines.join("\n");
    expect(output).toContain("simple-vps deploy <env> [--dirty] [--include-dotenv]");
    expect(output).not.toContain("simple-deploy");
  });

  test("package exposes the simple-vps binary only", () => {
    const packageJson = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")) as {
      name?: string;
      bin?: Record<string, string>;
    };

    expect(packageJson.name).toBe("simple-vps");
    expect(packageJson.bin).toEqual({ "simple-vps": "./src/cli.ts" });
  });

  test("init template uses deploy identity", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const originalLog = console.log;

    console.log = () => {};
    try {
      await main(["init"], root);
    } finally {
      console.log = originalLog;
    }

    const manifest = readFileSync(join(root, "simple-vps.toml"), "utf8");
    expect(manifest).toContain('server = "deploy@100.x.y.z"');
  });

  test("init inspects lockfile and package scripts", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const originalLog = console.log;
    writeFileSync(
      join(root, "package.json"),
      JSON.stringify({
        name: "@acme/web",
        scripts: { build: "vite build", start: "node dist/server.js" },
      }),
    );
    writeFileSync(join(root, "package-lock.json"), "{}\n");

    console.log = () => {};
    try {
      await main(["init"], root);
    } finally {
      console.log = originalLog;
    }

    const manifest = readFileSync(join(root, "simple-vps.toml"), "utf8");
    expect(manifest).toContain('name = "web"');
    expect(manifest).toContain('runtime = "node"');
    expect(manifest).toContain('command = "npm run build"');
    expect(manifest).toContain('command = "npm run start"');
  });

  test("check accepts an optional positional env", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const output: string[] = [];
    const originalLog = console.log;
    writeManifest(root);

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["check", "production"], root);
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(output).toEqual(["simple-vps.toml OK (envs: production)"]);
  });

  test("ssh opens the selected env server with passthrough", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const commands: Array<{ command: string[]; passthrough: boolean | undefined }> = [];
    const runner: CommandRunner = {
      async run(command, options) {
        commands.push({ command, passthrough: options?.passthrough });
        return { code: 0, stdout: "", stderr: "" };
      },
    };
    writeManifest(root);

    await main(["ssh", "production"], root, { runner });

    expect(process.exitCode).toBe(0);
    expect(commands).toEqual([{ command: ["ssh", "deploy@100.x.y.z"], passthrough: true }]);
  });

  test("route list reads routes from the selected host", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const output: string[] = [];
    const originalLog = console.log;
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: '{"routes":[]}\n', stderr: "" };
      },
    };
    writeManifest(root);

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["route", "list", "--json"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(commands).toEqual([["ssh", "deploy@100.x.y.z", "sudo simple-vps route list --json"]]);
    expect(output).toEqual(['{"routes":[]}']);
  });

  test("route list accepts an explicit server without a manifest", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const output: string[] = [];
    const originalLog = console.log;
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "No routes configured.\n", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["route", "list", "--server", "deploy@100.x.y.z"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(commands).toEqual([["ssh", "deploy@100.x.y.z", "sudo simple-vps route list"]]);
    expect(output).toEqual(["No routes configured."]);
  });

  test("route list rejects server values that look like ssh options", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const errors: string[] = [];
    const originalError = console.error;
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => errors.push(String(message));
    try {
      await main(["route", "list", "--server", "-oProxyCommand=sh"], root, { runner });
    } finally {
      console.error = originalError;
    }

    expect(process.exitCode).toBe(1);
    expect(commands).toEqual([]);
    expect(errors.join("\n")).toContain("--server must be an SSH target like deploy@example.com");
  });

  test("host status reads server status from the selected host", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const output: string[] = [];
    const originalLog = console.log;
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "Simple VPS status\n", stderr: "" };
      },
    };
    writeManifest(root);

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["host", "status"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(commands).toEqual([["ssh", "deploy@100.x.y.z", "sudo simple-vps status"]]);
    expect(output).toEqual(["Simple VPS status"]);
  });

  test("host status accepts an explicit server without a manifest", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const output: string[] = [];
    const originalLog = console.log;
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "Simple VPS status\n", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["host", "status", "--server", "deploy@100.x.y.z"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(commands).toEqual([["ssh", "deploy@100.x.y.z", "sudo simple-vps status"]]);
    expect(output).toEqual(["Simple VPS status"]);
  });

  test("host doctor runs server doctor command", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const output: string[] = [];
    const originalLog = console.log;
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "Simple VPS doctor\nidentity: healthy\n", stderr: "" };
      },
    };
    writeManifest(root);

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["host", "doctor"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(commands).toEqual([["ssh", "deploy@100.x.y.z", "sudo simple-vps doctor"]]);
    expect(output).toEqual(["Simple VPS doctor\nidentity: healthy"]);
  });

  test("host doctor prints degraded output before failing", async () => {
    const root = mkdtempSync(join(tmpdir(), "simple-vps-contract-test-"));
    const output: string[] = [];
    const errors: string[] = [];
    const originalLog = console.log;
    const originalError = console.error;
    const commands: string[][] = [];
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 1, stdout: "Simple VPS doctor\nidentity: degraded\n", stderr: "legacy admin conflation" };
      },
    };
    writeManifest(root);

    console.log = (message?: unknown) => output.push(String(message));
    console.error = (message?: unknown) => errors.push(String(message));
    try {
      await main(["host", "doctor"], root, { runner });
    } finally {
      console.log = originalLog;
      console.error = originalError;
    }

    expect(process.exitCode).toBe(1);
    expect(commands).toEqual([["ssh", "deploy@100.x.y.z", "sudo simple-vps doctor"]]);
    expect(output).toEqual(["Simple VPS doctor\nidentity: degraded"]);
    expect(errors.join("\n")).toContain("host doctor reported degraded: legacy admin conflation");
  });
});
