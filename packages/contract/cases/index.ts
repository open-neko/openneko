import type { Case } from "../src/case";
import { adminCases } from "./admin";
import { authCases } from "./auth";
import { connectionCases } from "./connections";
import { settingsCases } from "./settings";
import { workCases } from "./work";
import { finalCases, workflowCases } from "./workflows";

export const cases: Case[] = [
  ...authCases,
  ...workflowCases,
  ...workCases,
  ...settingsCases,
  ...connectionCases,
  ...adminCases,
  ...finalCases,
];
