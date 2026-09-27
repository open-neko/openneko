import { createHash } from "node:crypto";
import { createReadStream, constants as fsConstants } from "node:fs";
import { lstat, mkdtemp, open, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, isAbsolute, join, relative, sep } from "node:path";
import { spawn } from "node:child_process";
import { startupEvent } from "@neko/telemetry/startup";

export interface HarnessProcessBinding {
  binary: string;
  binarySha256: string;
  openshell: string;
  gateway: string;
  image: string;
  orgRoot: string;
  runRoot: string;
  artifactRoot: string;
  uploadsRoot: string;
}

type ProcessInput = {
  language: "python" | "shell";
  script: string;
  uploads: string[];
  outputs: string[];
};

const safeName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const sha256 = /^[a-f0-9]{64}$/;

export function validHarnessProcessBinding(binding: HarnessProcessBinding): boolean {
  return [binding.binary, binding.openshell, binding.orgRoot, binding.runRoot,
    binding.artifactRoot, binding.uploadsRoot].every(isAbsolute) &&
    sha256.test(binding.binarySha256) && !!binding.gateway && !!binding.image &&
    binding.artifactRoot === join(binding.runRoot, "artifacts") &&
    binding.runRoot.startsWith(binding.orgRoot + sep) &&
    binding.uploadsRoot.startsWith(binding.orgRoot + sep);
}

export function parseHarnessProcessInput(instruction: string): ProcessInput {
  let value: unknown;
  try { value = JSON.parse(instruction); }
  catch { throw new Error("Invalid isolated process request"); }
  if (!value || Array.isArray(value) || typeof value !== "object") throw new Error("Invalid isolated process request");
  const obj = value as Record<string, unknown>;
  if (Object.keys(obj).some(key => !["language", "script", "uploads", "outputs"].includes(key)) ||
      (obj.language !== "python" && obj.language !== "shell") ||
      typeof obj.script !== "string" || !obj.script.trim() ||
      Buffer.byteLength(obj.script) > 65536 || obj.script.includes("\0")) {
    throw new Error("Invalid isolated process request");
  }
  const uploads = obj.uploads === undefined ? [] : obj.uploads;
  if (!Array.isArray(uploads) || uploads.length > 16 ||
      !Array.isArray(obj.outputs) || obj.outputs.length < 1 || obj.outputs.length > 16 ||
      ![...uploads, ...obj.outputs].every(name => typeof name === "string" && safeName.test(name) && name !== "." && name !== "..") ||
      new Set(uploads).size !== uploads.length || new Set(obj.outputs).size !== obj.outputs.length ||
      [...uploads, ...obj.outputs].includes(obj.language === "python" ? "run.py" : "run.sh")) {
    throw new Error("Invalid isolated process files");
  }
  return {language: obj.language, script: obj.script, uploads, outputs: obj.outputs as string[]};
}

async function copyUpload(root: string, name: string, destination: string): Promise<number> {
  const source = join(root, name);
  if (!(await lstat(source)).isFile()) throw new Error("Invalid selected upload");
  const file = await open(source, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW);
  try {
    const before = await file.stat();
    if (!before.isFile() || before.size > 4 << 20) throw new Error("Selected upload exceeds limit");
    const data = await file.readFile();
    const after = await file.stat();
    if (data.length !== before.size || before.size !== after.size || before.mtimeMs !== after.mtimeMs) {
      throw new Error("Selected upload changed during staging");
    }
    await writeFile(destination, data, {flag: "wx", mode: 0o600});
    return data.length;
  } finally {
    await file.close();
  }
}

async function verifyBinary(binding: HarnessProcessBinding): Promise<void> {
  const actual = createHash("sha256").update(await readFile(binding.binary)).digest("hex");
  if (actual !== binding.binarySha256) throw new Error("Isolated process executable changed");
}

async function invokeBinary(
  binding: HarnessProcessBinding, runId: string, operationId: number,
  inputRoot: string, outputRoot: string, input: ProcessInput, signal?: AbortSignal,
): Promise<{files: string[]; output: string; outputTruncated: boolean; inputDigest: string}> {
  const timeoutSeconds = process.env.OPENNEKO_PROCESS_TIMEOUT_SECONDS ?? "120";
  if (!/^[1-9]\d{0,3}$/.test(timeoutSeconds) || Number(timeoutSeconds) > 1200) {
    throw new Error("Invalid isolated process timeout limit");
  }
  const child = spawn(binding.binary, [], {
    stdio: ["pipe", "pipe", "pipe"],
    env: {
      NODE_ENV: process.env.NODE_ENV ?? "production",
      PATH: process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin",
      HOME: process.env.HOME ?? "",
      ...(process.env.XDG_CONFIG_HOME ? {XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME} : {}),
      HARNESS_OPENSHELL_BIN: binding.openshell,
      OPENSHELL_GATEWAY: binding.gateway,
      HARNESS_PROCESS_IMAGE: binding.image,
      HARNESS_PROCESS_RUN_ID: runId,
      HARNESS_PROCESS_OPERATION_ID: String(operationId),
      HARNESS_PROCESS_INPUT_ROOT: inputRoot,
      HARNESS_PROCESS_OUTPUT_ROOT: outputRoot,
      HARNESS_PROCESS_TIMEOUT_SECONDS: timeoutSeconds,
    },
  });
  const encoded = JSON.stringify({Argv: input.language === "python" ? ["python3", "run.py"] : ["sh", "run.sh"], Outputs: input.outputs});
  child.stdin.on("error", () => undefined);
  child.stdin.end(encoded);
  let stdout = "", stderr = "";
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk: string) => {
    stdout = (stdout + chunk).slice(0, 262145);
    if (stdout.length > 262144) child.kill("SIGTERM");
  });
  child.stderr.on("data", (chunk: string) => {
    stderr = (stderr + chunk).slice(0, 16385);
    if (stderr.length > 16384) child.kill("SIGTERM");
  });
  const abort = () => child.kill("SIGTERM");
  signal?.addEventListener("abort", abort, {once: true});
  if (signal?.aborted) abort();
  const timeout = setTimeout(abort, 5 * 60_000);
  const force = setTimeout(() => child.kill("SIGKILL"), 6 * 60_000);
  try {
    const exit = await new Promise<number | null>((resolve, reject) => {
      child.once("error", reject);
      child.once("close", resolve);
    });
    if (signal?.aborted || exit !== 0 || stdout.length > 262144 || stderr.length > 16384) {
      throw new Error("Isolated process failed or was cancelled");
    }
    let receipt: unknown;
    try { receipt = JSON.parse(stdout); }
    catch { throw new Error("Invalid isolated process receipt"); }
    const result = (receipt as {ok?: unknown; result?: Record<string, unknown>})?.result;
    if ((receipt as {ok?: unknown})?.ok !== true || !result ||
        !Array.isArray(result.Files) || result.Files.length !== input.outputs.length ||
        result.Files.some((file, index) => file !== input.outputs[index]) ||
        typeof result.Output !== "string" || typeof result.InputDigest !== "string" ||
        !sha256.test(result.InputDigest)) {
      throw new Error("Invalid isolated process receipt");
    }
    return {files: result.Files, output: result.Output.slice(0, 8192),
      outputTruncated: result.OutputTruncated === true || result.Output.length > 8192,
      inputDigest: result.InputDigest};
  } finally {
    clearTimeout(timeout);
    clearTimeout(force);
    signal?.removeEventListener("abort", abort);
  }
}

async function hashFile(file: string): Promise<string> {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest("hex");
}

/** Called only after a run-scoped broker grant and durable operation intent. */
export async function runHarnessProcess(
  binding: HarnessProcessBinding, runId: string, operationId: number,
  instruction: string, signal?: AbortSignal,
): Promise<{ok: true; files: Array<{path: string; sha256: string; bytes: number}>; output: string; outputTruncated: boolean; inputDigest: string}> {
  if (!validHarnessProcessBinding(binding) || !/^[a-zA-Z0-9-]{1,128}$/.test(runId) ||
      !Number.isInteger(operationId) || operationId < 1 || operationId > 32 ||
      basename(binding.runRoot) !== runId) {
    throw new Error("Invalid isolated process binding");
  }
  const input = parseHarnessProcessInput(instruction);
  const started = performance.now();
  const inputRoot = await mkdtemp(join(tmpdir(), "harness-process-input-"));
  const outputRoot = join(binding.artifactRoot, `process-${operationId}`);
  try {
    const scriptName = input.language === "python" ? "run.py" : "run.sh";
    await writeFile(join(inputRoot, scriptName), input.script, {flag: "wx", mode: 0o600});
    let inputBytes = Buffer.byteLength(input.script);
    for (const name of input.uploads) {
      inputBytes += await copyUpload(binding.uploadsRoot, name, join(inputRoot, name));
      if (inputBytes > 32 << 20) throw new Error("Isolated process input exceeds limit");
    }
    await verifyBinary(binding);
    const result = await invokeBinary(binding, runId, operationId, inputRoot, outputRoot, input, signal);
    const files: Array<{path: string; sha256: string; bytes: number}> = [];
    for (const name of result.files) {
      const published = join(outputRoot, name);
      const info = await lstat(published);
      if (!info.isFile() || info.size > 16 << 20) throw new Error("Invalid isolated process output");
      files.push({path: relative(binding.orgRoot, published).split(sep).join("/"),
        sha256: await hashFile(published), bytes: info.size});
    }
    startupEvent("harness.process", {runId, operationId, outcome: "completed",
      durationMs: Math.round(performance.now() - started), files: files.length,
      bytes: files.reduce((total, file) => total + file.bytes, 0), uploads: input.uploads.length,
      language: input.language, outputTruncated: result.outputTruncated});
    return {ok: true, files, output: result.output, outputTruncated: result.outputTruncated,
      inputDigest: result.inputDigest};
  } catch (error) {
    startupEvent("harness.process", {runId, operationId,
      outcome: signal?.aborted ? "cancelled" : "failed",
      durationMs: Math.round(performance.now() - started), uploads: input.uploads.length,
      language: input.language});
    throw error;
  } finally {
    await rm(inputRoot, {recursive: true, force: true});
  }
}
