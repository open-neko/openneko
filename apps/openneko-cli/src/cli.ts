import { runInit } from "./commands/init.js";
import { runInstall } from "./commands/install.js";
import { runList } from "./commands/list.js";
import { runRemove } from "./commands/remove.js";
import { runDoctor } from "./commands/doctor.js";
import {
  runMarketplaceAdd,
  runMarketplaceList,
  runMarketplaceRemove,
} from "./commands/marketplace.js";
import {
  runSecretsList,
  runSecretsSet,
  runSecretsUnset,
} from "./commands/secrets.js";
import { checkHost } from "./host-check.js";

const VERSION = "0.4.0";

const HELP = `
openneko — OpenNeko operator CLI

USAGE
  openneko <command> [args]

COMMANDS
  init                              Create an empty openneko.plugins.json
                                    in the current directory.

  install <name>[@<marketplace>]    Install a plugin from a trusted
                                    marketplace.
    [--version <v>]                 Pin to a specific version (default:
                                    latest non-yanked).
    [--unverified]                  Skip marketplaces entirely and install
                                    directly from npm. Loud warning;
                                    integrity hash is taken on trust.

  list                              Show plugins listed in the project's
                                    openneko.plugins.json.

  remove <name>                     Remove a plugin from openneko.plugins.json.

  marketplace list                  Show trusted marketplaces.
  marketplace add <url>             Trust a third-party marketplace URL.
  marketplace remove <name|url>     Stop trusting a marketplace.

  secrets list [<plugin>]           Show env keys stored for a plugin (or
                                    all plugins). Values are never echoed.
  secrets set <plugin> <key> [<v>]  Set an env value for a plugin in
                                    the per-user store at
                                    ~/.config/openneko/secrets.json. If
                                    <v> is omitted, prompts hidden.
  secrets unset <plugin> <key>      Remove an env value.

  doctor                            Check whether this host can run
                                    microsandbox and report manifest state.

  --version                         Print CLI version.
  --help, -h                        Show this help.

The official OpenNeko marketplace
(https://open-neko.github.io/plugins/marketplace.json) is trusted by
default and cannot be removed. Add your own with \`marketplace add\`.
`.trimStart();

export interface RunCliOptions {
  argv: string[];
  cwd: string;
  stdout?: (line: string) => void;
  stderr?: (line: string) => void;
  /** For tests: override the marketplaces config dir. */
  configDir?: string;
}

export async function runCli(options: RunCliOptions): Promise<number> {
  const stdout = options.stdout ?? ((s: string) => process.stdout.write(s + "\n"));
  const stderr = options.stderr ?? ((s: string) => process.stderr.write(s + "\n"));
  const args = options.argv;
  if (args.length === 0 || args[0] === "--help" || args[0] === "-h") {
    stdout(HELP);
    return 0;
  }
  if (args[0] === "--version" || args[0] === "-v") {
    stdout(VERSION);
    return 0;
  }
  const command = args[0];
  const rest = args.slice(1);
  try {
    switch (command) {
      case "init":
        return await handleInit(options.cwd, stdout);
      case "install":
        return await handleInstall(options, rest, stdout, stderr);
      case "list":
        return await handleList(options.cwd, stdout);
      case "remove":
        return await handleRemove(options.cwd, rest, stdout, stderr);
      case "doctor":
        return await handleDoctor(options.cwd, stdout);
      case "marketplace":
        return await handleMarketplace(options, rest, stdout, stderr);
      case "secrets":
        return await handleSecrets(options, rest, stdout, stderr);
      default:
        stderr(`unknown command: ${command}`);
        stderr(HELP);
        return 2;
    }
  } catch (err) {
    stderr(`error: ${err instanceof Error ? err.message : String(err)}`);
    return 1;
  }
}

async function handleInit(
  cwd: string,
  stdout: (s: string) => void,
): Promise<number> {
  const result = await runInit({ repoRoot: cwd });
  if (result.created) {
    stdout(`created ${result.path}`);
  } else {
    stdout(`${result.path} already exists`);
  }
  return 0;
}

async function handleInstall(
  cliOptions: RunCliOptions,
  rest: string[],
  stdout: (s: string) => void,
  stderr: (s: string) => void,
): Promise<number> {
  const positional: string[] = [];
  let version: string | undefined;
  let unverified = false;
  for (let i = 0; i < rest.length; i++) {
    const a = rest[i];
    if (a === "--version") {
      version = rest[i + 1];
      i++;
    } else if (a === "--unverified") {
      unverified = true;
    } else if (typeof a === "string" && a.startsWith("--")) {
      stderr(`unknown flag: ${a}`);
      return 2;
    } else if (typeof a === "string") {
      positional.push(a);
    }
  }
  const spec = positional[0];
  if (!spec) {
    stderr("install: package name required");
    return 2;
  }
  const host = checkHost();
  if (!host.supported && !unverified) {
    stderr(
      `host not supported: ${host.reason ?? "(unknown reason)"}\n` +
        `If you understand and want to install anyway, re-run with --unverified.`,
    );
    return 3;
  }
  if (unverified) {
    stderr(
      "WARNING: --unverified bypasses every trusted marketplace. The plugin " +
        "is not reviewed and its integrity hash is taken on trust from npm. " +
        "Use only for plugin authoring or emergency hotfixes.",
    );
  }
  const result = await runInstall({
    repoRoot: cliOptions.cwd,
    spec,
    version,
    unverified,
    configDir: cliOptions.configDir,
  });
  const network =
    result.capabilities.network.length === 0
      ? "none"
      : result.capabilities.network.join(", ");
  const provenance =
    result.source === "unverified"
      ? "unverified npm"
      : `marketplace=${result.marketplace}`;
  stdout(
    `installed ${result.name}@${result.version} (${provenance}) — network: ${network}`,
  );
  if (result.envSaved.length > 0) {
    stdout(`  saved env: ${result.envSaved.join(", ")}`);
  }
  if (result.envAlreadySet.length > 0) {
    stdout(`  env already set: ${result.envAlreadySet.join(", ")}`);
  }
  return 0;
}

async function handleList(
  cwd: string,
  stdout: (s: string) => void,
): Promise<number> {
  const result = await runList({ repoRoot: cwd });
  if (!result.hadManifest) {
    stdout("no openneko.plugins.json — run `openneko init` to create one");
    return 0;
  }
  if (result.entries.length === 0) {
    stdout("no plugins installed");
    return 0;
  }
  for (const entry of result.entries) {
    const hosts =
      entry.capabilities.network.length === 0
        ? "no network"
        : entry.capabilities.network.join(", ");
    const from = entry.marketplace ? `  from=${entry.marketplace}` : "";
    stdout(`${entry.name}@${entry.version}  [${hosts}]${from}`);
  }
  return 0;
}

async function handleRemove(
  cwd: string,
  rest: string[],
  stdout: (s: string) => void,
  stderr: (s: string) => void,
): Promise<number> {
  const name = rest[0];
  if (!name) {
    stderr("remove: package name required");
    return 2;
  }
  const result = await runRemove({ repoRoot: cwd, name });
  if (result.removed) {
    stdout(`removed ${name}`);
  } else {
    stdout(`${name} was not in the manifest`);
  }
  return 0;
}

async function handleDoctor(
  cwd: string,
  stdout: (s: string) => void,
): Promise<number> {
  const report = await runDoctor({ repoRoot: cwd });
  stdout(`host: ${report.host.triple} (${report.host.supported ? "supported" : "UNSUPPORTED"})`);
  if (!report.host.supported && report.host.reason) {
    stdout(`  reason: ${report.host.reason}`);
  }
  stdout(`manifest: ${report.manifest.present ? "found" : "missing"} at ${report.manifest.path}`);
  stdout(`plugins:  ${report.manifest.pluginCount}`);
  return report.host.supported ? 0 : 1;
}

async function handleMarketplace(
  cliOptions: RunCliOptions,
  rest: string[],
  stdout: (s: string) => void,
  stderr: (s: string) => void,
): Promise<number> {
  const sub = rest[0];
  if (sub === "list" || !sub) {
    const result = await runMarketplaceList({ configDir: cliOptions.configDir });
    if (result.marketplaces.length === 0) {
      stdout("no marketplaces trusted");
      return 0;
    }
    for (const m of result.marketplaces) {
      const tag = m.official ? "  [official]" : "";
      stdout(`${m.name}  ${m.url}${tag}`);
    }
    return 0;
  }
  if (sub === "add") {
    const url = rest[1];
    if (!url) {
      stderr("marketplace add: URL required");
      return 2;
    }
    const result = await runMarketplaceAdd({
      url,
      configDir: cliOptions.configDir,
    });
    stdout(
      `trusted ${result.marketplaceName} as "${result.added.name}" — ${result.pluginCount} plugin(s) listed`,
    );
    return 0;
  }
  if (sub === "remove") {
    const target = rest[1];
    if (!target) {
      stderr("marketplace remove: name or URL required");
      return 2;
    }
    const result = await runMarketplaceRemove({
      nameOrUrl: target,
      configDir: cliOptions.configDir,
    });
    if (result.removed) {
      stdout(`removed marketplace ${result.removed.name}`);
    } else {
      stdout(`no trusted marketplace matched ${target}`);
    }
    return 0;
  }
  stderr(`unknown marketplace subcommand: ${sub}`);
  return 2;
}

async function handleSecrets(
  cliOptions: RunCliOptions,
  rest: string[],
  stdout: (s: string) => void,
  stderr: (s: string) => void,
): Promise<number> {
  const sub = rest[0];
  if (sub === "list" || !sub) {
    const result = await runSecretsList({
      configDir: cliOptions.configDir,
      ...(rest[1] ? { plugin: rest[1] } : {}),
    });
    if (result.entries.length === 0 || result.entries.every((e) => e.keys.length === 0)) {
      stdout("no secrets stored");
      return 0;
    }
    for (const e of result.entries) {
      if (e.keys.length === 0) continue;
      stdout(`${e.plugin}`);
      for (const k of e.keys) stdout(`  ${k}`);
    }
    return 0;
  }
  if (sub === "set") {
    const plugin = rest[1];
    const key = rest[2];
    const valueArg = rest[3];
    if (!plugin || !key) {
      stderr("secrets set: plugin and key required");
      return 2;
    }
    let value = valueArg;
    if (value === undefined) {
      const { isInteractive, promptHidden } = await import("./prompt.js");
      if (!isInteractive()) {
        stderr(
          `secrets set: value required when stdin is not a TTY (pass it as the third arg)`,
        );
        return 2;
      }
      value = await promptHidden(`${key} (hidden): `);
    }
    const result = await runSecretsSet({
      configDir: cliOptions.configDir,
      plugin,
      key,
      value,
    });
    stdout(
      `${result.newKey ? "set" : "updated"} ${result.plugin}/${result.key}`,
    );
    return 0;
  }
  if (sub === "unset") {
    const plugin = rest[1];
    const key = rest[2];
    if (!plugin || !key) {
      stderr("secrets unset: plugin and key required");
      return 2;
    }
    const result = await runSecretsUnset({
      configDir: cliOptions.configDir,
      plugin,
      key,
    });
    if (result.removed) {
      stdout(`unset ${result.plugin}/${result.key}`);
    } else {
      stdout(`${result.plugin}/${result.key} was not set`);
    }
    return 0;
  }
  stderr(`unknown secrets subcommand: ${sub}`);
  return 2;
}

const isDirectCall = () =>
  process.argv[1] &&
  (process.argv[1].endsWith("/cli.js") || process.argv[1].endsWith("openneko"));

if (isDirectCall()) {
  runCli({ argv: process.argv.slice(2), cwd: process.cwd() })
    .then((code) => process.exit(code))
    .catch((err) => {
      process.stderr.write(
        `error: ${err instanceof Error ? err.message : String(err)}\n`,
      );
      process.exit(1);
    });
}
