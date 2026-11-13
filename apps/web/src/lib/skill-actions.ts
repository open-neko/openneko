export type SkillScope = "team" | "own";

async function postSkill(path: string, body?: unknown): Promise<{ name: string; scope: SkillScope }> {
  const response = await fetch(path, {
    method: "POST",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = (await response.json().catch(() => ({}))) as { name?: string; scope?: SkillScope; error?: string };
  if (!response.ok || !data.name || !data.scope) throw new Error(data.error ?? "The skill could not be saved.");
  return { name: data.name, scope: data.scope };
}

export function copySkill(source: string, name: string) {
  return postSkill(`/api/v1/work/skills/${encodeURIComponent(source)}/copy`, { name });
}

export function shareSkill(name: string) {
  return postSkill(`/api/v1/work/skills/${encodeURIComponent(name)}/share`);
}

export function suggestedCopyName(source: string): string {
  return `${source}-copy`.slice(0, 64);
}
