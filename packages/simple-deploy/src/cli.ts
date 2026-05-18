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

export type MainOptions = {
  runner?: CommandRunner;
  now?: () => Date;
};

const APP_RE = /^[a-z][a-z0-9-]{1,40}$/;
const SERVICE_RE = /^[a-z][a-z0-9-]{0,30}$/;
const HOST_RE =
  /^(?=.{1,253}$)(?!-)[a-z0-9-]{1,63}(?<!-)(?:\.(?!-)[a-z0-9-]{1,63}(?<!-))*$/;
const RUNTIMES = new Set(["bun", "node", "static"]);
const ROUTE_TYPES = new Set(["proxy", "static", "redirect"]);
const RESERVED_SERVICES = new Set(["current", "releases", "shared"]);
const LOCKFILES = ["bun.lock", "bun.lockb", "pnpm-lock.yaml", "package-lock.json", "yarn.lock"];
const ALLOWED_DOTENV_FILES = new Set([".env.example", ".env.sample", ".env.defaults"]);
const COPY_OPTIONS = { recursive: true, verbatimSymlinks: true };

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
  const path = join(root, "simple-deploy.toml");
  if (!existsSync(path)) {
    throw new Error("simple-deploy.toml not found");
  }
  return parseToml(path);
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

function validateEnvBlock(name: string, appName: string | undefined, env: Dict, errors: string[]) {
  if (!SERVICE_RE.test(name)) errors.push(`invalid env name: ${name}`);
  if (!asString(env.server)) errors.push(`[env.${name}].server is required`);
  const path = asString(env.path);
  if (!path) errors.push(`[env.${name}].path is required`);
  else if (appName && path !== `/var/apps/${appName}`) errors.push(`[env.${name}].path must be /var/apps/${appName}`);
  else if (!path.startsWith("/")) errors.push(`[env.${name}].path must be absolute`);

  const runtime = asString(env.runtime);
  if (!runtime) errors.push(`[env.${name}].runtime is required`);
  else if (!RUNTIMES.has(runtime)) errors.push(`[env.${name}].runtime must be bun, node, or static`);

  const keepReleases = env.keep_releases;
  if (keepReleases !== undefined) {
    const value = asNumber(keepReleases);
    if (value === undefined || !Number.isInteger(value) || value < 1) {
      errors.push(`[env.${name}].keep_releases must be a positive integer`);
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

function resolveEnv(manifest: Dict, envName: string): Dict {
  const envs = isRecord(manifest.env) ? manifest.env : {};
  const env = envs[envName];
  if (!isRecord(env)) throw new Error(`env not found: ${envName}`);
  return env;
}

function loadAppContext(root: string, envName: string): AppContext {
  const result = checkManifest(root, envName);
  if (result.errors.length > 0) throw new Error(result.errors.join("\n"));

  const manifest = readManifest(root);
  const appName = requireString(manifest.name, "name");
  const env = resolveEnv(manifest, envName);
  const server = requireString(env.server, `[env.${envName}].server`);
  const appRoot = requireString(env.path, `[env.${envName}].path`);
  const runtime = requireString(env.runtime, `[env.${envName}].runtime`);
  const build = effectiveBuild(manifest, env);
  const services = effectiveServices(manifest, env);
  const routes = effectiveRoutes(manifest, env);
  return { manifest, appName, env, envName, server, appRoot, runtime, build, services, routes };
}

export function checkManifest(root = process.cwd(), envName?: string): CheckResult {
  const manifest = readManifest(root);
  const errors: string[] = [];
  const warnings: string[] = [];

  const name = asString(manifest.name);
  if (!name) errors.push("name is required");
  else if (!APP_RE.test(name)) errors.push("name must match ^[a-z][a-z0-9-]{1,40}$");

  const envs = isRecord(manifest.env) ? manifest.env : {};
  const envNames = Object.keys(envs);
  if (envNames.length === 0) errors.push("at least one [env.<name>] block is required");
  if (envName && !isRecord(envs[envName])) errors.push(`env not found: ${envName}`);

  const selectedEnvNames = envName ? [envName] : envNames;
  const locks = lockfilesIn(root);
  if (locks.length > 1) errors.push(`multiple lockfiles found: ${locks.join(", ")}`);

  for (const selected of selectedEnvNames) {
    const env = envs[selected];
    if (!isRecord(env)) continue;
    validateEnvBlock(selected, name, env, errors);

    const build = effectiveBuild(manifest, env);
    const services = effectiveServices(manifest, env);
    const routes = effectiveRoutes(manifest, env);
    const runtime = asString(env.runtime);

    validateBuild(build, root, errors);
    validateServices(services, runtime, errors);
    validateRoutes(routes, services, errors);

    if (installNeeded(runtime, build) && locks.length === 0) {
      errors.push(`no lockfile found for env ${selected}`);
    }
  }

  return { errors, warnings, envs: envNames };
}

function printCheckResult(result: CheckResult) {
  for (const warning of result.warnings) console.error(`Warning: ${warning}`);
  if (result.errors.length > 0) {
    for (const error of result.errors) console.error(`Error: ${error}`);
    process.exitCode = 1;
    return;
  }
  const envList = result.envs.length > 0 ? result.envs.join(", ") : "none";
  console.log(`simple-deploy.toml OK (envs: ${envList})`);
}

function inferPackageName(root: string): string {
  const packagePath = join(root, "package.json");
  if (!existsSync(packagePath)) return "my-app";
  try {
    const packageJson = JSON.parse(readFileSync(packagePath, "utf8")) as { name?: unknown };
    const rawName = asString(packageJson.name) ?? "my-app";
    const unscoped = rawName.split("/").pop() ?? rawName;
    return unscoped.toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/^-+|-+$/g, "") || "my-app";
  } catch {
    return "my-app";
  }
}

function runInit(root: string) {
  const manifestPath = join(root, "simple-deploy.toml");
  if (existsSync(manifestPath)) throw new Error("simple-deploy.toml already exists");
  const name = inferPackageName(root);
  const content = `name = "${name}"

[env.production]
server = "admin@100.x.y.z"
path = "/var/apps/${name}"
runtime = "bun"

[services.web]
command = "bun run src/server.ts"
port = 3000
healthcheck = "/health"

[routes.app]
host = "app.example.com"
type = "proxy"
service = "web"
`;
  writeFileSync(manifestPath, content, { encoding: "utf8", mode: 0o644 });
  console.log("Created simple-deploy.toml");
  console.log("Next:");
  console.log("1. edit simple-deploy.toml");
  console.log("2. simple-deploy setup production");
  console.log("3. simple-deploy deploy production");
}

async function runCommand(runner: CommandRunner, command: string[], failureMessage: string): Promise<CommandResult> {
  const result = await runner.run(command);
  if (result.code !== 0) {
    const detail = result.stderr.trim() || result.stdout.trim();
    throw new Error(detail ? `${failureMessage}: ${detail}` : failureMessage);
  }
  return result;
}

async function runSetup(root: string, envName: string, runner: CommandRunner) {
  const { appName, server, runtime } = loadAppContext(root, envName);

  await runCommand(runner, ["ssh", server, "true"], `SSH failed for ${server}`);
  for (const tool of ["simple-vps", "rsync", runtime]) {
    if (tool === "static") continue;
    const message =
      tool === "simple-vps"
        ? "missing Simple VPS server API; rerun the Simple VPS install"
        : `missing required server tool: ${tool}`;
    await runCommand(runner, ["ssh", server, `command -v ${tool}`], message);
  }
  await runCommand(
    runner,
    ["ssh", server, `sudo simple-vps app create ${appName}`],
    "simple-vps app create failed; rerun the Simple VPS install",
  );
  console.log(`Setup complete for ${appName} (${envName})`);
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
    `Description=simple-deploy: ${appName}/${serviceName}`,
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
  const checkoutDir = mkdtempSync(join(tmpdir(), "simple-deploy-checkout-"));
  const checkoutCommand = dirty
    ? `tar -C ${shellEscape(root)} --exclude .git --exclude node_modules -cf - . | tar -x -C ${shellEscape(checkoutDir)}`
    : `git -C ${shellEscape(root)} archive HEAD | tar -x -C ${shellEscape(checkoutDir)}`;
  await runCommand(runner, ["sh", "-c", checkoutCommand], "failed to create release checkout");

  if (!build) {
    return { artifactDir: checkoutDir, lockfiles: lockfilesIn(checkoutDir) };
  }

  const command = requireString(build.command, "[build].command");
  await runCommand(runner, ["sh", "-c", `cd ${shellEscape(checkoutDir)} && ${command}`], "build failed");

  const artifactDir = mkdtempSync(join(tmpdir(), "simple-deploy-artifact-"));
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

async function healthCheckServices(runner: CommandRunner, server: string, services: Record<string, Dict>) {
  for (const [serviceName, service] of Object.entries(services)) {
    const port = servicePort(service);
    const healthcheck = asString(service.healthcheck);
    if (port !== undefined && healthcheck) {
      const expectedStatus = asNumber(service.healthcheck_status) ?? 200;
      const timeout = asNumber(service.healthcheck_timeout) ?? 10;
      await runCommand(
        runner,
        ["ssh", server, healthCheckCommand(port, healthcheck, expectedStatus, timeout)],
        `health check failed for ${serviceName}`,
      );
    }
  }
}

async function stopServices(runner: CommandRunner, server: string, appName: string, services: Record<string, Dict>) {
  for (const serviceName of Object.keys(services)) {
    await runCommand(runner, ["ssh", server, `sudo simple-vps app service stop ${appName} ${serviceName}`], "service stop failed");
  }
}

async function startServices(runner: CommandRunner, server: string, appName: string, services: Record<string, Dict>) {
  for (const serviceName of Object.keys(services)) {
    await runCommand(
      runner,
      ["ssh", server, `sudo simple-vps app service start ${appName} ${serviceName}`],
      `failed to start ${serviceName}`,
    );
  }
}

async function activateRelease(runner: CommandRunner, context: AppContext, releaseDir: string) {
  const previousCurrentResult = await runner.run(["ssh", context.server, `readlink -f ${context.appRoot}/current`]);
  const previousCurrent = previousCurrentResult.code === 0 ? previousCurrentResult.stdout.trim() : "";
  await stopServices(runner, context.server, context.appName, context.services);
  await runCommand(runner, ["ssh", context.server, `ln -sfn ${releaseDir} ${context.appRoot}/current`], "failed to activate release");
  await startServices(runner, context.server, context.appName, context.services);
  try {
    await healthCheckServices(runner, context.server, context.services);
  } catch (error) {
    await stopServices(runner, context.server, context.appName, context.services);
    if (previousCurrent) {
      await runCommand(
        runner,
        ["ssh", context.server, `ln -sfn ${previousCurrent} ${context.appRoot}/current`],
        "failed to restore previous release",
      );
      await startServices(runner, context.server, context.appName, context.services);
    }
    throw error;
  }
}

async function markReleaseSuccessful(runner: CommandRunner, server: string, releaseDir: string) {
  await runCommand(
    runner,
    ["ssh", server, `touch ${shellEscape(releaseDir)}/.simple-deploy-success`],
    "failed to mark release successful",
  );
}

function pruneReleasesCommand(appRoot: string, keep: number): string {
  const releases = `${appRoot}/releases`;
  const current = `${appRoot}/current`;
  return [
    "set -eu",
    `releases=${shellEscape(releases)}`,
    `current=$(readlink -f ${shellEscape(current)} 2>/dev/null || true)`,
    `previous=$(find "$releases" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\\n' 2>/dev/null | sort -rn | while read -r _ dir; do [ -f "$dir/.simple-deploy-success" ] || continue; resolved=$(readlink -f "$dir"); [ "$resolved" = "$current" ] && continue; echo "$resolved"; break; done)`,
    "count=0",
    `find "$releases" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\\n' 2>/dev/null | sort -rn | while read -r _ dir; do count=$((count + 1)); resolved=$(readlink -f "$dir"); if [ "$resolved" = "$current" ] || [ "$resolved" = "$previous" ] || [ "$count" -le ${keep} ]; then continue; fi; rm -rf -- "$dir"; done`,
  ].join("; ");
}

async function pruneReleases(runner: CommandRunner, context: AppContext) {
  await runCommand(
    runner,
    ["ssh", context.server, pruneReleasesCommand(context.appRoot, keepReleasesFor(context.env))],
    "failed to prune releases",
  );
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

function validateReleaseArg(release: string) {
  if (!/^[A-Za-z0-9._-]+$/.test(release)) throw new Error(`invalid release: ${release}`);
}

async function resolveRollbackTarget(context: AppContext, runner: CommandRunner, release: string | undefined): Promise<string> {
  const releasesDir = `${context.appRoot}/releases`;
  if (release) {
    validateReleaseArg(release);
    const target = `${releasesDir}/${release}`;
    await runCommand(runner, ["ssh", context.server, `test -d ${target}`], `release not found: ${release}`);
    return target;
  }
  const command = `current=$(readlink -f ${context.appRoot}/current 2>/dev/null || true); for dir in $(ls -1dt ${releasesDir}/* 2>/dev/null); do [ -f "$dir/.simple-deploy-success" ] || continue; [ "$(readlink -f "$dir")" = "$current" ] && continue; echo "$dir"; exit 0; done; exit 1`;
  const result = await runCommand(runner, ["ssh", context.server, command], "no previous successful release found");
  return result.stdout.trim();
}

async function runRollback(root: string, envName: string, release: string | undefined, runner: CommandRunner) {
  const context = loadAppContext(root, envName);
  const target = await resolveRollbackTarget(context, runner, release);
  await activateRelease(runner, context, target);
  await markReleaseSuccessful(runner, context.server, target);
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

  for (const serviceName of Object.keys(context.services)) {
    await runCommand(
      runner,
      ["ssh", context.server, `sudo simple-vps app service stop ${context.appName} ${serviceName} || true`],
      `failed to stop ${serviceName}`,
    );
    await runCommand(
      runner,
      ["ssh", context.server, `sudo simple-vps app service disable ${context.appName} ${serviceName} || true`],
      `failed to disable ${serviceName}`,
    );
    await runCommand(
      runner,
      ["ssh", context.server, `sudo simple-vps app uninstall-unit ${context.appName} ${serviceName}`],
      `failed to uninstall ${serviceName}`,
    );
  }
  await runCommand(runner, ["ssh", context.server, "sudo simple-vps app daemon-reload"], "systemd daemon-reload failed");
  await runCommand(
    runner,
    ["ssh", context.server, `sudo simple-vps route remove --app ${context.appName}`],
    "failed to remove app routes",
  );
  await runCommand(runner, ["ssh", context.server, `rm -f ${context.appRoot}/current`], "failed to remove current symlink");

  if (options.purge) {
    await runCommand(
      runner,
      ["ssh", context.server, `sudo simple-vps app destroy ${context.appName}`],
      "failed to purge app data",
    );
    console.log(`Destroyed ${context.appName} (${envName}) and purged ${context.appRoot}`);
    return;
  }

  console.log(`Destroyed ${context.appName} (${envName}), preserved ${context.appRoot}/shared and ${context.appRoot}/releases`);
}

async function runDeploy(
  root: string,
  envName: string,
  runner: CommandRunner,
  options: { dirty: boolean; includeDotenv: boolean; now: () => Date },
) {
  const context = loadAppContext(root, envName);
  const { appName, appRoot, build, runtime, server, services, routes } = context;

  const sha = await gitOutput(root, runner, ["rev-parse", "HEAD"]);
  const dirty = await gitOutput(root, runner, ["status", "--porcelain"]);
  if (dirty && !options.dirty) throw new Error("working tree is dirty; commit changes or pass --dirty");
  const release = dirty ? `${sha}-dirty-${dirtyStamp(options.now)}` : sha;

  const { artifactDir, lockfiles } = await prepareArtifact(root, runner, build, runtime, Boolean(dirty));
  validateArtifact(artifactDir, options.includeDotenv);

  const releaseDir = `${appRoot}/releases/${release}`;
  await runCommand(runner, ["ssh", server, `test -d ${shellEscape(appRoot)}/shared`], `setup has not run for ${envName}`);
  await runCommand(runner, ["ssh", server, `install -d -m 2775 ${shellEscape(releaseDir)}`], "failed to create release directory");
  await runCommand(runner, ["rsync", "-az", "--delete", `${artifactDir}/`, `${server}:${releaseDir}/`], "rsync failed");
  await runCommand(runner, ["ssh", server, `chmod 2775 ${shellEscape(releaseDir)}`], "failed to restore release permissions");
  for (const entry of [".env", "db", "storage", "logs"]) {
    await runCommand(
      runner,
      ["ssh", server, `ln -sfn ${appRoot}/shared/${entry} ${releaseDir}/${entry}`],
      `failed to link shared ${entry}`,
    );
  }

  if (installNeeded(runtime, build)) {
    await runCommand(
      runner,
      ["ssh", server, `sudo simple-vps app run-as ${appName} --cwd ${releaseDir} -- ${installCommandFor(lockfiles[0])}`],
      "production install failed",
    );
  }

  const localUnitDir = mkdtempSync(join(tmpdir(), "simple-deploy-units-"));
  const remoteUnitDir = `/tmp/simple-deploy/${release}`;
  for (const [serviceName, service] of Object.entries(services)) {
    const serviceUnitName = unitName(appName, serviceName);
    const unitPath = join(localUnitDir, serviceUnitName);
    writeFileSync(unitPath, renderUnit(appName, envName, release, serviceName, service), { encoding: "utf8", mode: 0o644 });
  }
  await runCommand(runner, ["ssh", server, `mkdir -p ${remoteUnitDir}`], "failed to create remote unit directory");
  await runCommand(runner, ["rsync", "-az", `${localUnitDir}/`, `${server}:${remoteUnitDir}/`], "failed to upload unit files");

  for (const serviceName of Object.keys(services)) {
    const serviceUnitName = unitName(appName, serviceName);
    const remoteUnitPath = `${remoteUnitDir}/${serviceUnitName}`;
    await runCommand(
      runner,
      ["ssh", server, `sudo simple-vps app install-unit ${appName} ${serviceName} ${remoteUnitPath}`],
      `failed to install ${serviceName} unit`,
    );
  }

  await runCommand(runner, ["ssh", server, `sudo simple-vps app daemon-reload`], "systemd daemon-reload failed");
  await activateRelease(runner, context, releaseDir);

  for (const route of Object.values(routes)) {
    const host = requireString(route.host, "route host");
    const type = requireString(route.type, "route type");
    if (type === "proxy") {
      const service = services[requireString(route.service, "route service")];
      await runCommand(
        runner,
        ["ssh", server, `sudo simple-vps route proxy ${host} --port ${servicePort(service)} --app ${appName}`],
        `failed to publish route ${host}`,
      );
    } else if (type === "static") {
      await runCommand(
        runner,
        ["ssh", server, `sudo simple-vps route static ${host} --root ${appRoot}/current --app ${appName}`],
        `failed to publish route ${host}`,
      );
    } else if (type === "redirect") {
      await runCommand(
        runner,
        ["ssh", server, `sudo simple-vps route redirect ${host} --to ${requireString(route.to, "redirect target")} --app ${appName}`],
        `failed to publish route ${host}`,
      );
    }
  }
  await markReleaseSuccessful(runner, server, releaseDir);
  await pruneReleases(runner, context);

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
  console.error("  simple-deploy init");
  console.error("  simple-deploy check [--env <name>]");
  console.error("  simple-deploy setup <env>");
  console.error("  simple-deploy deploy <env> [--dirty] [--include-dotenv]");
  console.error("  simple-deploy status <env>");
  console.error("  simple-deploy logs <env> [service] [--tail]");
  console.error("  simple-deploy rollback <env> [release]");
  console.error("  simple-deploy destroy <env> [--yes|--confirm <app>] [--purge]");
}

export async function main(argv = process.argv.slice(2), root = process.cwd(), options: MainOptions = {}) {
  process.exitCode = 0;
  const [command, ...args] = argv;
  const runner = options.runner ?? defaultRunner;
  const now = options.now ?? (() => new Date());
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
      let env: string | undefined;
      for (let index = 0; index < args.length; index += 1) {
        const arg = args[index];
        if (arg === "--env") {
          env = args[index + 1];
          index += 1;
        } else {
          throw new Error(`unknown argument: ${arg}`);
        }
      }
      printCheckResult(checkManifest(root, env));
      return;
    }
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
  }
}

if (import.meta.main) {
  await main();
}
