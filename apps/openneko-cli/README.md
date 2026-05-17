# @open-neko/cli

The `openneko` operator CLI for [OpenNeko](https://github.com/open-neko/neko).

```
npm install -g @open-neko/cli
```

## Commands

| Command | Purpose |
|---|---|
| `openneko init` | Create an empty `openneko.plugins.json` in cwd. |
| `openneko install <name>` | Install a plugin from a trusted marketplace. The official marketplace at `https://open-neko.github.io/plugins/marketplace.json` is trusted by default. |
| `openneko install <name>@<marketplace>` | Scoped install — disambiguate when the same plugin name is listed in multiple trusted marketplaces. |
| `openneko install <name> --version <v>` | Pin to a specific version (else latest non-yanked). |
| `openneko install <name> --unverified` | Bypass all marketplaces; install directly from npm. Loud warning. |
| `openneko list` | Show plugins listed in the manifest. |
| `openneko remove <name>` | Remove a plugin from the manifest. |
| `openneko marketplace list` | Show trusted marketplaces. |
| `openneko marketplace add <url>` | Trust a third-party marketplace. |
| `openneko marketplace remove <name\|url>` | Stop trusting a marketplace. The official marketplace cannot be removed. |
| `openneko secrets list [<plugin>]` | Show env keys stored per plugin. Values are never echoed. |
| `openneko secrets set <plugin> <key> [<value>]` | Set an env value in the per-user store at `~/.config/openneko/secrets.json` (0600 perms). Omit `<value>` to prompt hidden. The worker injects these into the plugin's microVM at exec time — they never land in `openneko.plugins.json` or in `action_request.payload`. |
| `openneko secrets unset <plugin> <key>` | Remove an env value. |
| `openneko doctor` | Check whether this host can run microsandbox; report manifest state. |

## Trust model

OpenNeko's plugin system is **federated**. The OpenNeko team ships one official marketplace listing only the first-party `@open-neko/plugin-*` plugins we write, test, and support. Anyone else can publish their own `marketplace.json` at a stable URL; operators trust it explicitly with `openneko marketplace add <url>`.

Every plugin, regardless of marketplace, runs inside a microsandbox microVM with outbound network limited to the hosts the plugin's manifest declared. That's the floor — curation is on top.

Plugin-required env values (Slack tokens, API keys, etc.) are declared by the plugin in its marketplace entry's `requires_env` field. During `openneko install` the CLI prompts the operator for each required key (hidden input) and stores it in the per-user secrets file at `~/.config/openneko/secrets.json`. Secrets never travel with the project (`openneko.plugins.json` stays committed; secrets stay per-user) and never enter `action_request.payload` (which the agent could log).

The trusted-marketplaces list lives at `$XDG_CONFIG_HOME/openneko/marketplaces.json` (default `~/.config/openneko/marketplaces.json`); the secrets store sits next to it.

## Host support

Plugins run inside microsandbox microVMs. Required:
- macOS arm64 (Apple Silicon), **or**
- Linux x86_64 or arm64 with `/dev/kvm` available

`openneko doctor` tells you whether your host qualifies.

## License

Apache-2.0
