import { createEvalPlan, loadEval, runEvaluation } from "../../src";
import { createFixtureDriver } from "./fixture-driver";

const [configPath, stateRoot, resultsRoot, callLog, cwd, resetLog, maxConcurrency] = process.argv.slice(2);
if (!configPath || !stateRoot || !resultsRoot || !callLog || !cwd) {
  throw new Error("missing crash-runner arguments");
}

const loaded = await loadEval(configPath);
const plan = createEvalPlan(loaded);
await runEvaluation({
  loaded,
  plan,
  driver: {
    ...createFixtureDriver({
      loaded,
      plan,
      callLog,
      crashOnCall: 2,
      ...(resetLog ? { resetLog } : {}),
    }),
    ...(maxConcurrency ? { maxConcurrency: Number(maxConcurrency) } : {}),
  },
  cwd,
  stateRoot,
  resultsRoot,
  promote: false,
});
