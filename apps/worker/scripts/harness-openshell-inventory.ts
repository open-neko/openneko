// Isolated acceptance helper for OpenShell 0.1.2's paginated inventory.
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const runCommand = promisify(execFile);

export async function sandboxExists(cli: string, name: string): Promise<boolean> {
  let token = "";
  const seen = new Set<string>();
  do {
    const { stdout } = await runCommand(cli, ["--gateway", "harness-m2", "sandbox", "list",
      "-o", "json", "--page-size", "500", ...(token ? ["--page-token", token] : [])], { timeout: 5000 });
    const page: unknown = JSON.parse(stdout);
    if (!page || typeof page !== "object" || !Array.isArray((page as {sandboxes?: unknown}).sandboxes) ||
        typeof (page as {next_page_token?: unknown}).next_page_token !== "string" ||
        !(page as {sandboxes: unknown[]}).sandboxes.every(box => box && typeof box === "object" &&
          typeof (box as {name?: unknown}).name === "string")) {
      throw Error("invalid OpenShell sandbox inventory");
    }
    if ((page as {sandboxes: Array<{name: string}>}).sandboxes.some(box => box.name === name)) return true;
    token = (page as {next_page_token: string}).next_page_token;
    if (token && (seen.has(token) || seen.size >= 1000)) throw Error("incomplete OpenShell sandbox inventory");
    if (token) seen.add(token);
  } while (token);
  return false;
}

export async function sandboxWorkloadContainer(name: string, image: string): Promise<string | null> {
  const { stdout } = await runCommand("docker", ["ps", "--filter", `label=openshell.ai/sandbox-name=${name}`,
    "--filter", `ancestor=${image}`, "--format", "{{.ID}}"], {timeout: 5000});
  const matches = stdout.trim().split("\n").filter(Boolean);
  if (matches.length > 1) throw Error("ambiguous OpenShell workload container");
  return matches[0] ?? null;
}
