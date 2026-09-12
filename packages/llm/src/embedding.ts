// Embedding computation belongs to the shared, lazily started service.
// Keep the model and vector contract identical to existing pgvector rows.
export const EMBEDDING_DIM = 384;
const EMBEDDING_MODEL = "Xenova/all-MiniLM-L6-v2";

export async function embedText(text: string, timeoutMs = 15_000): Promise<number[]> {
  const trimmed = text.trim();
  if (!trimmed) throw new Error("embedText: text is empty");
  if (trimmed.length > 32000) throw new Error("embedText: text exceeds 32000 characters");
  const baseUrl = process.env.NEKO_EMBEDDING_URL?.trim();
  if (!baseUrl) throw new Error("NEKO_EMBEDDING_URL is not configured");
  const response = await fetch(`${baseUrl.replace(/\/+$/, "")}/v1/embeddings`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ text: trimmed }),
    // Interactive queries have a short deadline; durable jobs allow cold startup.
    signal: AbortSignal.timeout(timeoutMs),
    redirect: "error",
  });
  if (!response.ok) throw new Error(`embedding service returned HTTP ${response.status}`);
  const body = await response.json();
  if (body?.model !== EMBEDDING_MODEL || body?.dimensions !== EMBEDDING_DIM ||
      !Array.isArray(body?.vector) || body.vector.length !== EMBEDDING_DIM ||
      !body.vector.every((value: unknown) => typeof value === "number" && Number.isFinite(value))) {
    throw new Error("embedding service returned an incompatible vector");
  }
  return body.vector;
}

// Format a JS number[] as the literal pgvector accepts when bound as text.
// pgvector wants `[0.1,0.2,...]` (square brackets, comma-separated, no spaces).
export function vectorLiteral(vec: number[]): string {
  return `[${vec.join(",")}]`;
}
