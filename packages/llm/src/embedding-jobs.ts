import { pool } from '@neko/db';
import { embedText, embeddingModel, vectorLiteral } from './embedding';

export type EmbeddingIndexPayload = { kind: 'memory' | 'skill'; orgId: string; id: string };

export async function runEmbeddingIndexJob(payload: EmbeddingIndexPayload): Promise<void> {
  if (!payload || !['memory', 'skill'].includes(payload.kind) ||
      typeof payload.orgId !== 'string' || typeof payload.id !== 'string') {
    throw new Error('Invalid embedding job');
  }
  const model = embeddingModel();
  if (!model) return;
  const memory = payload.kind === 'memory';
  const table = { memory: 'work_memory', skill: 'skill_embedding' }[payload.kind];
  const expression = {
    memory: 'text',
    skill: 'document',
  }[payload.kind];
  const stale = '(embedding IS NULL OR embedding_model IS DISTINCT FROM $3)';
  const eligible = payload.kind === 'skill'
    ? stale
    : `${stale} AND archived_at IS NULL${memory ? ' AND NOT suppressed AND (expires_at IS NULL OR expires_at > now())' : ''}`;
  const client = await pool().connect();
  try {
    await client.query('BEGIN');
    // Cross-worker admission: no more than one background model request.
    const lock = await client.query("SELECT pg_try_advisory_xact_lock(hashtext('embedding-processing')) AS acquired");
    if (!lock.rows[0].acquired) throw new Error('Embedding processing is busy');
    const { rows } = await client.query(`SELECT ${expression} AS content FROM ${table}
      WHERE org_id = $1 AND id = $2 AND ${eligible}`, [payload.orgId, payload.id, model]);
    if (rows.length) {
      const content: string = rows[0].content;
      const vector = vectorLiteral(await embedText(content.slice(0, memory ? 32000 : 4000), 150_000));
      // A concurrent edit/archive/delete must never receive an obsolete vector.
      await client.query(`UPDATE ${table} SET embedding = $4::vector, embedding_model = $3
        WHERE org_id = $1 AND id = $2 AND ${eligible} AND ${expression} = $5`,
      [payload.orgId, payload.id, model, vector, content]);
    }
    await client.query('COMMIT');
  } finally {
    await client.query('ROLLBACK').catch(() => {});
    client.release();
  }
}
