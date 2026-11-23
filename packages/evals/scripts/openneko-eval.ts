import { runEvalCli } from "@neko/evals/cli";
import { createAdventureWorksMetricDriver } from "./eval-adventureworks-metric";
import { createOpenNekoLifecycleDriver } from "./eval-openneko-lifecycle";
import { createOpenNekoBackendDriver } from "./eval-openneko-backend";
import { createOpenNekoWorkDriver } from "./eval-openneko-work";

void runEvalCli(process.argv.slice(2), {
  cwd: process.env.OPENNEKO_WORKSPACE_ROOT || process.cwd(),
  adapters: {
    "adventureworks.metric": createAdventureWorksMetricDriver,
    "openneko.lifecycle": createOpenNekoLifecycleDriver,
    "openneko.work-backend": createOpenNekoBackendDriver,
    "openneko.work-memory": createOpenNekoWorkDriver,
  },
}).then(
  (code) => {
    process.exitCode = code;
  },
  (cause) => {
    console.error(
      `[openneko-eval] ${cause instanceof Error ? cause.message : String(cause)}`,
    );
    process.exitCode = 1;
  },
);
