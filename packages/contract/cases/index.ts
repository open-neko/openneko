import type { Case } from "../src/case";
import { adminCases } from "./admin";
import { authCases } from "./auth";
import { briefingCases } from "./briefing";
import { connectionCases } from "./connections";
import { settingsCases } from "./settings";
import { workCases } from "./work";
import { finalCases, workflowCases } from "./workflows";

export const cases: Case[] = [
  ...authCases,
  ...briefingCases,
  ...workflowCases,
  ...workCases,
  ...settingsCases,
  ...connectionCases,
  ...adminCases,
  ...finalCases,
];
