// Acceptance driver: real queue and production handler, isolated synthetic stack only.
import assert from 'node:assert/strict';
import { db, pool, getOrgId, organization, customer_profile, data_source, llm_provider_config, processing_job, eq } from '@neko/db';
import { boss, enqueue, QUEUE, type WorkRunPayload } from '@neko/db/jobs';
import { createWorkThread, createWorkRun, getWorkRun } from '@neko/llm/work';
import { runWorkRun } from '../src/jobs/work-run.js';
if (process.env.HARNESS_M3_LIVE !== '1' || process.env.NEKO_PG_PORT !== '18119')
    throw Error('isolated M3 environment required');
const orgId = await getOrgId();
await db().update(organization).set({ setup_complete_at: new Date() }).where(eq(organization.id, orgId));
await db().insert(llm_provider_config).values({ org_id: orgId, scope: 'primary', provider: 'ollama', model: 'harness-fixture', config: { url: 'http://host.docker.internal:18118' } }).onConflictDoNothing();
await db().insert(data_source).values({ org_id: orgId, graphql_url: 'http://127.0.0.1:18117/api/v1/graphql', kind: 'graphjin', auth_mode: 'none', is_default: true }).onConflictDoNothing();
await db().insert(customer_profile).values({ org_id: orgId, version: 1, is_current: true, company_note: 'Synthetic reference business', business_profile: 'Seeded reference data for Harness acceptance' }).onConflictDoNothing();
if (process.argv.includes('--seed-only'))
    process.exit(0);
const queue = await boss();
await queue.createQueue(QUEUE.WORK_RUN);
await queue.work<WorkRunPayload>(QUEUE.WORK_RUN, async (jobs) => {
    for (const job of jobs) {
        const { processingJobId, orgId, ...payload } = job.data;
        await db().update(processing_job).set({ status: 'running' }).where(eq(processing_job.id, processingJobId));
        try {
            await runWorkRun(processingJobId, orgId, { ...payload, channel: 'web' });
            await db().update(processing_job).set({ status: 'succeeded' }).where(eq(processing_job.id, processingJobId));
        }
        catch (e) {
            await db().update(processing_job).set({ status: 'failed' }).where(eq(processing_job.id, processingJobId));
            throw e;
        }
    }
});
const thread = await createWorkThread(orgId, 'M3 queued lookup');
const run = await createWorkRun(orgId, thread.id, 'harness', { userId: null, role: 'service' });
const [job] = await db().insert(processing_job).values({ org_id: orgId, kind: QUEUE.WORK_RUN, trigger: 'test' }).returning();
await enqueue(QUEUE.WORK_RUN, { processingJobId: job.id, orgId, runId: run.id, threadId: thread.id, message: 'Find the seeded reference using lookup.' }, { retryLimit: 0 });
for (let n = 0; n < 120; n++) {
    const current = await getWorkRun(orgId, run.id);
    if (current && ['completed', 'failed', 'cancelled'].includes(current.status)) {
        assert.equal(current.status, 'completed', JSON.stringify(current));
        const [finished] = await db().select().from(processing_job).where(eq(processing_job.id, job.id));
        if (finished.status !== 'succeeded') {
            await new Promise(r => setTimeout(r, 100));
            continue;
        }
        await queue.stop({ graceful: true, timeout: 5000 });
        await pool().end();
        console.log('M3_QUEUE_PASS', run.id);
        process.exit(0);
    }
    await new Promise(r => setTimeout(r, 1000));
}
throw Error('queued run timed out');
