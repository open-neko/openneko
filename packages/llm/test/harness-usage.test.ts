import { expect, it } from "vitest";
import { harnessCost, harnessModelCall, harnessProvisionalAnswer, harnessRemoteUsage, harnessResult, harnessStageUsage,
  harnessToolCatalogProfile, harnessToolSelectionError, harnessUsage } from "../src/agent-backends/harness";

it("accepts only bounded content-free model route receipts", () => {
  const started={type:"model.request.started",call_id:2,name:"gemini-3.8-flash",origin:"google",stage:"responder"};
  expect(harnessModelCall(started)).toEqual({type:"model_call",phase:"started",callId:2,model:"gemini-3.8-flash",provider:"google",stage:"responder"});
  expect(harnessModelCall({...started,type:"model.request.finished",duration_ms:19,cost_micros:100,
    usage:{requests:1,reported:1,coverage:"complete",input_tokens:6,output_tokens:4,total_tokens:10}})).toMatchObject({phase:"finished",durationMs:19,
      usage:{coverage:"complete",totalTokens:10},chargedMicros:100,failed:false});
  expect(harnessModelCall({...started,origin:undefined,stage:undefined})).toMatchObject({provider:"unknown",stage:"unattributed"});
  expect(harnessModelCall({...started,name:"bad\nsecret"})).toBeUndefined();
  expect(harnessModelCall({...started,call_id:65})).toBeUndefined();
  expect(harnessModelCall({...started,type:"model.request.finished",duration_ms:-1})).toBeUndefined();
  expect(harnessModelCall({...started,type:"model.request.finished",duration_ms:1,cost_micros:-1})).toBeUndefined();
});

it("accepts only bounded responder deltas as provisional output", () => {
  expect(harnessProvisionalAnswer({version: 0, index: 0, text: "Hel"})).toEqual({type: "provisional_answer", version: 0, index: 0, text: "Hel"});
  expect(harnessProvisionalAnswer({version: 0, index: 1, text: "hidden"})).toBeUndefined();
  expect(harnessProvisionalAnswer({version: 0, index: 0, text: ""})).toBeUndefined();
  expect(harnessProvisionalAnswer({version: 0, index: 0, text: "x".repeat(65537)})).toBeUndefined();
});

it("projects Harness model usage without claiming delegated usage", () => {
  const usage = { requests: 3, reported: 3, input_tokens: 60, output_tokens: 12, total_tokens: 72, coverage: "complete" };
  expect(harnessUsage(usage)).toMatchObject({ inputTokens: 60, outputTokens: 12, totalTokens: 72, coverage: "complete" });
  expect(harnessResult({ status: "completed", answer: "Done", usage }).backendState?.harness).toMatchObject({
    usageCoverage: "complete", usageScope: "outer-only",
  });
  expect(harnessUsage({ ...usage, reported: 2, coverage: "partial" })).toMatchObject({
    coverage: "partial", missingReasons: ["Provider usage was available for 2 of 3 Harness model requests"],
  });
  expect(harnessUsage({ ...usage, input_tokens: -1 })).toMatchObject({ coverage: "unavailable" });
  expect(harnessUsage({ requests: 1, reported: 1, coverage: "complete" })).toMatchObject({ coverage: "unavailable" });
});

it("accepts only bounded, versioned Harness admission cost", () => {
  const raw = {pricing_version: "operator-2026-09", charged_micros: 4567, budget_micros: 20_000};
  expect(harnessCost(raw)).toEqual({pricingVersion: "operator-2026-09", chargedMicros: 4567, budgetMicros: 20_000});
  expect(harnessResult({status: "failed", cost: raw}).backendState?.harness).toMatchObject({cost: {chargedMicros: 4567}});
  expect(harnessCost({...raw, charged_micros: -1})).toBeUndefined();
  expect(harnessCost({...raw, pricing_version: ""})).toBeUndefined();
});

it("accepts only a bounded flat Harness remote usage projection", () => {
  expect(harnessRemoteUsage({ reported: true, charged_tokens: 6000, total_tokens: 6000,
    prompt_tokens: 4000, completion_tokens: 2000, llm_calls: 3 })).toEqual({
    reported: true, chargedTokens: 6000, totalTokens: 6000,
    promptTokens: 4000, completionTokens: 2000, llmCalls: 3,
  });
  expect(harnessRemoteUsage({ reported: false, charged_tokens: 12 * 4096 })).toEqual({
    reported: false, chargedTokens: 12 * 4096,
  });
  expect(harnessRemoteUsage({ reported: true, charged_tokens: 1, total_tokens: 6000 })).toBeUndefined();
  expect(harnessRemoteUsage({ reported: false, charged_tokens: 0 })).toBeUndefined();
});

it("accepts only bounded stage usage as diagnostic telemetry", () => {
  const stage = { name: "child.executor", stage_usage: { requests: 2, reported: 2,
    input_tokens: 40, output_tokens: 8, total_tokens: 48, coverage: "complete" } };
  expect(harnessStageUsage(stage)).toMatchObject({ type: "stage_usage", source: "harness", stage: "child.executor",
    requests: 2, reported: 2, usage: { totalTokens: 48, coverage: "complete" } });
  expect(harnessStageUsage({ ...stage, name: "unknown" })).toBeUndefined();
  expect(harnessStageUsage({ ...stage, stage_usage: { ...stage.stage_usage, requests: 1000 } })).toBeUndefined();
  expect(harnessStageUsage({ ...stage, stage_usage: { ...stage.stage_usage, total_tokens: -1 } })).toBeUndefined();
  expect(harnessStageUsage({ ...stage, stage_usage: { ...stage.stage_usage, coverage: "unavailable" } })).toBeUndefined();
});

it("projects only bounded tool-catalog and invalid-selection metadata", () => {
  const catalog={type:"tool.catalog.configured",name:"parent",tool_catalog:{count:2,schema_bytes:85,descriptor_bytes:220},
    data:{private:"never forward"}};
  expect(harnessToolCatalogProfile(catalog)).toEqual({type:"tool_catalog_profile",actor:"parent",count:2,
    schemaBytes:85,descriptorBytes:220});
  expect(harnessToolCatalogProfile({...catalog,tool_catalog:{count:2,schema_bytes:85,descriptor_bytes:84}})).toBeUndefined();
  expect(harnessToolCatalogProfile({...catalog,name:"untrusted"})).toBeUndefined();
  expect(harnessToolSelectionError({type:"tool.input.rejected",name:"lookup",error:"invalid_input",
    data:{private:"never forward"}})).toEqual({type:"tool_selection_error",name:"lookup",reason:"invalid_input"});
  expect(harnessToolSelectionError({type:"tool.input.rejected",name:"lookup",error:"database password"})).toBeUndefined();
  expect(harnessToolSelectionError({type:"tool.input.rejected",name:"bad\nname",error:"invalid_input"})).toBeUndefined();
});
