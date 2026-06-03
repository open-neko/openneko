import { relations, sql } from "drizzle-orm";
import {
  bigserial,
  boolean,
  customType,
  date,
  index,
  integer,
  jsonb,
  numeric,
  pgTable,
  real,
  smallint,
  text,
  timestamp,
  uniqueIndex,
  uuid,
} from "drizzle-orm/pg-core";

const ts = (name: string) =>
  timestamp(name, { withTimezone: true, mode: "date" });

// pgvector column. Stored as `vector(N)`; we don't read it back into JS
// (similarity search uses raw SQL with the `<=>` operator). Drizzle just
// needs to know the type exists so inserts typecheck.
const vector = (name: string, dim: number) =>
  customType<{ data: string; driverData: string }>({
    dataType() {
      return `vector(${dim})`;
    },
  })(name);

export const organization = pgTable(
  "organization",
  {
    id: text("id").primaryKey(),
    scalekit_org_id: text("scalekit_org_id"),
    name: text("name").notNull(),
    domain: text("domain"),
    features: text("features").notNull().default(""),
    status: text("status").notNull().default("active"),
    setup_complete_at: ts("setup_complete_at"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    domain_unique: uniqueIndex("organization_domain_unique")
      .on(t.domain)
      .where(sql`${t.domain} is not null`),
  }),
);

export const app_user = pgTable(
  "app_user",
  {
    id: text("id").primaryKey(),
    sub: text("sub"),
    email: text("email").notNull(),
    name: text("name"),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    role: text("role").notNull(),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
    last_login_at: ts("last_login_at"),
  },
  (t) => ({
    org_idx: index("app_user_org_idx").on(t.org_id),
  }),
);

export const data_source = pgTable(
  "data_source",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    kind: text("kind").notNull(),
    graphql_url: text("graphql_url").notNull(),
    subscription_url: text("subscription_url"),
    label: text("label"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
    mcp_url: text("mcp_url"),
  },
  (t) => ({
    org_idx: index("data_source_org_idx").on(t.org_id),
  }),
);

export const onboarding_wizard = pgTable("onboarding_wizard", {
  org_id: text("org_id")
    .primaryKey()
    .references(() => organization.id, { onDelete: "cascade" }),
  company_note: text("company_note"),
  fiscal_year_start_month: smallint("fiscal_year_start_month"),
  active_seats: text("active_seats")
    .array()
    .notNull()
    .default(sql`'{}'::text[]`),
  priorities: text("priorities")
    .array()
    .notNull()
    .default(sql`'{}'::text[]`),
  step: text("step").notNull().default("company"),
  submitted_at: ts("submitted_at"),
  created_at: ts("created_at").notNull().defaultNow(),
  updated_at: ts("updated_at").notNull().defaultNow(),
});

export const processing_job = pgTable(
  "processing_job",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    kind: text("kind").notNull(),
    status: text("status").notNull().default("queued"),
    trigger: text("trigger").notNull(),
    trigger_payload: jsonb("trigger_payload"),
    progress: jsonb("progress").notNull().default(sql`'{}'::jsonb`),
    result: jsonb("result"),
    error: text("error"),
    started_at: ts("started_at"),
    finished_at: ts("finished_at"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_status_idx: index("processing_job_org_status_idx").on(
      t.org_id,
      t.kind,
      t.status,
    ),
    org_recent_idx: index("processing_job_org_recent_idx").on(
      t.org_id,
      t.created_at.desc(),
    ),
  }),
);

export const customer_profile = pgTable(
  "customer_profile",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    version: integer("version").notNull(),
    is_current: boolean("is_current").notNull().default(false),
    company_note: text("company_note"),
    fiscal_year_start_month: smallint("fiscal_year_start_month"),
    declared_priorities: text("declared_priorities")
      .array()
      .notNull()
      .default(sql`'{}'::text[]`),
    industry_insights: text("industry_insights"),
    built_at: ts("built_at").notNull().defaultNow(),
    built_by_job: uuid("built_by_job"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
    business_profile: text("business_profile"),
    industry_insights_research_task: text("industry_insights_research_task"),
  },
  (t) => ({
    org_version_unique: uniqueIndex("customer_profile_org_version_unique").on(
      t.org_id,
      t.version,
    ),
    one_current_per_org: uniqueIndex("one_current_profile_per_org")
      .on(t.org_id)
      .where(sql`${t.is_current}`),
  }),
);

export const metric = pgTable(
  "metric",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    role: text("role").notNull(),
    slug: text("slug").notNull(),
    source: text("source").notNull(),
    title: text("title").notNull(),
    description: text("description"),
    why: text("why"),
    chart_hint: text("chart_hint"),
    unit: text("unit"),
    direction_good: text("direction_good"),
    cadence: text("cadence").notNull().default("daily"),
    active: boolean("active").notNull().default(true),
    created_by_job: uuid("created_by_job").references(() => processing_job.id),
    // Denormalized refresh status — read by /api/briefing so the dashboard
    // can render pending vs failed cards without joining processing_job.
    // Writers: metric-refresh.ts (success/failure) and bootstrap-metrics-build.ts
    // ('pending' on insert).
    last_refresh_status: text("last_refresh_status"),
    last_refresh_error: text("last_refresh_error"),
    last_refresh_job_id: uuid("last_refresh_job_id").references(() => processing_job.id),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_role_slug_unique: uniqueIndex("metric_org_role_slug_unique").on(
      t.org_id,
      t.role,
      t.slug,
    ),
    org_role_active_idx: index("metric_org_role_active_idx")
      .on(t.org_id, t.role)
      .where(sql`${t.active}`),
    org_refresh_status_idx: index("metric_org_refresh_status_idx")
      .on(t.org_id, t.last_refresh_status)
      .where(sql`${t.last_refresh_status} is not null`),
  }),
);

export const metric_snapshot = pgTable(
  "metric_snapshot",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    metric_id: uuid("metric_id")
      .notNull()
      .references(() => metric.id, { onDelete: "cascade" }),
    captured_at: ts("captured_at").notNull().defaultNow(),
    value: numeric("value"),
    value_json: jsonb("value_json"),
    baseline: numeric("baseline"),
    delta_pct: numeric("delta_pct"),
    status: text("status"),
    created_at: ts("created_at").notNull().defaultNow(),
    payload: jsonb("payload"),
  },
  (t) => ({
    metric_recent_idx: index("metric_snapshot_metric_recent_idx").on(
      t.metric_id,
      t.captured_at.desc(),
    ),
  }),
);

export const dashboard_pin = pgTable(
  "dashboard_pin",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id").notNull(),
    role: text("role").notNull(),
    metric_id: uuid("metric_id")
      .notNull()
      .references(() => metric.id, { onDelete: "cascade" }),
    sort_order: integer("sort_order").notNull().default(0),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    org_role_metric_unique: uniqueIndex(
      "dashboard_pin_org_role_metric_unique",
    ).on(t.org_id, t.role, t.metric_id),
  }),
);

// Operator-curated pin: links a workflow_output to the Briefing's pinned
// section. See migration 0013_briefing_finding_pin.sql for the column
// rationale. Forward-declared above workflow_output (which it references)
// using sql"workflow_output" deferred resolution — the actual FK constraint
// is enforced by the migration's REFERENCES clause.
export const briefing_finding_pin = pgTable(
  "briefing_finding_pin",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id").notNull(),
    output_id: uuid("output_id").notNull(),
    sort_order: integer("sort_order").notNull().default(0),
    pinned_by_user_id: text("pinned_by_user_id"),
    pinned_at: ts("pinned_at").notNull().defaultNow(),
  },
  (t) => ({
    org_output_unique: uniqueIndex("briefing_finding_pin_org_output_unique").on(
      t.org_id,
      t.output_id,
    ),
    org_idx: index("briefing_finding_pin_org_idx").on(
      t.org_id,
      t.sort_order,
      t.pinned_at.desc(),
    ),
  }),
);

export const briefing = pgTable(
  "briefing",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    role: text("role").notNull(),
    for_date: date("for_date").notNull(),
    profile_version: integer("profile_version").notNull(),
    summary_md: text("summary_md").notNull(),
    insights: jsonb("insights").notNull().default(sql`'[]'::jsonb`),
    generated_by_job: uuid("generated_by_job").references(
      () => processing_job.id,
    ),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_role_date_unique: uniqueIndex("briefing_org_role_date_unique").on(
      t.org_id,
      t.role,
      t.for_date,
    ),
    org_recent_idx: index("briefing_org_recent_idx").on(
      t.org_id,
      t.for_date.desc(),
    ),
  }),
);

export const source_change_log = pgTable(
  "source_change_log",
  {
    id: bigserial("id", { mode: "number" }).primaryKey(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    source_id: uuid("source_id")
      .notNull()
      .references(() => data_source.id, { onDelete: "cascade" }),
    table_name: text("table_name").notNull(),
    change_kind: text("change_kind").notNull(),
    observed_at: ts("observed_at").notNull().defaultNow(),
    payload: jsonb("payload"),
  },
  (t) => ({
    org_recent_idx: index("source_change_log_org_recent_idx").on(
      t.org_id,
      t.observed_at.desc(),
    ),
  }),
);

export const reprofile_debounce = pgTable("reprofile_debounce", {
  org_id: text("org_id")
    .primaryKey()
    .references(() => organization.id, { onDelete: "cascade" }),
  last_change_at: ts("last_change_at").notNull(),
  last_built_at: ts("last_built_at"),
  pending_job_id: uuid("pending_job_id").references(() => processing_job.id),
  updated_at: ts("updated_at").notNull().defaultNow(),
});

export const metricRelations = relations(metric, ({ many }) => ({
  snapshots: many(metric_snapshot),
  pins: many(dashboard_pin),
}));

export const metric_snapshotRelations = relations(metric_snapshot, ({ one }) => ({
  metric: one(metric, {
    fields: [metric_snapshot.metric_id],
    references: [metric.id],
  }),
}));

export const dashboard_pinRelations = relations(dashboard_pin, ({ one }) => ({
  metric: one(metric, {
    fields: [dashboard_pin.metric_id],
    references: [metric.id],
  }),
}));

export const llm_provider_config = pgTable(
  "llm_provider_config",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    scope: text("scope").notNull(),
    provider: text("provider").notNull(),
    model: text("model"),
    label: text("label"),
    enabled: boolean("enabled").notNull().default(true),
    config: jsonb("config").notNull().default(sql`'{}'::jsonb`),
    secrets: jsonb("secrets").notNull().default(sql`'{}'::jsonb`),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_scope_unique: uniqueIndex("llm_provider_config_org_scope_unique").on(
      t.org_id,
      t.scope,
    ),
    org_idx: index("llm_provider_config_org_idx").on(t.org_id),
  }),
);

export const work_thread = pgTable(
  "work_thread",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    title: text("title").notNull().default(""),
    backend_state: jsonb("backend_state").notNull().default(sql`'{}'::jsonb`),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
    last_message_at: ts("last_message_at").notNull().defaultNow(),
  },
  (t) => ({
    org_recent_idx: index("work_thread_org_recent_idx").on(
      t.org_id,
      t.last_message_at.desc(),
      t.created_at.desc(),
    ),
  }),
);

export const work_run = pgTable(
  "work_run",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    thread_id: uuid("thread_id")
      .notNull()
      .references(() => work_thread.id, { onDelete: "cascade" }),
    backend: text("backend").notNull(),
    status: text("status").notNull().default("running"),
    error: text("error"),
    // Agent-estimated minutes of human effort the run's ANALYSIS saved
    // (excludes per-action work, which is tracked on action_request). Null
    // until the run emits a value estimate; server-clamped before write.
    analysis_minutes_saved: integer("analysis_minutes_saved"),
    analysis_minutes_basis: text("analysis_minutes_basis"),
    estimate_version: integer("estimate_version").notNull().default(1),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
    finished_at: ts("finished_at"),
  },
  (t) => ({
    thread_created_idx: index("work_run_thread_created_idx").on(
      t.thread_id,
      t.created_at.asc(),
    ),
    org_finished_idx: index("work_run_org_finished_idx").on(
      t.org_id,
      t.finished_at,
    ),
  }),
);

export const work_message = pgTable(
  "work_message",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    thread_id: uuid("thread_id")
      .notNull()
      .references(() => work_thread.id, { onDelete: "cascade" }),
    run_id: uuid("run_id").references(() => work_run.id, { onDelete: "set null" }),
    role: text("role").notNull(),
    content: text("content").notNull(),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    thread_created_idx: index("work_message_thread_created_idx").on(
      t.thread_id,
      t.created_at.asc(),
    ),
  }),
);

export const work_run_event = pgTable(
  "work_run_event",
  {
    // `id` is a Postgres bigserial — globally monotonic, guaranteed
    // unique. Use it as the canonical ordering key ("ORDER BY id ASC")
    // and as the SSE tail cursor ("WHERE id > $lastId"). The old `seq`
    // column was per-run, computed by app code, and racy when two
    // writers (work-run + action-execute) emitted close in time —
    // surfacing as "duplicate key value violates unique constraint
    // work_run_event_run_seq_unique" errors in the chat. Migration
    // 0022 dropped it.
    id: bigserial("id", { mode: "number" }).primaryKey(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    thread_id: uuid("thread_id")
      .notNull()
      .references(() => work_thread.id, { onDelete: "cascade" }),
    run_id: uuid("run_id")
      .notNull()
      .references(() => work_run.id, { onDelete: "cascade" }),
    kind: text("kind").notNull(),
    payload: jsonb("payload").notNull(),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    run_id_idx: index("work_run_event_run_id_idx").on(t.run_id, t.id),
    thread_id_idx: index("work_run_event_thread_id_idx").on(t.thread_id, t.id),
  }),
);

export const work_memory = pgTable(
  "work_memory",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    kind: text("kind").notNull(),
    scope: text("scope").notNull(),
    scope_id: text("scope_id"),
    text: text("text").notNull(),
    pinned: boolean("pinned").notNull().default(false),
    confidence: real("confidence").notNull().default(0.8),
    metadata: jsonb("metadata").notNull().default(sql`'{}'::jsonb`),
    source_run_id: uuid("source_run_id").references(() => work_run.id, {
      onDelete: "set null",
    }),
    source_thread_id: uuid("source_thread_id").references(() => work_thread.id, {
      onDelete: "set null",
    }),
    use_count: integer("use_count").notNull().default(0),
    last_used_at: ts("last_used_at"),
    archived_at: ts("archived_at"),
    embedding: vector("embedding", 384),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_active_idx: index("work_memory_org_active_idx").on(
      t.org_id,
      t.archived_at,
      t.updated_at.desc(),
    ),
    org_scope_idx: index("work_memory_org_scope_idx").on(
      t.org_id,
      t.scope,
      t.scope_id,
      t.archived_at,
    ),
    org_pinned_idx: index("work_memory_org_pinned_idx").on(
      t.org_id,
      t.pinned,
      t.archived_at,
      t.updated_at.desc(),
    ),
  }),
);

export const work_memory_event = pgTable(
  "work_memory_event",
  {
    id: bigserial("id", { mode: "number" }).primaryKey(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    memory_id: uuid("memory_id").references(() => work_memory.id, {
      onDelete: "set null",
    }),
    run_id: uuid("run_id").references(() => work_run.id, { onDelete: "set null" }),
    thread_id: uuid("thread_id").references(() => work_thread.id, {
      onDelete: "set null",
    }),
    action: text("action").notNull(),
    payload: jsonb("payload").notNull().default(sql`'{}'::jsonb`),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    memory_idx: index("work_memory_event_memory_idx").on(t.memory_id, t.id.desc()),
    org_recent_idx: index("work_memory_event_org_recent_idx").on(
      t.org_id,
      t.id.desc(),
    ),
  }),
);

export const work_pending_memory = pgTable(
  "work_pending_memory",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    thread_id: uuid("thread_id").references(() => work_thread.id, {
      onDelete: "cascade",
    }),
    run_id: uuid("run_id").references(() => work_run.id, { onDelete: "set null" }),
    status: text("status").notNull().default("proposed"),
    draft_text: text("draft_text").notNull(),
    draft_kind: text("draft_kind").notNull(),
    draft_scope: text("draft_scope").notNull(),
    draft_scope_id: text("draft_scope_id"),
    confidence: real("confidence").notNull(),
    reasoning: text("reasoning"),
    conflict: jsonb("conflict"),
    decision_text: text("decision_text"),
    decided_at: ts("decided_at"),
    memory_id: uuid("memory_id").references(() => work_memory.id, {
      onDelete: "set null",
    }),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    thread_status_idx: index("work_pending_memory_thread_status_idx").on(
      t.org_id,
      t.thread_id,
      t.status,
      t.created_at.desc(),
    ),
    run_status_idx: index("work_pending_memory_run_status_idx").on(
      t.org_id,
      t.run_id,
      t.status,
      t.created_at.desc(),
    ),
    org_status_idx: index("work_pending_memory_org_status_idx").on(
      t.org_id,
      t.status,
      t.created_at.desc(),
    ),
  }),
);

export const workflow_definition = pgTable(
  "workflow_definition",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    description: text("description").notNull().default(""),
    enabled: boolean("enabled").notNull().default(true),
    status: text("status").notNull().default("active"),
    goal: text("goal").notNull().default(""),
    system_prompt_overlay: text("system_prompt_overlay").notNull().default(""),
    steps: jsonb("steps").notNull().default(sql`'[]'::jsonb`),
    cron: text("cron"),
    cron_timezone: text("cron_timezone").notNull().default("UTC"),
    cron_enabled: boolean("cron_enabled").notNull().default(true),
    daily_run_budget: integer("daily_run_budget"),
    output_contract: jsonb("output_contract"),
    created_by_thread_id: uuid("created_by_thread_id").references(
      () => work_thread.id,
      { onDelete: "set null" },
    ),
    created_by_run_id: uuid("created_by_run_id").references(() => work_run.id, {
      onDelete: "set null",
    }),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_idx: index("workflow_definition_org_idx").on(
      t.org_id,
      t.enabled,
      t.updated_at.desc(),
    ),
    org_name_unique: uniqueIndex("workflow_definition_org_name_unique").on(
      t.org_id,
      t.name,
    ),
    cron_active_idx: index("workflow_definition_cron_active_idx")
      .on(t.org_id, t.cron_enabled)
      .where(sql`${t.cron} is not null and ${t.enabled} = true`),
  }),
);

export const workflow_run = pgTable(
  "workflow_run",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    workflow_id: uuid("workflow_id")
      .notNull()
      .references(() => workflow_definition.id, { onDelete: "cascade" }),
    thread_id: uuid("thread_id")
      .notNull()
      .references(() => work_thread.id, { onDelete: "cascade" }),
    work_run_id: uuid("work_run_id")
      .notNull()
      .references(() => work_run.id, { onDelete: "cascade" }),
    trigger_kind: text("trigger_kind").notNull(),
    trigger_payload: jsonb("trigger_payload")
      .notNull()
      .default(sql`'{}'::jsonb`),
    triggered_by_subscription_id: uuid("triggered_by_subscription_id"),
    triggered_by_output_id: uuid("triggered_by_output_id"),
    triggered_by_observation_id: uuid("triggered_by_observation_id"),
    chain_depth: integer("chain_depth").notNull().default(0),
    status: text("status").notNull().default("queued"),
    started_at: ts("started_at"),
    finished_at: ts("finished_at"),
    summary: text("summary"),
    error: text("error"),
    source_writes: jsonb("source_writes")
      .notNull()
      .default(sql`'[]'::jsonb`),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    work_run_unique: uniqueIndex("workflow_run_work_run_unique").on(
      t.work_run_id,
    ),
    workflow_created_idx: index("workflow_run_workflow_created_idx").on(
      t.workflow_id,
      t.created_at.desc(),
    ),
    org_status_idx: index("workflow_run_org_status_idx").on(
      t.org_id,
      t.status,
      t.created_at.desc(),
    ),
  }),
);

export const workflow_output = pgTable(
  "workflow_output",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    workflow_run_id: uuid("workflow_run_id")
      .notNull()
      .references(() => workflow_run.id, { onDelete: "cascade" }),
    work_run_id: uuid("work_run_id")
      .notNull()
      .references(() => work_run.id, { onDelete: "cascade" }),
    kind: text("kind").notNull(),
    title: text("title").notNull().default(""),
    body: text("body").notNull().default(""),
    payload: jsonb("payload").notNull().default(sql`'{}'::jsonb`),
    artifact_path: text("artifact_path"),
    scope: text("scope"),
    topic: text("topic"),
    mood: text("mood"),
    time_window_start: ts("time_window_start"),
    time_window_end: ts("time_window_end"),
    freshness_ttl_seconds: integer("freshness_ttl_seconds"),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    run_created_idx: index("workflow_output_run_created_idx").on(
      t.workflow_run_id,
      t.created_at.desc(),
    ),
    org_scope_idx: index("workflow_output_org_scope_idx")
      .on(t.org_id, t.scope, t.created_at.desc())
      .where(sql`${t.scope} is not null`),
    org_mood_idx: index("workflow_output_org_mood_idx")
      .on(t.org_id, t.mood, t.created_at.desc())
      .where(sql`${t.mood} is not null`),
  }),
);

// Routes a workflow output (for an audience) to a channel plugin's deliver RPC.
// The web channel is implicit/always-on; rows here add extra membranes
// (Telegram, Slack, …) per the V2 frontend-as-capability design. `recipient`
// is the channel-native address minted at config time.
export const delivery_binding = pgTable(
  "delivery_binding",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    audience: text("audience").notNull().default("*"),
    channel_plugin: text("channel_plugin").notNull(),
    recipient: jsonb("recipient").notNull().default(sql`'{}'::jsonb`),
    filter: jsonb("filter"),
    enabled: boolean("enabled").notNull().default(true),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_enabled_idx: index("delivery_binding_org_enabled_idx").on(
      t.org_id,
      t.enabled,
    ),
  }),
);

export const subscription = pgTable(
  "subscription",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    workflow_id: uuid("workflow_id")
      .notNull()
      .references(() => workflow_definition.id, { onDelete: "cascade" }),
    source_kind: text("source_kind").notNull(),
    filter: jsonb("filter").notNull().default(sql`'{}'::jsonb`),
    enabled: boolean("enabled").notNull().default(true),
    debounce_ms: integer("debounce_ms").notNull().default(0),
    max_concurrent_runs: integer("max_concurrent_runs").notNull().default(5),
    max_chain_depth_override: integer("max_chain_depth_override"),
    idempotency_key_template: text("idempotency_key_template"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    workflow_idx: index("subscription_workflow_idx").on(t.workflow_id),
    org_enabled_idx: index("subscription_org_enabled_idx")
      .on(t.org_id, t.enabled)
      .where(sql`${t.enabled} = true`),
    source_kind_idx: index("subscription_source_kind_idx")
      .on(t.source_kind)
      .where(sql`${t.enabled} = true`),
  }),
);

export const observation = pgTable(
  "observation",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    source_output_id: uuid("source_output_id").references(
      () => workflow_output.id,
      { onDelete: "set null" },
    ),
    consumer_kind: text("consumer_kind").notNull(),
    consumer_workflow_id: uuid("consumer_workflow_id").references(
      () => workflow_definition.id,
      { onDelete: "set null" },
    ),
    consumer_run_id: uuid("consumer_run_id").references(() => workflow_run.id, {
      onDelete: "set null",
    }),
    consumer_user_id: text("consumer_user_id"),
    subscription_id: uuid("subscription_id").references(() => subscription.id, {
      onDelete: "set null",
    }),
    title: text("title"),
    body: text("body"),
    mood: text("mood"),
    status: text("status").notNull().default("active"),
    first_seen_at: ts("first_seen_at").notNull().defaultNow(),
    last_seen_at: ts("last_seen_at").notNull().defaultNow(),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    source_output_idx: index("observation_source_output_idx")
      .on(t.source_output_id)
      .where(sql`${t.source_output_id} is not null`),
    consumer_workflow_idx: index("observation_consumer_workflow_idx")
      .on(t.consumer_workflow_id, t.created_at.desc())
      .where(sql`${t.consumer_workflow_id} is not null`),
    subscription_idx: index("observation_subscription_idx")
      .on(t.subscription_id, t.created_at.desc())
      .where(sql`${t.subscription_id} is not null`),
    org_status_idx: index("observation_org_status_idx").on(
      t.org_id,
      t.status,
      t.created_at.desc(),
    ),
  }),
);

export const workflow_output_source_observation = pgTable(
  "workflow_output_source_observation",
  {
    workflow_output_id: uuid("workflow_output_id")
      .notNull()
      .references(() => workflow_output.id, { onDelete: "cascade" }),
    observation_id: uuid("observation_id")
      .notNull()
      .references(() => observation.id, { onDelete: "cascade" }),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    pk: uniqueIndex("workflow_output_source_obs_pk").on(
      t.workflow_output_id,
      t.observation_id,
    ),
    obs_idx: index("workflow_output_source_obs_obs_idx").on(t.observation_id),
  }),
);

export const action_policy = pgTable(
  "action_policy",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    description: text("description").notNull().default(""),
    applies_to_kinds: text("applies_to_kinds")
      .array()
      .notNull()
      .default(sql`'{}'::text[]`),
    applies_to_scopes: text("applies_to_scopes")
      .array()
      .notNull()
      .default(sql`'{}'::text[]`),
    mode: text("mode").notNull(),
    risk_threshold_auto_approve: text("risk_threshold_auto_approve"),
    allowed_targets: jsonb("allowed_targets"),
    denied_targets: jsonb("denied_targets"),
    limits: jsonb("limits").notNull().default(sql`'{}'::jsonb`),
    approver_role: text("approver_role"),
    priority: integer("priority").notNull().default(100),
    enabled: boolean("enabled").notNull().default(true),
    created_by_thread_id: uuid("created_by_thread_id").references(
      () => work_thread.id,
      { onDelete: "set null" },
    ),
    created_by_run_id: uuid("created_by_run_id").references(() => work_run.id, {
      onDelete: "set null",
    }),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_enabled_priority_idx: index("action_policy_org_enabled_priority_idx").on(
      t.org_id,
      t.enabled,
      t.priority,
    ),
  }),
);

export const action_request = pgTable(
  "action_request",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    workflow_run_id: uuid("workflow_run_id").references(
      () => workflow_run.id,
      { onDelete: "cascade" },
    ),
    triggered_by_observation_id: uuid("triggered_by_observation_id").references(
      () => observation.id,
      { onDelete: "set null" },
    ),
    policy_id: uuid("policy_id").references(() => action_policy.id, {
      onDelete: "set null",
    }),
    scope: text("scope").notNull(),
    kind: text("kind").notNull(),
    target: text("target"),
    payload: jsonb("payload").notNull().default(sql`'{}'::jsonb`),
    risk_level: text("risk_level"),
    status: text("status").notNull().default("pending_approval"),
    summary: text("summary"),
    intent: text("intent"),
    // Agent-estimated minutes of human effort this action saved, counted
    // toward rollups only once status reaches "executed". Server-clamped.
    minutes_saved: integer("minutes_saved"),
    minutes_saved_basis: text("minutes_saved_basis"),
    estimate_source: text("estimate_source").notNull().default("agent"),
    estimate_version: integer("estimate_version").notNull().default(1),
    work_run_id: uuid("work_run_id").references(() => work_run.id, {
      onDelete: "set null",
    }),
    requested_by_run_id: uuid("requested_by_run_id").references(
      () => workflow_run.id,
      { onDelete: "set null" },
    ),
    approved_by_user_id: text("approved_by_user_id").references(
      () => app_user.id,
      { onDelete: "set null" },
    ),
    approved_at: ts("approved_at"),
    rejection_reason: text("rejection_reason"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_status_idx: index("action_request_org_status_idx").on(
      t.org_id,
      t.status,
      t.created_at.desc(),
    ),
    workflow_run_idx: index("action_request_workflow_run_idx").on(
      t.workflow_run_id,
      t.created_at.desc(),
    ),
    pending_idx: index("action_request_pending_idx").on(
      t.org_id,
      t.created_at.desc(),
    ),
  }),
);

export const action_execution = pgTable(
  "action_execution",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    action_request_id: uuid("action_request_id")
      .notNull()
      .references(() => action_request.id, { onDelete: "cascade" }),
    executor: text("executor").notNull(),
    command_or_operation: text("command_or_operation"),
    payload: jsonb("payload"),
    result: jsonb("result"),
    external_ref: text("external_ref"),
    status: text("status").notNull().default("pending"),
    error: text("error"),
    started_at: ts("started_at"),
    finished_at: ts("finished_at"),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    request_idx: index("action_execution_request_idx").on(
      t.action_request_id,
      t.created_at.desc(),
    ),
    org_status_idx: index("action_execution_org_status_idx").on(
      t.org_id,
      t.status,
      t.created_at.desc(),
    ),
  }),
);
