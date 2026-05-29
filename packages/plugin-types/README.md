# @open-neko/plugin-types

Public types + RPC schema for [OpenNeko](https://github.com/open-neko/openneko) plugins.

Imported by:
- every plugin (for `definePlugin`, `runPluginEntrypoint`)
- the OpenNeko worker's plugin loader (for manifest parsing, RPC dispatch)

## Writing a plugin

```ts
// plugin.ts
import { definePlugin } from "@open-neko/plugin-types";

export default definePlugin({
  name: "@open-neko/plugin-example",
  version: "0.1.0",
  actions: [
    {
      kind: "echo",
      description: "echoes the payload back as the result",
      handler: async (req) => ({
        commandOrOperation: "echo",
        externalRef: `ext-${req.id}`,
        result: { received: req.payload },
      }),
    },
  ],
});
```

```ts
// run.ts (the runner script the worker invokes inside the sandbox VM)
import plugin from "./plugin.js";
import { runPluginEntrypoint } from "@open-neko/plugin-types/runner";
await runPluginEntrypoint(plugin);
```

## RPC contract

One-shot per call. The worker invokes the runner script with two argv:

```
node run.js <method> <json-params>
```

…and reads a single `RpcResponse` JSON object from stdout. Methods:

| Method | Params | Result |
|---|---|---|
| `register` | `{}` | `RegisterResult` — protocol version, plugin id, declared actions |
| `execute_action` | `{ request: PluginActionRequest }` | `{ outcome: PluginActionOutcome }` |

## License

Apache-2.0
