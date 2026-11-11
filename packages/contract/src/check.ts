import type { Result } from "./client";
import type { Contract, Operation } from "./spec";
import { validate } from "./validate";

export function conformance(contract: Contract, op: Operation, result: Result): string[] {
  const declared = op.responses[String(result.status)] ?? op.responses.default;
  if (!declared) {
    return [`${op.key}: status ${result.status} is not in the contract (${Object.keys(op.responses).join(", ")})`];
  }
  const content = declared.content;
  if (!content) return [];
  const mediaType = result.contentType.split(";")[0]!.trim();
  const entry = content[mediaType] ?? (mediaType.endsWith("+json") ? content["application/json"] : undefined)
    ?? content["*/*"];
  if (!entry) {
    return [`${op.key}: ${result.status} came as ${mediaType || "no content type"}, the contract says ${Object.keys(content).join(", ")}`];
  }
  if (!entry.schema || !mediaType.includes("json")) return [];
  return validate(result.body, entry.schema, contract.components?.schemas ?? {}).map((e) => `${op.key} ${result.status}: ${e}`);
}
