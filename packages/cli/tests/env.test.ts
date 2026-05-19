import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { afterEach, describe, expect, test } from "bun:test";
import { main, type CommandRunner } from "../src/cli";

function fixture(): string {
  const root = mkdtempSync(join(tmpdir(), "simple-vps-env-test-"));
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
`,
  );
  return root;
}

afterEach(() => {
  process.exitCode = 0;
});

describe("env and secret commands", () => {
  test("env push validates and atomically installs shared env through simple-vps", async () => {
    const root = fixture();
    const envFile = join(root, "production.env");
    writeFileSync(envFile, "# prod\nFEATURE_FLAG=on\nEMPTY=\n");
    const commands: string[][] = [];
    const output: string[] = [];
    let uploaded = "";
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        if (command[0] === "rsync") uploaded = readFileSync(command[2], "utf8");
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["env", "push", "production", envFile], root, { runner });
    } finally {
      console.log = originalLog;
    }

    const joined = commands.map((command) => command.join(" "));
    expect(process.exitCode).toBe(0);
    expect(uploaded).toBe("# prod\nFEATURE_FLAG=on\nEMPTY=\n");
    expect(joined.some((command) => command.startsWith("rsync -az ") && command.includes("deploy@100.x.y.z:/tmp/simple-deploy/"))).toBe(
      true,
    );
    expect(joined.some((command) => command.includes("sudo simple-vps app install-env api /tmp/simple-deploy/"))).toBe(true);
    expect(output.join("\n")).toContain("Run simple-vps restart production <service> to apply.");
    expect(output.join("\n")).not.toContain("simple-deploy restart");
  });

  test("env push rejects shell-style env files before upload", async () => {
    const root = fixture();
    const envFile = join(root, "bad.env");
    writeFileSync(envFile, "export API_KEY=secret\n");
    const commands: string[][] = [];
    const errors: string[] = [];
    const originalError = console.error;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.error = (message?: unknown) => errors.push(String(message));
    try {
      await main(["env", "push", "production", envFile], root, { runner });
    } finally {
      console.error = originalError;
    }

    expect(process.exitCode).toBe(1);
    expect(errors.join("\n")).toContain("line 1: export is not supported");
    expect(commands).toHaveLength(0);
  });

  test("env push uses CI SSH auth for rsync uploads", async () => {
    const root = fixture();
    const envFile = join(root, "production.env");
    writeFileSync(envFile, "FEATURE_FLAG=on\n");
    const commands: string[][] = [];
    const previousKey = process.env.SIMPLE_VPS_SSH_KEY;
    const previousKnownHosts = process.env.SIMPLE_VPS_KNOWN_HOSTS;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    process.env.SIMPLE_VPS_SSH_KEY = "test-private-key";
    process.env.SIMPLE_VPS_KNOWN_HOSTS = "100.x.y.z ssh-ed25519 AAAA";
    try {
      await main(["env", "push", "production", envFile], root, { runner });
    } finally {
      if (previousKey === undefined) delete process.env.SIMPLE_VPS_SSH_KEY;
      else process.env.SIMPLE_VPS_SSH_KEY = previousKey;
      if (previousKnownHosts === undefined) delete process.env.SIMPLE_VPS_KNOWN_HOSTS;
      else process.env.SIMPLE_VPS_KNOWN_HOSTS = previousKnownHosts;
    }

    const rsync = commands.find((command) => command[0] === "rsync");
    expect(rsync).toBeDefined();
    expect(rsync?.[1]).toBe("-e");
    expect(rsync?.[2]).toContain("StrictHostKeyChecking=yes");
    expect(rsync?.[2]).toContain("UserKnownHostsFile=");
  });

  test("secret put reads stdin and updates one key without printing the value", async () => {
    const root = fixture();
    const output: string[] = [];
    const commands: string[][] = [];
    let uploaded = "";
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        const joined = command.join(" ");
        if (joined === "ssh deploy@100.x.y.z sudo simple-vps app read-env api") {
          return { code: 0, stdout: "# keep\nAPI_KEY=old\nOTHER=value\n", stderr: "" };
        }
        if (command[0] === "rsync") uploaded = readFileSync(command[2], "utf8");
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["secret", "put", "production", "API_KEY"], root, { runner, stdinText: "new-secret\n" });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(uploaded).toBe("# keep\nAPI_KEY=new-secret\nOTHER=value\n");
    expect(output.join("\n")).toContain("Set secret API_KEY");
    expect(output.join("\n")).toContain("Run simple-vps restart production <service> to apply.");
    expect(output.join("\n")).not.toContain("new-secret");
  });

  test("secret list prints names only", async () => {
    const root = fixture();
    const output: string[] = [];
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command) {
        if (command.join(" ") === "ssh deploy@100.x.y.z sudo simple-vps app read-env api") {
          return { code: 0, stdout: "API_KEY=secret\nOTHER=value\n", stderr: "" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["secret", "list", "production"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(output).toEqual(["API_KEY", "OTHER"]);
    expect(output.join("\n")).not.toContain("secret");
  });

  test("secret rm removes the key atomically", async () => {
    const root = fixture();
    const commands: string[][] = [];
    let uploaded = "";
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        if (command.join(" ") === "ssh deploy@100.x.y.z sudo simple-vps app read-env api") {
          return { code: 0, stdout: "API_KEY=secret\nOTHER=value\n", stderr: "" };
        }
        if (command[0] === "rsync") uploaded = readFileSync(command[2], "utf8");
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    await main(["secret", "rm", "production", "API_KEY"], root, { runner });

    expect(process.exitCode).toBe(0);
    expect(uploaded).toBe("OTHER=value\n");
    expect(commands.map((command) => command.join(" ")).some((command) => command.includes("sudo simple-vps app install-env api"))).toBe(
      true,
    );
  });

  test("secret rm does not upload unchanged env when the key is absent", async () => {
    const root = fixture();
    const commands: string[][] = [];
    const output: string[] = [];
    const originalLog = console.log;
    const runner: CommandRunner = {
      async run(command) {
        commands.push(command);
        if (command.join(" ") === "ssh deploy@100.x.y.z sudo simple-vps app read-env api") {
          return { code: 0, stdout: "OTHER=value\n", stderr: "" };
        }
        return { code: 0, stdout: "", stderr: "" };
      },
    };

    console.log = (message?: unknown) => output.push(String(message));
    try {
      await main(["secret", "rm", "production", "API_KEY"], root, { runner });
    } finally {
      console.log = originalLog;
    }

    expect(process.exitCode).toBe(0);
    expect(commands.map((command) => command[0])).toEqual(["ssh"]);
    expect(output.join("\n")).toContain("Secret API_KEY was not set");
  });
});
