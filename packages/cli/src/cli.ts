#!/usr/bin/env bun
import {
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, isAbsolute, join, relative, resolve as resolvePath } from "node:path";
import { tmpdir } from "node:os";

type Dict = Record<string, unknown>;

type AppContext = {
  manifest: Dict;
  appName: string;
  env: Dict;
  envName: string;
  server: string;
  appRoot: string;
  runtime: string;
  build: Dict | undefined;
  services: Record<string, Dict>;
  routes: Record<string, Dict>;
};

type ManifestModel = {
  manifest: Dict;
  appName?: string;
  envs: Record<string, Dict>;
  envNames: string[];
};

type EnvTarget = {
  manifest: Dict;
  appName?: string;
  env: Dict;
  envName: string;
  server?: string;
  appRoot?: string;
  runtime?: string;
  build: Dict | undefined;
  services: Record<string, Dict>;
  routes: Record<string, Dict>;
};

type DestroyOptions = {
  purge: boolean;
  yes: boolean;
  confirm?: string;
};

export type CheckResult = {
  errors: string[];
  warnings: string[];
  envs: string[];
};

export type CommandResult = {
  code: number;
  stdout: string;
  stderr: string;
};

export type CommandRunner = {
  run(command: string[], options?: { passthrough?: boolean }): Promise<CommandResult>;
};

type ReadTarget = {
  server: string;
};

export type MainOptions = {
  runner?: CommandRunner;
  now?: () => Date;
  stdinText?: string;
};

const APP_RE = /^[a-z][a-z0-9-]{1,40}$/;
const SERVICE_RE = /^[a-z][a-z0-9-]{0,30}$/;
const HOST_RE =
  /^(?=.{1,253}$)(?!-)[a-z0-9-]{1,63}(?<!-)(?:\.(?!-)[a-z0-9-]{1,63}(?<!-))*$/;
const SSH_TARGET_RE = new RegExp(
  `^(?:[a-z_][a-z0-9_-]{0,31}\\$?@)?${HOST_RE.source.slice(1, -1)}$`,
);
const RUNTIMES = new Set(["bun", "node", "static"]);
const ROUTE_TYPES = new Set(["proxy", "static", "redirect"]);
const RESERVED_SERVICES = new Set(["current", "releases", "shared"]);
const LOCKFILES = ["bun.lock", "bun.lockb", "pnpm-lock.yaml", "package-lock.json", "yarn.lock"];
const ALLOWED_DOTENV_FILES = new Set([".env.example", ".env.sample", ".env.defaults"]);
const COPY_OPTIONS = { recursive: true, verbatimSymlinks: true };
const MANIFEST_FILE = "simple-vps.toml";
// Internal server API contract retained for 0.2.0; not a public product name.
const REMOTE_DEPLOY_TMP_DIR = "/tmp/simple-deploy";
const RELEASE_SUCCESS_MARKER = ".simple-deploy-success";

const defaultRunner: CommandRunner = {
  async run(command, options) {
    const proc = Bun.spawn(command, {
      stdin: options?.passthrough ? "inherit" : "ignore",
      stdout: options?.passthrough ? "inherit" : "pipe",
      stderr: options?.passthrough ? "inherit" : "pipe",
    });
    if (options?.passthrough) {
      const code = await proc.exited;
      return { code, stdout: "", stderr: "" };
    }
    const [stdout, stderr, code] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
      proc.exited,
    ]);
    return { code, stdout, stderr };
  },
};

function isRecord(value: unknown): value is Dict {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

function asBoolean(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function asNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function parseToml(path: string): Dict {
  try {
    return Bun.TOML.parse(readFileSync(path, "utf8")) as Dict;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`failed to parse ${path}: ${message}`);
  }
}

function readManifest(root: string): Dict {
  const path = join(root, MANIFEST_FILE);
  if (!existsSync(path)) {
    throw new Error(`${MANIFEST_FILE} not found`);
  }
  return parseToml(path);
}

function manifestEnvBlocks(manifest: Dict): Record<string, Dict> {
  const envs: Record<string, Dict> = {};
  if (!isRecord(manifest.env)) return envs;
  for (const [name, env] of Object.entries(manifest.env)) {
    if (isRecord(env)) envs[name] = env;
  }
  return envs;
}

function manifestModel(manifest: Dict): ManifestModel {
  const envs = manifestEnvBlocks(manifest);
  return {
    manifest,
    appName: asString(manifest.name),
    envs,
    envNames: Object.keys(envs),
  };
}

function readManifestModel(root: string): ManifestModel {
  return manifestModel(readManifest(root));
}

function defaultAppRoot(appName: string): string {
  return `/var/apps/${appName}`;
}

function normalizeEnvTarget(model: ManifestModel, envName: string): EnvTarget | undefined {
  const env = model.envs[envName];
  if (!env) return undefined;
  return {
    manifest: model.manifest,
    appName: model.appName,
    env,
    envName,
    server: asString(env.server),
    appRoot: model.appName ? asString(env.path) ?? defaultAppRoot(model.appName) : undefined,
    runtime: asString(env.runtime),
    build: effectiveBuild(model.manifest, env),
    services: effectiveServices(model.manifest, env),
    routes: effectiveRoutes(model.manifest, env),
  };
}

function requireAppContext(target: EnvTarget): AppContext {
  const appName = requireString(target.appName, "name");
  const server = requireString(target.server, `[env.${target.envName}].server`);
  validateSshTarget(server, `[env.${target.envName}].server`);
  const appRoot = target.appRoot ?? defaultAppRoot(appName);
  const runtime = requireString(target.runtime, `[env.${target.envName}].runtime`);
  return {
    manifest: target.manifest,
    appName,
    env: target.env,
    envName: target.envName,
    server,
    appRoot,
    runtime,
    build: target.build,
    services: target.services,
    routes: target.routes,
  };
}

function mergeNamed(base: unknown, override: unknown): Record<string, Dict> {
  const merged: Record<string, Dict> = {};
  if (isRecord(base)) {
    for (const [name, value] of Object.entries(base)) {
      if (isRecord(value)) merged[name] = { ...value };
    }
  }
  if (isRecord(override)) {
    for (const [name, value] of Object.entries(override)) {
      if (!isRecord(value)) continue;
      merged[name] = { ...(merged[name] ?? {}), ...value };
    }
  }
  return merged;
}

function effectiveBuild(manifest: Dict, env: Dict): Dict | undefined {
  const base = isRecord(manifest.build) ? manifest.build : undefined;
  const override = isRecord(env.build) ? env.build : undefined;
  if (!base && !override) return undefined;
  return { ...(base ?? {}), ...(override ?? {}) };
}

function effectiveServices(manifest: Dict, env: Dict): Record<string, Dict> {
  return mergeNamed(manifest.services, env.services);
}

function effectiveRoutes(manifest: Dict, env: Dict): Record<string, Dict> {
  return mergeNamed(manifest.routes, env.routes);
}

function validateRelativePath(path: unknown, label: string, errors: string[]) {
  const value = asString(path);
  if (!value) {
    errors.push(`${label} must be a non-empty string`);
    return;
  }
  if (value.startsWith("/") || value.includes("..") || /[*?[\]{}]/.test(value)) {
    errors.push(`${label} must be a relative path without '..' or globs`);
  }
}

function validateBuild(build: Dict | undefined, root: string, errors: string[]) {
  if (!build) return;

  if (!asString(build.command)) errors.push("[build].command is required when [build] is present");
  validateRelativePath(build.output, "[build].output", errors);

  if (build.install !== undefined && asBoolean(build.install) === undefined) {
    errors.push("[build].install must be a boolean");
  }

  if (build.include === undefined) return;
  if (!Array.isArray(build.include)) {
    errors.push("[build].include must be an array of paths");
    return;
  }
  for (const [index, entry] of build.include.entries()) {
    const label = `[build].include[${index}]`;
    validateRelativePath(entry, label, errors);
    if (typeof entry === "string" && !existsSync(join(root, entry))) {
      errors.push(`${label} does not exist: ${entry}`);
    }
  }
}

function validateEnvTarget(target: EnvTarget, errors: string[]) {
  const { appName, env, envName, server, runtime } = target;
  if (!SERVICE_RE.test(envName)) errors.push(`invalid env name: ${envName}`);
  if (!server) errors.push(`[env.${envName}].server is required`);
  else if (server.startsWith("-") || !SSH_TARGET_RE.test(server)) {
    errors.push(`[env.${envName}].server must be an SSH target like deploy@example.com`);
  }
  if (env.path !== undefined) {
    const path = asString(env.path);
    if (!path) errors.push(`[env.${envName}].path must be a non-empty string when present`);
    else if (!path.startsWith("/")) errors.push(`[env.${envName}].path must be absolute`);
    else if (!appName) errors.push(`[env.${envName}].path requires a valid top-level name`);
    else if (path !== defaultAppRoot(appName)) errors.push(`[env.${envName}].path must be ${defaultAppRoot(appName)}`);
  }

  if (!runtime) errors.push(`[env.${envName}].runtime is required`);
  else if (!RUNTIMES.has(runtime)) errors.push(`[env.${envName}].runtime must be bun, node, or static`);

  const keepReleases = env.keep_releases;
  if (keepReleases !== undefined) {
    const value = asNumber(keepReleases);
    if (value === undefined || !Number.isInteger(value) || value < 1) {
      errors.push(`[env.${envName}].keep_releases must be a positive integer`);
    }
  }
}

function validateServices(services: Record<string, Dict>, runtime: string | undefined, errors: string[]) {
  const ports = new Map<number, string>();
  if (runtime === "static" && Object.keys(services).length > 0) {
    errors.push('runtime = "static" cannot declare services');
  }

  for (const [name, service] of Object.entries(services)) {
    if (!SERVICE_RE.test(name)) errors.push(`invalid service name: ${name}`);
    if (RESERVED_SERVICES.has(name)) errors.push(`reserved service name: ${name}`);
    if (!asString(service.command)) errors.push(`[services.${name}].command is required`);

    if (service.port !== undefined) {
      const port = asNumber(service.port);
      if (port === undefined || !Number.isInteger(port) || port < 1 || port > 65535) {
        errors.push(`[services.${name}].port must be an integer in [1, 65535]`);
      } else if (ports.has(port)) {
        errors.push(`[services.${name}].port duplicates [services.${ports.get(port)}].port`);
      } else {
        ports.set(port, name);
      }
      if (!asString(service.healthcheck)) errors.push(`[services.${name}].healthcheck is required when port is set`);
    }

    const timeout = service.healthcheck_timeout;
    if (timeout !== undefined) {
      const value = asNumber(timeout);
      if (value === undefined || value <= 0) errors.push(`[services.${name}].healthcheck_timeout must be positive`);
    }

    const status = service.healthcheck_status;
    if (status !== undefined) {
      const value = asNumber(status);
      if (value === undefined || !Number.isInteger(value) || value < 100 || value > 599) {
        errors.push(`[services.${name}].healthcheck_status must be an HTTP status code`);
      }
    }
  }
}

function validateRoutes(routes: Record<string, Dict>, services: Record<string, Dict>, errors: string[]) {
  for (const [name, route] of Object.entries(routes)) {
    if (!SERVICE_RE.test(name)) errors.push(`invalid route name: ${name}`);
    const host = asString(route.host);
    if (!host) errors.push(`[routes.${name}].host is required`);
    else if (!HOST_RE.test(host.toLowerCase().replace(/\.$/, ""))) errors.push(`[routes.${name}].host is invalid`);

    const type = asString(route.type);
    if (!type) errors.push(`[routes.${name}].type is required`);
    else if (!ROUTE_TYPES.has(type)) errors.push(`[routes.${name}].type must be proxy, static, or redirect`);

    if (type === "proxy") {
      const serviceName = asString(route.service);
      if (!serviceName) {
        errors.push(`[routes.${name}].service is required for proxy routes`);
      } else if (!services[serviceName]) {
        errors.push(`[routes.${name}].service references unknown service: ${serviceName}`);
      } else if (services[serviceName].port === undefined) {
        errors.push(`[routes.${name}].service must reference a service with a port`);
      }
    }

    if (type === "static" && route.root !== undefined) {
      errors.push(`[routes.${name}].root is not configurable in v1`);
    }

    if (type === "redirect") {
      const to = asString(route.to);
      if (!to) errors.push(`[routes.${name}].to is required for redirect routes`);
      else if (!to.startsWith("http://") && !to.startsWith("https://")) {
        errors.push(`[routes.${name}].to must start with http:// or https://`);
      }
    }
  }
}

function lockfilesIn(root: string): string[] {
  return LOCKFILES.filter((file) => existsSync(join(root, file)));
}

function packageManagerForLockfile(lockfile: string | undefined): string {
  if (lockfile === "bun.lock" || lockfile === "bun.lockb") return "bun";
  if (lockfile === "pnpm-lock.yaml") return "pnpm";
  if (lockfile === "yarn.lock") return "yarn";
  if (lockfile === "package-lock.json") return "npm";
  return "bun";
}

function runtimeForLockfile(lockfile: string | undefined): string {
  if (!lockfile) return "bun";
  return lockfile === "bun.lock" || lockfile === "bun.lockb" ? "bun" : "node";
}

function installCommandFor(lockfile: string): string {
  if (lockfile === "bun.lock" || lockfile === "bun.lockb") return "bun install --production --frozen-lockfile";
  if (lockfile === "pnpm-lock.yaml") return "pnpm install --prod --frozen-lockfile";
  if (lockfile === "package-lock.json") return "npm ci --omit=dev";
  if (lockfile === "yarn.lock") return "yarn install --production --frozen-lockfile";
  throw new Error(`unsupported lockfile: ${lockfile}`);
}

function installNeeded(runtime: string | undefined, build: Dict | undefined): boolean {
  if (runtime === "static") return false;
  if (!build) return true;
  return build.install !== false;
}

function requireString(value: unknown, label: string): string {
  const text = asString(value);
  if (!text) throw new Error(`${label} is required`);
  return text;
}

function shellEscape(value: string): string {
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(value)) return value;
  return `'${value.replaceAll("'", "'\\''")}'`;
}

function ensureTrailingNewline(value: string): string {
  return value.endsWith("\n") ? value : `${value}\n`;
}

function authCommand(command: string[], sshOptions: string[], rsyncRemoteShell: string): string[] {
  if (command[0] === "ssh") return ["ssh", ...sshOptions, ...command.slice(1)];
  if (command[0] === "rsync") return ["rsync", "-e", rsyncRemoteShell, ...command.slice(1)];
  return command;
}

function prepareCommandRunner(runner: CommandRunner): { runner: CommandRunner; cleanup: () => void } {
  const key = process.env.SIMPLE_VPS_SSH_KEY;
  if (!key) return { runner, cleanup: () => {} };

  const knownHosts = process.env.SIMPLE_VPS_KNOWN_HOSTS;
  if (!knownHosts) throw new Error("SIMPLE_VPS_KNOWN_HOSTS is required when SIMPLE_VPS_SSH_KEY is set");

  const dir = mkdtempSync(join(tmpdir(), "simple-vps-ssh-"));
  const keyPath = join(dir, "id");
  const knownHostsPath = join(dir, "known_hosts");
  writeFileSync(keyPath, ensureTrailingNewline(key), { encoding: "utf8", mode: 0o600 });
  writeFileSync(knownHostsPath, ensureTrailingNewline(knownHosts), { encoding: "utf8", mode: 0o600 });

  const sshOptions = [
    "-i",
    keyPath,
    "-o",
    "IdentitiesOnly=yes",
    "-o",
    "StrictHostKeyChecking=yes",
    "-o",
    `UserKnownHostsFile=${knownHostsPath}`,
  ];
  const rsyncRemoteShell = ["ssh", ...sshOptions].map(shellEscape).join(" ");
  return {
    runner: {
      run(command, options) {
        return runner.run(authCommand(command, sshOptions, rsyncRemoteShell), options);
      },
    },
    cleanup: () => rmSync(dir, { recursive: true, force: true }),
  };
}

function systemdEscape(value: string): string {
  return value.replaceAll("%", "%%");
}

function servicePort(service: Dict): number | undefined {
  return asNumber(service.port);
}

function healthCheckCommand(port: number, path: string, expectedStatus: number, timeout: number): string {
  return `for i in $(seq 1 ${timeout}); do status=$(curl -o /dev/null -s -w '%{http_code}' --max-time 2 http://127.0.0.1:${port}${path} || true); [ "$status" = "${expectedStatus}" ] && exit 0; sleep 1; done; exit 1`;
}

function unitName(appName: string, serviceName: string): string {
  return `simple-${appName}-${serviceName}.service`;
}

function releaseNameFromPath(path: string): string {
  return path.split("/").filter(Boolean).pop() ?? "none";
}

function keepReleasesFor(env: Dict): number {
  return asNumber(env.keep_releases) ?? 5;
}

function blockedDotenvFiles(paths: string[]): string[] {
  return paths.filter((path) => {
    const name = path.split("/").pop() ?? path;
    return name.startsWith(".env") && !ALLOWED_DOTENV_FILES.has(name);
  });
}

function envKeyFromLine(line: string): string | undefined {
  const trimmed = line.trim();
  if (!trimmed || trimmed.startsWith("#")) return undefined;
  const equals = line.indexOf("=");
  if (equals === -1) return undefined;
  return line.slice(0, equals).trim();
}

function validateEnvKey(key: string) {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw new Error(`invalid env key: ${key}`);
}

function validateEnvironmentFile(content: string) {
  if (content.includes("\0")) throw new Error("env file cannot contain NUL bytes");
  const lines = content.split(/\n/);
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index].replace(/\r$/, "");
    const number = index + 1;
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    if (trimmed.startsWith("export ")) throw new Error(`line ${number}: export is not supported`);
    const equals = line.indexOf("=");
    if (equals === -1) throw new Error(`line ${number}: expected KEY=value`);
    const key = line.slice(0, equals);
    if (key.trim() !== key) throw new Error(`line ${number}: whitespace around keys is not supported`);
    validateEnvKey(key);
    const value = line.slice(equals + 1);
    if (value.startsWith('"') || value.startsWith("'")) throw new Error(`line ${number}: quoted values are not supported`);
    if (/\s+#/.test(value)) throw new Error(`line ${number}: inline comments are not supported`);
  }
}

function envKeys(content: string): string[] {
  const keys: string[] = [];
  for (const rawLine of content.split(/\n/)) {
    const key = envKeyFromLine(rawLine.replace(/\r$/, ""));
    if (key) keys.push(key);
  }
  return keys;
}

function setEnvValue(content: string, key: string, value: string): string {
  validateEnvKey(key);
  if (/[\0\r\n]/.test(value)) throw new Error("secret value must be a single line");
  const lines = content ? content.replace(/\n$/, "").split(/\n/) : [];
  let replaced = false;
  const next: string[] = [];
  for (const rawLine of lines) {
    const line = rawLine.replace(/\r$/, "");
    if (envKeyFromLine(line) === key) {
      if (!replaced) next.push(`${key}=${value}`);
      replaced = true;
    } else {
      next.push(line);
    }
  }
  if (!replaced) next.push(`${key}=${value}`);
  return `${next.join("\n")}\n`;
}

function removeEnvValue(content: string, key: string): string {
  validateEnvKey(key);
  const lines = content ? content.replace(/\n$/, "").split(/\n/) : [];
  const next = lines.filter((rawLine) => envKeyFromLine(rawLine.replace(/\r$/, "")) !== key);
  return next.length > 0 ? `${next.join("\n")}\n` : "";
}

function dirtyStamp(now: () => Date): string {
  return now().toISOString().replace(/[-:]/g, "").replace(/\.\d{3}Z$/, "").replace("T", "");
}

function pathIsInside(root: string, child: string): boolean {
  const relativePath = relative(root, child);
  return relativePath === "" || (!relativePath.startsWith("..") && !isAbsolute(relativePath));
}

function validateSymlink(root: string, fullPath: string, relativePath: string): string | undefined {
  const linkTarget = readlinkSync(fullPath);
  if (isAbsolute(linkTarget)) return `${relativePath} -> ${linkTarget}`;
  const resolvedTarget = resolvePath(dirname(fullPath), linkTarget);
  if (!pathIsInside(root, resolvedTarget)) return `${relativePath} -> ${linkTarget}`;
  return undefined;
}

function scanArtifact(root: string, relativeDir = ""): { dotenvFiles: string[]; unsafeSymlinks: string[] } {
  const dotenvFiles: string[] = [];
  const unsafeSymlinks: string[] = [];
  const currentDir = join(root, relativeDir);
  for (const entry of readdirSync(currentDir)) {
    const relativePath = relativeDir ? `${relativeDir}/${entry}` : entry;
    const fullPath = join(currentDir, entry);
    if (blockedDotenvFiles([relativePath]).length > 0) dotenvFiles.push(relativePath);

    const stat = lstatSync(fullPath);
    if (stat.isSymbolicLink()) {
      const unsafe = validateSymlink(root, fullPath, relativePath);
      if (unsafe) unsafeSymlinks.push(unsafe);
    } else if (stat.isDirectory()) {
      const nested = scanArtifact(root, relativePath);
      dotenvFiles.push(...nested.dotenvFiles);
      unsafeSymlinks.push(...nested.unsafeSymlinks);
    }
  }
  return { dotenvFiles, unsafeSymlinks };
}

function validateArtifact(root: string, includeDotenv: boolean) {
  const { dotenvFiles, unsafeSymlinks } = scanArtifact(root);
  if (unsafeSymlinks.length > 0) throw new Error(`refusing to deploy unsafe symlink: ${unsafeSymlinks.join(", ")}`);
  if (dotenvFiles.length === 0) return;
  if (!includeDotenv) throw new Error(`refusing to deploy dotenv file: ${dotenvFiles.join(", ")}`);
  console.error(`Warning: deploying dotenv file: ${dotenvFiles.join(", ")}`);
}

function copyDirectoryContents(source: string, target: string) {
  if (!existsSync(source) || !lstatSync(source).isDirectory()) {
    throw new Error(`[build].output does not exist after build: ${source}`);
  }
  mkdirSync(target, { recursive: true });
  for (const entry of readdirSync(source)) {
    cpSync(join(source, entry), join(target, entry), COPY_OPTIONS);
  }
}

function copyRelativePath(sourceRoot: string, relativePath: string, targetRoot: string) {
  const source = join(sourceRoot, relativePath);
  const target = join(targetRoot, relativePath);
  if (!existsSync(source)) throw new Error(`include path does not exist after build: ${relativePath}`);
  mkdirSync(dirname(target), { recursive: true });
  cpSync(source, target, COPY_OPTIONS);
}

function copyRootFile(sourceRoot: string, file: string, targetRoot: string) {
  const source = join(sourceRoot, file);
  if (!existsSync(source)) throw new Error(`${file} is required when install is enabled`);
  cpSync(source, join(targetRoot, file));
}

function loadAppContext(root: string, envName: string): AppContext {
  const model = readManifestModel(root);
  const result = checkManifestModel(root, model, envName);
  if (result.errors.length > 0) throw new Error(result.errors.join("\n"));

  const target = normalizeEnvTarget(model, envName);
  if (!target) throw new Error(`env not found: ${envName}`);
  return requireAppContext(target);
}

function loadSingleEnvContext(root: string): AppContext {
  const model = readManifestModel(root);
  const result = checkManifestModel(root, model);
  if (result.errors.length > 0) throw new Error(result.errors.join("\n"));
  if (result.envs.length !== 1) throw new Error("command requires exactly one env in simple-vps.toml");
  const target = normalizeEnvTarget(model, result.envs[0]);
  if (!target) throw new Error(`env not found: ${result.envs[0]}`);
  return requireAppContext(target);
}

function parseServerFlag(args: string[]): { server?: string; rest: string[] } {
  const rest: string[] = [];
  let server: string | undefined;
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg !== "--server") {
      rest.push(arg);
      continue;
    }
    const value = args[index + 1];
    if (!value) throw new Error("--server requires a value");
    validateSshTarget(value, "--server");
    server = value;
    index += 1;
  }
  return { server, rest };
}

function validateSshTarget(value: string, label: string) {
  if (value.startsWith("-") || !SSH_TARGET_RE.test(value)) {
    throw new Error(`${label} must be an SSH target like deploy@example.com`);
  }
}

function loadReadTarget(root: string, server: string | undefined): ReadTarget {
  if (server) return { server };
  return loadSingleEnvContext(root);
}

function checkManifestModel(root: string, model: ManifestModel, envName?: string): CheckResult {
  const errors: string[] = [];
  const warnings: string[] = [];

  if (!model.appName) errors.push("name is required");
  else if (!APP_RE.test(model.appName)) errors.push("name must match ^[a-z][a-z0-9-]{1,40}$");

  if (model.envNames.length === 0) errors.push("at least one [env.<name>] block is required");
  if (envName && !model.envs[envName]) errors.push(`env not found: ${envName}`);

  const selectedEnvNames = envName ? [envName] : model.envNames;
  const locks = lockfilesIn(root);
  if (locks.length > 1) errors.push(`multiple lockfiles found: ${locks.join(", ")}`);

  for (const selected of selectedEnvNames) {
    const target = normalizeEnvTarget(model, selected);
    if (!target) continue;
    validateEnvTarget(target, errors);
    validateBuild(target.build, root, errors);
    validateServices(target.services, target.runtime, errors);
    validateRoutes(target.routes, target.services, errors);

    if (installNeeded(target.runtime, target.build) && locks.length === 0) {
      errors.push(`no lockfile found for env ${selected}`);
    }
  }

  return { errors, warnings, envs: model.envNames };
}

export function checkManifest(root = process.cwd(), envName?: string): CheckResult {
  return checkManifestModel(root, readManifestModel(root), envName);
}

function printCheckResult(result: CheckResult) {
  for (const warning of result.warnings) console.error(`Warning: ${warning}`);
  if (result.errors.length > 0) {
    for (const error of result.errors) console.error(`Error: ${error}`);
    process.exitCode = 1;
    return;
  }
  const envList = result.envs.length > 0 ? result.envs.join(", ") : "none";
  console.log(`${MANIFEST_FILE} OK (envs: ${envList})`);
}

function inferPackageName(root: string): string {
  const packageJson = readPackageJson(root);
  const rawName = asString(packageJson?.name) ?? "my-app";
  const unscoped = rawName.split("/").pop() ?? rawName;
  return unscoped.toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/^-+|-+$/g, "") || "my-app";
}

function readPackageJson(root: string): Dict | undefined {
  const packagePath = join(root, "package.json");
  if (!existsSync(packagePath)) return undefined;
  try {
    const packageJson = JSON.parse(readFileSync(packagePath, "utf8")) as unknown;
    return isRecord(packageJson) ? packageJson : undefined;
  } catch {
    return undefined;
  }
}

function packageScripts(packageJson: Dict | undefined): Dict {
  return isRecord(packageJson?.scripts) ? packageJson.scripts : {};
}

function runInit(root: string) {
  const manifestPath = join(root, MANIFEST_FILE);
  if (existsSync(manifestPath)) throw new Error(`${MANIFEST_FILE} already exists`);
  const packageJson = readPackageJson(root);
  const name = inferPackageName(root);
  const scripts = packageScripts(packageJson);
  const lockfile = lockfilesIn(root)[0];
  const packageManager = packageManagerForLockfile(lockfile);
  const runtime = runtimeForLockfile(lockfile);
  const startCommand = asString(scripts.start)
    ? `${packageManager} run start`
    : runtime === "bun"
      ? "bun run src/server.ts"
      : "npm run start";
  const buildBlock = asString(scripts.build)
    ? `[build]
command = "${packageManager} run build"
output = "dist"

`
    : "";
  const content = `name = "${name}"

${buildBlock}[env.production]
server = "deploy@100.x.y.z"
runtime = "${runtime}"

[services.web]
command = "${startCommand}"
port = 3000
healthcheck = "/health"

[routes.app]
host = "app.example.com"
type = "proxy"
service = "web"
`;
  writeFileSync(manifestPath, content, { encoding: "utf8", mode: 0o644 });
  console.log(`Created ${MANIFEST_FILE}`);
  console.log("Next:");
  console.log(`1. edit ${MANIFEST_FILE}`);
  console.log("2. simple-vps setup production");
  console.log("3. simple-vps deploy production");
}

async function runCommand(runner: CommandRunner, command: string[], failureMessage: string): Promise<CommandResult> {
  const result = await runner.run(command);
  if (result.code !== 0) {
    const detail = result.stderr.trim() || result.stdout.trim();
    throw new Error(detail ? `${failureMessage}: ${detail}` : failureMessage);
  }
  return result;
}

function simpleVpsHelperCommand(args: string[]): string {
  return ["sudo", "simple-vps", ...args].map(shellEscape).join(" ");
}

class RemoteHost {
  constructor(
    private readonly runner: CommandRunner,
    readonly server: string,
  ) {
    validateSshTarget(server, "server");
  }

  async ssh(command: string, failureMessage: string): Promise<CommandResult> {
    return runCommand(this.runner, ["ssh", this.server, command], failureMessage);
  }

  async trySsh(command: string): Promise<CommandResult> {
    return this.runner.run(["ssh", this.server, command]);
  }

  async read(command: string, failureMessage: string): Promise<CommandResult> {
    const result = await this.ssh(command, failureMessage);
    if (result.stdout.trim()) console.log(result.stdout.trimEnd());
    return result;
  }

  async passthrough(): Promise<void> {
    const result = await this.runner.run(["ssh", this.server], { passthrough: true });
    if (result.code !== 0) throw new Error(`SSH failed for ${this.server}`);
  }

  async checkSsh(): Promise<void> {
    await this.ssh("true", `SSH failed for ${this.server}`);
  }

  async requireTool(tool: string, failureMessage: string): Promise<void> {
    await this.ssh(`command -v ${shellEscape(tool)}`, failureMessage);
  }

  async helper(args: string[], failureMessage: string): Promise<CommandResult> {
    return this.ssh(simpleVpsHelperCommand(args), failureMessage);
  }

  async readHelper(args: string[], failureMessage: string): Promise<CommandResult> {
    return this.read(simpleVpsHelperCommand(args), failureMessage);
  }

  async tryHelper(args: string[]): Promise<CommandResult> {
    return this.trySsh(simpleVpsHelperCommand(args));
  }

  async upload(localPath: string, remotePath: string, failureMessage: string): Promise<void> {
    await runCommand(this.runner, ["rsync", "-az", localPath, `${this.server}:${remotePath}`], failureMessage);
  }
}

function remoteHost(runner: CommandRunner, target: ReadTarget): RemoteHost {
  return new RemoteHost(runner, target.server);
}

async function readSecretInput(stdinText: string | undefined): Promise<string> {
  const text =
    stdinText ??
    (process.stdin.isTTY
      ? await promptHidden("Value: ")
      : await Bun.stdin.text());
  return text.replace(/\r?\n$/, "");
}

async function promptHidden(label: string): Promise<string> {
  const script = `printf ${shellEscape(label)} >&2; stty -echo; IFS= read -r value; status=$?; stty echo; printf '\\n' >&2; [ "$status" -eq 0 ] || exit "$status"; printf '%s' "$value"`;
  const proc = Bun.spawn(["sh", "-c", script], {
    stdin: "inherit",
    stdout: "pipe",
    stderr: "inherit",
  });
  const [stdout, code] = await Promise.all([new Response(proc.stdout).text(), proc.exited]);
  if (code !== 0) throw new Error("failed to read secret value");
  return stdout;
}

async function runSetup(root: string, envName: string, runner: CommandRunner) {
  const { appName, server, runtime } = loadAppContext(root, envName);
  const host = remoteHost(runner, { server });

  await host.checkSsh();
  for (const tool of ["simple-vps", "rsync", runtime]) {
    if (tool === "static") continue;
    const message =
      tool === "simple-vps"
        ? "missing Simple VPS server API; rerun the Simple VPS install"
        : `missing required server tool: ${tool}`;
    await host.requireTool(tool, message);
  }
  await host.helper(["app", "create", appName], "simple-vps app create failed; rerun the Simple VPS install");
  console.log(`Setup complete for ${appName} (${envName})`);
}

async function uploadEnvContent(runner: CommandRunner, context: AppContext, content: string) {
  validateEnvironmentFile(content);
  const host = remoteHost(runner, context);
  const localDir = mkdtempSync(join(tmpdir(), "simple-vps-env-"));
  const localPath = join(localDir, ".env");
  const remotePath = `${REMOTE_DEPLOY_TMP_DIR}/${context.appName}-env-${Date.now().toString(36)}.env`;
  writeFileSync(localPath, content, { encoding: "utf8", mode: 0o600 });
  await host.upload(localPath, remotePath, "env upload failed");
  await host.helper(["app", "install-env", context.appName, remotePath], "env install failed");
}

async function readRemoteEnv(runner: CommandRunner, context: AppContext): Promise<string> {
  const result = await remoteHost(runner, context).helper(["app", "read-env", context.appName], "failed to read remote env");
  return result.stdout;
}

async function runEnvPush(root: string, envName: string, file: string, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  const content = readFileSync(file, "utf8");
  await uploadEnvContent(runner, context, content);
  console.log(`Pushed env for ${context.appName} (${envName}). Run simple-vps restart ${envName} <service> to apply.`);
}

async function runSecretPut(root: string, envName: string, key: string, runner: CommandRunner, stdinText: string | undefined) {
  const context = loadAppContext(root, envName);
  validateEnvKey(key);
  const value = await readSecretInput(stdinText);
  const content = setEnvValue(await readRemoteEnv(runner, context), key, value);
  await uploadEnvContent(runner, context, content);
  console.log(`Set secret ${key} for ${context.appName} (${envName}). Run simple-vps restart ${envName} <service> to apply.`);
}

async function runSecretList(root: string, envName: string, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  for (const key of envKeys(await readRemoteEnv(runner, context))) {
    console.log(key);
  }
}

async function runSecretRm(root: string, envName: string, key: string, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  const current = await readRemoteEnv(runner, context);
  const content = removeEnvValue(current, key);
  if (content === current) {
    console.log(`Secret ${key} was not set for ${context.appName} (${envName}).`);
    return;
  }
  await uploadEnvContent(runner, context, content);
  console.log(`Removed secret ${key} for ${context.appName} (${envName}). Run simple-vps restart ${envName} <service> to apply.`);
}

async function gitOutput(root: string, runner: CommandRunner, args: string[]): Promise<string> {
  const result = await runCommand(runner, ["git", "-C", root, ...args], `git ${args.join(" ")} failed`);
  return result.stdout.trim();
}

function renderUnit(appName: string, envName: string, release: string, serviceName: string, service: Dict): string {
  const port = servicePort(service);
  const command = requireString(service.command, `[services.${serviceName}].command`);
  const releaseDir = `/var/apps/${appName}/releases/${release}`;
  const lines = [
    "[Unit]",
    `Description=simple-vps: ${appName}/${serviceName}`,
    "After=network.target",
    "",
    "[Service]",
    "Type=simple",
    `User=app-${appName}`,
    `Group=app-${appName}`,
    `WorkingDirectory=/var/apps/${appName}/current`,
    `EnvironmentFile=/var/apps/${appName}/shared/.env`,
    `Environment="SIMPLE_APP_NAME=${appName}"`,
    `Environment="SIMPLE_ENV=${envName}"`,
    `Environment="SIMPLE_RELEASE=${release}"`,
    `Environment="SIMPLE_RELEASE_DIR=${releaseDir}"`,
    'Environment="NODE_ENV=production"',
  ];
  if (port !== undefined) lines.push(`Environment="PORT=${port}"`);
  lines.push(
    `ExecStart=/usr/bin/env bash -c 'exec ${systemdEscape(command).replaceAll("'", "'\\''")}'`,
    "Restart=on-failure",
    "RestartSec=5s",
    "StandardOutput=journal",
    "StandardError=journal",
    "NoNewPrivileges=true",
    "PrivateTmp=true",
    "ProtectSystem=strict",
    "ProtectHome=true",
    `ReadWritePaths=/var/apps/${appName}/shared`,
    "",
    "[Install]",
    "WantedBy=multi-user.target",
    "",
  );
  return lines.join("\n");
}

async function prepareArtifact(
  root: string,
  runner: CommandRunner,
  build: Dict | undefined,
  runtime: string,
  dirty: boolean,
): Promise<{ artifactDir: string; lockfiles: string[] }> {
  const checkoutDir = mkdtempSync(join(tmpdir(), "simple-vps-checkout-"));
  const checkoutCommand = dirty
    ? `tar -C ${shellEscape(root)} --exclude .git --exclude node_modules -cf - . | tar -x -C ${shellEscape(checkoutDir)}`
    : `git -C ${shellEscape(root)} archive HEAD | tar -x -C ${shellEscape(checkoutDir)}`;
  await runCommand(runner, ["sh", "-c", checkoutCommand], "failed to create release checkout");

  if (!build) {
    return { artifactDir: checkoutDir, lockfiles: lockfilesIn(checkoutDir) };
  }

  const command = requireString(build.command, "[build].command");
  await runCommand(runner, ["sh", "-c", `cd ${shellEscape(checkoutDir)} && ${command}`], "build failed");

  const artifactDir = mkdtempSync(join(tmpdir(), "simple-vps-artifact-"));
  const output = requireString(build.output, "[build].output");
  copyDirectoryContents(join(checkoutDir, output), artifactDir);

  if (Array.isArray(build.include)) {
    for (const entry of build.include) {
      copyRelativePath(checkoutDir, requireString(entry, "[build].include[]"), artifactDir);
    }
  }

  const lockfiles = lockfilesIn(checkoutDir);
  if (installNeeded(runtime, build)) {
    copyRootFile(checkoutDir, "package.json", artifactDir);
    if (lockfiles.length === 0) throw new Error("lockfile is required when install is enabled");
    copyRootFile(checkoutDir, lockfiles[0], artifactDir);
  }

  return { artifactDir, lockfiles };
}

function pruneReleasesCommand(appRoot: string, keep: number): string {
  const releases = `${appRoot}/releases`;
  const current = `${appRoot}/current`;
  return [
    "set -eu",
    `releases=${shellEscape(releases)}`,
    `current=$(readlink -f ${shellEscape(current)} 2>/dev/null || true)`,
    `previous=$(find "$releases" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\\n' 2>/dev/null | sort -rn | while read -r _ dir; do [ -f "$dir/${RELEASE_SUCCESS_MARKER}" ] || continue; resolved=$(readlink -f "$dir"); [ "$resolved" = "$current" ] && continue; echo "$resolved"; break; done)`,
    "count=0",
    `find "$releases" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\\n' 2>/dev/null | sort -rn | while read -r _ dir; do count=$((count + 1)); resolved=$(readlink -f "$dir"); if [ "$resolved" = "$current" ] || [ "$resolved" = "$previous" ] || [ "$count" -le ${keep} ]; then continue; fi; rm -rf -- "$dir"; done`,
  ].join("; ");
}

class ReleaseLifecycle {
  private readonly host: RemoteHost;

  constructor(
    private readonly runner: CommandRunner,
    private readonly context: AppContext,
  ) {
    this.host = remoteHost(runner, context);
  }

  releaseDir(release: string): string {
    return `${this.context.appRoot}/releases/${release}`;
  }

  private async ssh(command: string, failureMessage: string): Promise<CommandResult> {
    return this.host.ssh(command, failureMessage);
  }

  private async trySsh(command: string): Promise<CommandResult> {
    return this.host.trySsh(command);
  }

  async assertSharedDirectory(): Promise<void> {
    await this.ssh(`test -d ${shellEscape(this.context.appRoot)}/shared`, `setup has not run for ${this.context.envName}`);
  }

  async createReleaseDirectory(releaseDir: string): Promise<void> {
    await this.ssh(`install -d -m 2775 ${shellEscape(releaseDir)}`, "failed to create release directory");
  }

  async uploadArtifact(artifactDir: string, releaseDir: string): Promise<void> {
    await runCommand(
      this.runner,
      ["rsync", "-az", "--delete", `${artifactDir}/`, `${this.context.server}:${releaseDir}/`],
      "rsync failed",
    );
    await this.ssh(`chmod 2775 ${shellEscape(releaseDir)}`, "failed to restore release permissions");
  }

  async linkSharedPaths(releaseDir: string): Promise<void> {
    for (const entry of [".env", "db", "storage", "logs"]) {
      await this.ssh(
        `ln -sfn ${this.context.appRoot}/shared/${entry} ${releaseDir}/${entry}`,
        `failed to link shared ${entry}`,
      );
    }
  }

  async installDependencies(releaseDir: string, lockfiles: string[]): Promise<void> {
    if (!installNeeded(this.context.runtime, this.context.build)) return;
    await this.ssh(
      `sudo simple-vps app run-as ${this.context.appName} --cwd ${releaseDir} -- ${installCommandFor(lockfiles[0])}`,
      "production install failed",
    );
  }

  async installServiceUnits(release: string): Promise<void> {
    const localUnitDir = mkdtempSync(join(tmpdir(), "simple-vps-units-"));
    const remoteUnitDir = `${REMOTE_DEPLOY_TMP_DIR}/${release}`;
    for (const [serviceName, service] of Object.entries(this.context.services)) {
      const serviceUnitName = unitName(this.context.appName, serviceName);
      const unitPath = join(localUnitDir, serviceUnitName);
      writeFileSync(unitPath, renderUnit(this.context.appName, this.context.envName, release, serviceName, service), {
        encoding: "utf8",
        mode: 0o644,
      });
    }
    await this.ssh(`mkdir -p ${remoteUnitDir}`, "failed to create remote unit directory");
    await runCommand(
      this.runner,
      ["rsync", "-az", `${localUnitDir}/`, `${this.context.server}:${remoteUnitDir}/`],
      "failed to upload unit files",
    );

    for (const serviceName of Object.keys(this.context.services)) {
      const serviceUnitName = unitName(this.context.appName, serviceName);
      const remoteUnitPath = `${remoteUnitDir}/${serviceUnitName}`;
      await this.ssh(
        `sudo simple-vps app install-unit ${this.context.appName} ${serviceName} ${remoteUnitPath}`,
        `failed to install ${serviceName} unit`,
      );
    }
    await this.daemonReload();
  }

  async daemonReload(): Promise<void> {
    await this.ssh("sudo simple-vps app daemon-reload", "systemd daemon-reload failed");
  }

  async healthCheckServices(services: Record<string, Dict> = this.context.services): Promise<void> {
    for (const [serviceName, service] of Object.entries(services)) {
      const port = servicePort(service);
      const healthcheck = asString(service.healthcheck);
      if (port !== undefined && healthcheck) {
        const expectedStatus = asNumber(service.healthcheck_status) ?? 200;
        const timeout = asNumber(service.healthcheck_timeout) ?? 10;
        await this.ssh(healthCheckCommand(port, healthcheck, expectedStatus, timeout), `health check failed for ${serviceName}`);
      }
    }
  }

  async stopServices(services: Record<string, Dict> = this.context.services, tolerateFailure = false): Promise<void> {
    for (const serviceName of Object.keys(services)) {
      await this.ssh(
        `sudo simple-vps app service stop ${this.context.appName} ${serviceName}${tolerateFailure ? " || true" : ""}`,
        tolerateFailure ? `failed to stop ${serviceName}` : "service stop failed",
      );
    }
  }

  async startServices(services: Record<string, Dict> = this.context.services): Promise<void> {
    for (const serviceName of Object.keys(services)) {
      await this.ssh(
        `sudo simple-vps app service start ${this.context.appName} ${serviceName}`,
        `failed to start ${serviceName}`,
      );
    }
  }

  private async currentReleaseStrict(): Promise<string> {
    const previousCurrentResult = await this.trySsh(`readlink -f ${this.context.appRoot}/current`);
    return previousCurrentResult.code === 0 ? previousCurrentResult.stdout.trim() : "";
  }

  async activateRelease(releaseDir: string): Promise<void> {
    const previousCurrent = await this.currentReleaseStrict();
    await this.stopServices();
    await this.ssh(`ln -sfn ${releaseDir} ${this.context.appRoot}/current`, "failed to activate release");
    await this.startServices();
    try {
      await this.healthCheckServices();
    } catch (error) {
      await this.stopServices();
      if (previousCurrent) {
        await this.ssh(`ln -sfn ${previousCurrent} ${this.context.appRoot}/current`, "failed to restore previous release");
        await this.startServices();
      }
      throw error;
    }
  }

  async markReleaseSuccessful(releaseDir: string): Promise<void> {
    await this.ssh(`touch ${shellEscape(releaseDir)}/${RELEASE_SUCCESS_MARKER}`, "failed to mark release successful");
  }

  async pruneReleases(): Promise<void> {
    await this.ssh(pruneReleasesCommand(this.context.appRoot, keepReleasesFor(this.context.env)), "failed to prune releases");
  }

  async resolveRollbackTarget(release: string | undefined): Promise<string> {
    const releasesDir = `${this.context.appRoot}/releases`;
    if (release) {
      validateReleaseArg(release);
      const target = `${releasesDir}/${release}`;
      await this.ssh(`test -d ${target}`, `release not found: ${release}`);
      return target;
    }
    const command = `current=$(readlink -f ${this.context.appRoot}/current 2>/dev/null || true); for dir in $(ls -1dt ${releasesDir}/* 2>/dev/null); do [ -f "$dir/${RELEASE_SUCCESS_MARKER}" ] || continue; [ "$(readlink -f "$dir")" = "$current" ] && continue; echo "$dir"; exit 0; done; exit 1`;
    const result = await this.ssh(command, "no previous successful release found");
    return result.stdout.trim();
  }

  async restartService(serviceName: string): Promise<void> {
    const service = this.context.services[serviceName];
    if (!service) throw new Error(`unknown service: ${serviceName}`);
    await this.ssh(`sudo simple-vps app service restart ${this.context.appName} ${serviceName}`, `failed to restart ${serviceName}`);
    await this.healthCheckServices({ [serviceName]: service });
  }

  async publishRoutes(): Promise<void> {
    for (const route of Object.values(this.context.routes)) {
      const host = requireString(route.host, "route host");
      const type = requireString(route.type, "route type");
      await this.ssh(`sudo simple-vps cloudflare publish ${host} --app ${this.context.appName}`, `failed to publish Cloudflare route ${host}`);
      if (type === "proxy") {
        const service = this.context.services[requireString(route.service, "route service")];
        await this.ssh(
          `sudo simple-vps route proxy ${host} --port ${servicePort(service)} --app ${this.context.appName}`,
          `failed to publish route ${host}`,
        );
      } else if (type === "static") {
        await this.ssh(
          `sudo simple-vps route static ${host} --root ${this.context.appRoot}/current --app ${this.context.appName}`,
          `failed to publish route ${host}`,
        );
      } else if (type === "redirect") {
        await this.ssh(
          `sudo simple-vps route redirect ${host} --to ${requireString(route.to, "redirect target")} --app ${this.context.appName}`,
          `failed to publish route ${host}`,
        );
      }
    }
  }

  async disableServices(services: Record<string, Dict> = this.context.services): Promise<void> {
    for (const serviceName of Object.keys(services)) {
      await this.ssh(`sudo simple-vps app service disable ${this.context.appName} ${serviceName} || true`, `failed to disable ${serviceName}`);
    }
  }

  async uninstallServiceUnits(services: Record<string, Dict> = this.context.services): Promise<void> {
    for (const serviceName of Object.keys(services)) {
      await this.ssh(`sudo simple-vps app uninstall-unit ${this.context.appName} ${serviceName}`, `failed to uninstall ${serviceName}`);
    }
  }

  async removeServiceUnits(): Promise<void> {
    for (const [serviceName, service] of Object.entries(this.context.services)) {
      const serviceSet = { [serviceName]: service };
      await this.stopServices(serviceSet, true);
      await this.disableServices(serviceSet);
      await this.uninstallServiceUnits(serviceSet);
    }
  }

  async removePublishedRoutes(): Promise<void> {
    await this.ssh(`sudo simple-vps cloudflare remove --app ${this.context.appName}`, "failed to remove Cloudflare routes");
    await this.ssh(`sudo simple-vps route remove --app ${this.context.appName}`, "failed to remove app routes");
  }

  async removeCurrentSymlink(): Promise<void> {
    await this.ssh(`rm -f ${this.context.appRoot}/current`, "failed to remove current symlink");
  }

  async purgeAppData(): Promise<void> {
    await this.ssh(`sudo simple-vps app destroy ${this.context.appName}`, "failed to purge app data");
  }
}

function serviceStatusText(result: CommandResult): string {
  const text = (result.stdout.trim() || result.stderr.trim()).split("\n")[0];
  return text || `exit ${result.code}`;
}

async function runStatus(root: string, envName: string, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  const currentResult = await runner.run(["ssh", context.server, `readlink -f ${context.appRoot}/current 2>/dev/null || true`]);
  const currentPath = currentResult.stdout.trim();
  const routesResult = await runCommand(
    runner,
    ["ssh", context.server, "sudo simple-vps route list --json"],
    "failed to read routes",
  );
  let routes: Dict[] = [];
  try {
    const payload = JSON.parse(routesResult.stdout) as { routes?: unknown };
    routes = Array.isArray(payload.routes) ? payload.routes.filter((route): route is Dict => isRecord(route)) : [];
  } catch {
    routes = [];
  }
  const appRoutes = routes.filter((route) => route.app === context.appName);

  console.log(`${context.appName} (${envName})`);
  console.log(`current: ${currentPath ? releaseNameFromPath(currentPath) : "none"}`);
  for (const serviceName of Object.keys(context.services)) {
    const result = await runner.run([
      "ssh",
      context.server,
      `sudo simple-vps app service is-active ${context.appName} ${serviceName}`,
    ]);
    console.log(`service ${serviceName}: ${serviceStatusText(result)}`);
  }
  if (appRoutes.length === 0) {
    console.log("routes: none");
  } else {
    for (const route of appRoutes) {
      console.log(`route ${route.host}: ${route.type}`);
    }
  }
}

function chooseLogService(services: Record<string, Dict>, serviceName: string | undefined): string {
  if (serviceName) {
    if (!services[serviceName]) throw new Error(`unknown service: ${serviceName}`);
    return serviceName;
  }
  const names = Object.keys(services);
  if (names.length === 0) throw new Error("no services configured");
  if (names.length > 1) throw new Error("logs requires a service when multiple services are configured");
  return names[0];
}

async function runLogs(root: string, envName: string, serviceName: string | undefined, tail: boolean, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  const selected = chooseLogService(context.services, serviceName);
  const unit = unitName(context.appName, selected);
  const command = tail ? `journalctl -u ${unit} -f` : `journalctl -u ${unit} -n 200 --no-pager`;
  if (tail) {
    const result = await runner.run(["ssh", context.server, command], { passthrough: true });
    if (result.code !== 0) throw new Error("journalctl failed");
    return;
  }
  const result = await runCommand(runner, ["ssh", context.server, command], "journalctl failed");
  if (result.stdout.trim()) console.log(result.stdout.trimEnd());
}

async function runSsh(root: string, envName: string, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  await remoteHost(runner, context).passthrough();
}

async function runRoute(root: string, args: string[], runner: CommandRunner) {
  const subcommand = args[0];
  if (subcommand !== "list") throw new Error("route requires subcommand: list");
  const { server, rest: flags } = parseServerFlag(args.slice(1));
  const json = flags.includes("--json");
  const unknown = flags.find((flag) => flag !== "--json");
  if (unknown) throw new Error(`unknown argument: ${unknown}`);
  const context = loadReadTarget(root, server);
  await remoteHost(runner, context).readHelper(["route", "list", ...(json ? ["--json"] : [])], "failed to read routes");
}

async function runHost(root: string, args: string[], runner: CommandRunner) {
  const subcommand = args[0];
  const { server, rest } = parseServerFlag(args.slice(1));
  if ((subcommand !== "status" && subcommand !== "doctor") || rest.length > 0) {
    throw new Error("host requires subcommand: status, doctor");
  }
  const context = loadReadTarget(root, server);
  const host = remoteHost(runner, context);
  if (subcommand === "status") {
    await host.readHelper(["status"], "failed to read host status");
    return;
  }

  const result = await host.tryHelper(["doctor"]);
  if (result.stdout.trim()) console.log(result.stdout.trimEnd());
  if (result.code !== 0) {
    const detail = result.stderr.trim();
    throw new Error(detail ? `host doctor reported degraded: ${detail}` : "host doctor reported degraded");
  }
}

function validateReleaseArg(release: string) {
  if (!/^[A-Za-z0-9._-]+$/.test(release)) throw new Error(`invalid release: ${release}`);
}

async function runRollback(root: string, envName: string, release: string | undefined, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  const lifecycle = new ReleaseLifecycle(runner, context);
  const target = await lifecycle.resolveRollbackTarget(release);
  await lifecycle.activateRelease(target);
  await lifecycle.markReleaseSuccessful(target);
  console.log(`Rolled back ${context.appName} to ${releaseNameFromPath(target)} (${envName})`);
}

function validateDestroyOptions(context: AppContext, options: DestroyOptions) {
  if (options.purge) {
    if (!options.yes || options.confirm !== context.appName) {
      throw new Error(`destroy --purge requires --yes --confirm ${context.appName}`);
    }
    return;
  }
  if (!options.yes && options.confirm !== context.appName) {
    throw new Error(`destroy requires --yes or --confirm ${context.appName}`);
  }
}

async function runDestroy(root: string, envName: string, runner: CommandRunner, options: DestroyOptions) {
  const context = loadAppContext(root, envName);
  validateDestroyOptions(context, options);
  const lifecycle = new ReleaseLifecycle(runner, context);

  await lifecycle.removeServiceUnits();
  await lifecycle.daemonReload();
  await lifecycle.removePublishedRoutes();
  await lifecycle.removeCurrentSymlink();

  if (options.purge) {
    await lifecycle.purgeAppData();
    console.log(`Destroyed ${context.appName} (${envName}) and purged ${context.appRoot}`);
    return;
  }

  console.log(`Destroyed ${context.appName} (${envName}), preserved ${context.appRoot}/shared and ${context.appRoot}/releases`);
}

async function runRestart(root: string, envName: string, serviceName: string, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  await new ReleaseLifecycle(runner, context).restartService(serviceName);
  console.log(`Restarted ${context.appName}/${serviceName} (${envName})`);
}

async function runDeploy(
  root: string,
  envName: string,
  runner: CommandRunner,
  options: { dirty: boolean; includeDotenv: boolean; now: () => Date },
) {
  const context = loadAppContext(root, envName);
  const { appName, build, runtime } = context;
  const lifecycle = new ReleaseLifecycle(runner, context);

  const sha = await gitOutput(root, runner, ["rev-parse", "HEAD"]);
  const dirty = await gitOutput(root, runner, ["status", "--porcelain"]);
  if (dirty && !options.dirty) throw new Error("working tree is dirty; commit changes or pass --dirty");
  const release = dirty ? `${sha}-dirty-${dirtyStamp(options.now)}` : sha;

  const { artifactDir, lockfiles } = await prepareArtifact(root, runner, build, runtime, Boolean(dirty));
  validateArtifact(artifactDir, options.includeDotenv);

  const releaseDir = lifecycle.releaseDir(release);
  await lifecycle.assertSharedDirectory();
  await lifecycle.createReleaseDirectory(releaseDir);
  await lifecycle.uploadArtifact(artifactDir, releaseDir);
  await lifecycle.linkSharedPaths(releaseDir);
  await lifecycle.installDependencies(releaseDir, lockfiles);
  await lifecycle.installServiceUnits(release);
  await lifecycle.activateRelease(releaseDir);
  await lifecycle.publishRoutes();
  await lifecycle.markReleaseSuccessful(releaseDir);
  try {
    await lifecycle.pruneReleases();
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.error(`Warning: deploy succeeded; pruning failed: ${message}`);
  }

  console.log(`Deployed ${appName} ${release.slice(0, 7)} to ${envName}`);
}

function parseDestroyOptions(args: string[], appNameHint?: string): DestroyOptions {
  const options: DestroyOptions = { purge: false, yes: false };
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === "--purge") {
      options.purge = true;
    } else if (arg === "--yes") {
      options.yes = true;
    } else if (arg === "--confirm") {
      const value = args[index + 1];
      if (!value) throw new Error(`--confirm requires ${appNameHint ?? "an app name"}`);
      options.confirm = value;
      index += 1;
    } else {
      throw new Error(`unknown argument: ${arg}`);
    }
  }
  return options;
}

function usage() {
  console.error("Usage:");
  console.error("  simple-vps init");
  console.error("  simple-vps check [env]");
  console.error("  simple-vps setup <env>");
  console.error("  simple-vps deploy <env> [--dirty] [--include-dotenv]");
  console.error("  simple-vps rollback <env> [release]");
  console.error("  simple-vps destroy <env> [--yes] [--confirm <app>] [--purge]");
  console.error("  simple-vps restart <env> <service>");
  console.error("  simple-vps status <env>");
  console.error("  simple-vps logs <env> [service] [--tail]");
  console.error("  simple-vps ssh <env>");
  console.error("  simple-vps secret put <env> <key>");
  console.error("  simple-vps secret list <env>");
  console.error("  simple-vps secret rm <env> <key>");
  console.error("  simple-vps env push <env> <file>");
  console.error("  simple-vps host status [--server <ssh-target>]");
  console.error("  simple-vps host doctor [--server <ssh-target>]");
  console.error("  simple-vps route list [--json] [--server <ssh-target>]");
}

export async function main(argv = process.argv.slice(2), root = process.cwd(), options: MainOptions = {}) {
  process.exitCode = 0;
  const [command, ...args] = argv;
  const baseRunner = options.runner ?? defaultRunner;
  const now = options.now ?? (() => new Date());
  let cleanupRunner = () => {};
  try {
    if (command === "--help" || command === "-h") {
      usage();
      return;
    }
    if (command === "init") {
      runInit(root);
      return;
    }
    if (command === "check") {
      if (args.length > 1) throw new Error("check accepts optional env");
      const env = args[0];
      printCheckResult(checkManifest(root, env));
      return;
    }
    // Local-only commands go above. Commands that touch the server go below.
    const prepared = prepareCommandRunner(baseRunner);
    cleanupRunner = prepared.cleanup;
    const runner = prepared.runner;
    if (command === "setup") {
      const env = args[0];
      if (!env || args.length > 1) throw new Error("setup requires exactly one env");
      await runSetup(root, env, runner);
      return;
    }
    if (command === "status") {
      const env = args[0];
      if (!env || args.length > 1) throw new Error("status requires exactly one env");
      await runStatus(root, env, runner);
      return;
    }
    if (command === "logs") {
      const env = args[0];
      const rest = args.slice(1);
      const tail = rest.includes("--tail");
      const values = rest.filter((arg) => arg !== "--tail");
      if (!env || values.length > 1) throw new Error("logs requires env, optional service, and optional --tail");
      await runLogs(root, env, values[0], tail, runner);
      return;
    }
    if (command === "ssh") {
      const env = args[0];
      if (!env || args.length > 1) throw new Error("ssh requires exactly one env");
      await runSsh(root, env, runner);
      return;
    }
    if (command === "route") {
      await runRoute(root, args, runner);
      return;
    }
    if (command === "host") {
      await runHost(root, args, runner);
      return;
    }
    if (command === "rollback") {
      const env = args[0];
      const release = args[1];
      if (!env || args.length > 2) throw new Error("rollback requires env and optional release");
      await runRollback(root, env, release, runner);
      return;
    }
    if (command === "destroy") {
      const env = args[0];
      if (!env) throw new Error("destroy requires env");
      await runDestroy(root, env, runner, parseDestroyOptions(args.slice(1)));
      return;
    }
    if (command === "restart") {
      const env = args[0];
      const service = args[1];
      if (!env || !service || args.length > 2) throw new Error("restart requires env and service");
      await runRestart(root, env, service, runner);
      return;
    }
    if (command === "env") {
      const subcommand = args[0];
      if (subcommand !== "push") throw new Error("env requires subcommand: push");
      const env = args[1];
      const file = args[2];
      if (!env || !file || args.length > 3) throw new Error("env push requires env and file");
      await runEnvPush(root, env, file, runner);
      return;
    }
    if (command === "secret") {
      const subcommand = args[0];
      const env = args[1];
      if (subcommand === "put") {
        const key = args[2];
        if (!env || !key || args.length > 3) throw new Error("secret put requires env and key");
        await runSecretPut(root, env, key, runner, options.stdinText);
        return;
      }
      if (subcommand === "list") {
        if (!env || args.length > 2) throw new Error("secret list requires env");
        await runSecretList(root, env, runner);
        return;
      }
      if (subcommand === "rm") {
        const key = args[2];
        if (!env || !key || args.length > 3) throw new Error("secret rm requires env and key");
        await runSecretRm(root, env, key, runner);
        return;
      }
      throw new Error("secret requires subcommand: put, list, rm");
    }
    if (command === "deploy") {
      const env = args[0];
      const flags = args.slice(1);
      const dirty = flags.includes("--dirty");
      const includeDotenv = flags.includes("--include-dotenv");
      const unknown = flags.find((flag) => flag !== "--dirty" && flag !== "--include-dotenv");
      if (!env || unknown) throw new Error("deploy requires one env and optional --dirty/--include-dotenv");
      await runDeploy(root, env, runner, { dirty, includeDotenv, now });
      return;
    }
    usage();
    process.exitCode = 1;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.error(`Error: ${message}`);
    process.exitCode = 1;
  } finally {
    cleanupRunner();
  }
}

if (import.meta.main) {
  await main();
}
