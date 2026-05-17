// Bundles src/cli.ts into a single dist/cli.js with a shebang so it
// works as a `bin`. All deps except node built-ins are inlined.
import { build } from "esbuild";
import { chmodSync, writeFileSync, readFileSync } from "node:fs";

await build({
  entryPoints: ["src/cli.ts"],
  bundle: true,
  platform: "node",
  target: "node20",
  format: "esm",
  outfile: "dist/cli.js",
  external: [],
  banner: {
    // The shebang must come first for `bin` to work. The require shim
    // is needed because some bundled CJS deps (e.g. `yaml`) do bare
    // `require()` calls that esbuild rewrites to `__require()` — which
    // throws in ESM unless we provide a real require here.
    js:
      `#!/usr/bin/env node\n` +
      `import { createRequire as _openNekoCreateRequire } from "node:module";\n` +
      `const require = _openNekoCreateRequire(import.meta.url);\n`,
  },
  logLevel: "info",
});

const body = readFileSync("dist/cli.js", "utf8");
if (!body.startsWith("#!")) {
  writeFileSync("dist/cli.js", `#!/usr/bin/env node\n${body}`);
}
chmodSync("dist/cli.js", 0o755);
