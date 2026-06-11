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
  primaryKey,
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
    // ADM1: a deactivated user can't sign in and their sessions are dead.
    disabled_at: ts("disabled_at"),
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
    // GJ4: 'none' = anonymous legacy; 'jwt' = source mode w/ actor tokens.
    auth_mode: text("auth_mode").notNull().default("none"),
    // ADM2 registry: stable per-org name; agents use the default source
    // unless told otherwise; disabled sources are registry placeholders.
    name: text("name").notNull().default("default"),
    is_default: boolean("is_default").notNull().default(false),
    enabled: boolean("enabled").notNull().default(true),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
    mcp_url: text("mcp_url"),
  },
  (t) => ({
    org_idx: index("data_source_org_idx").on(t.org_id),
    org_name_unique: uniqueIndex("data_source_org_name_unique").on(
      t.org_id,
      t.name,
    ),
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
    // CV3: per-persona cards. NULL = org-shared card.
    user_id: text("user_id"),
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

// CH2 — channel workspace → org mapping. First inbound contact
// auto-binds a workspace to the default org; multi-tenant remaps rows.
export const channel_workspace = pgTable(
  "channel_workspace",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    channel_plugin: text("channel_plugin").notNull(),
    workspace_id: text("workspace_id").notNull(),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    plugin_ws_unique: uniqueIndex("channel_workspace_plugin_ws_unique").on(
      t.channel_plugin,
      t.workspace_id,
    ),
    org_idx: index("channel_workspace_org_idx").on(t.org_id),
  }),
);

// CV3 — personas: one profile per (org, user). user_id = '' is the
// org-default persona (solo profile / unlinked channels). The compiled
// brief_md is what the agent reads as <operator-profile>; raw answers
// stay out of prompts.
export const operator_profile = pgTable(
  "operator_profile",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    user_id: text("user_id").notNull().default(""),
    display_name: text("display_name"),
    role_template: text("role_template").notNull().default(""),
    focus_areas: text("focus_areas").array().notNull().default(sql`'{}'::text[]`),
    answers: jsonb("answers").notNull().default(sql`'{}'::jsonb`),
    brief_md: text("brief_md").notNull().default(""),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_user_unique: uniqueIndex("operator_profile_org_user_unique").on(
      t.org_id,
      t.user_id,
    ),
  }),
);

// CV0 — config-vcs ref pointers: the DB knows each org layer's current
// commit; git holds the content. Team layer = (scope='team', user_id='').
export const config_ref = pgTable(
  "config_ref",
  {
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    scope: text("scope").notNull().default("team"),
    user_id: text("user_id").notNull().default(""),
    commit_sha: text("commit_sha").notNull(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_scope_user_unique: uniqueIndex("config_ref_org_scope_user_unique").on(
      t.org_id,
      t.scope,
      t.user_id,
    ),
  }),
);

// CH3 — channel→app_user mapping. One row per channel-native identity
// an org has seen; linking (SSO email match or admin-map) binds it to
// an app_user. Unlinked identities act as anonymous members.
export const channel_identity = pgTable(
  "channel_identity",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    channel_plugin: text("channel_plugin").notNull(),
    workspace_id: text("workspace_id").notNull().default(""),
    channel_user_id: text("channel_user_id").notNull(),
    app_user_id: text("app_user_id").references(() => app_user.id, {
      onDelete: "cascade",
    }),
    display_name: text("display_name"),
    email: text("email"),
    status: text("status").notNull().default("unverified"),
    first_seen_at: ts("first_seen_at").notNull().defaultNow(),
    verified_at: ts("verified_at"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    tuple_unique: uniqueIndex("channel_identity_tuple_unique").on(
      t.org_id,
      t.channel_plugin,
      t.workspace_id,
      t.channel_user_id,
    ),
    org_user_idx: index("channel_identity_org_user_idx").on(
      t.org_id,
      t.app_user_id,
    ),
  }),
);

// SEC5 — every authenticated gateway (broker) call a sandboxed agent
// makes, stamped with the dual identity (human principal + agent
// backend). SEC7 reads this for behavioral thresholds.
export const control_plane_audit = pgTable(
  "control_plane_audit",
  {
    id: bigserial("id", { mode: "number" }).primaryKey(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    run_id: uuid("run_id"),
    path: text("path").notNull(),
    actor_user_id: text("actor_user_id"),
    actor_role: text("actor_role"),
    backend: text("backend"),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    org_idx: index("control_plane_audit_org_idx").on(
      t.org_id,
      t.created_at.desc(),
    ),
    run_idx: index("control_plane_audit_run_idx").on(t.run_id),
  }),
);

// SEC10 — append-only per-org hash chain over the governance entities.
// Any retroactive edit/deletion breaks every later link.
export const audit_chain = pgTable(
  "audit_chain",
  {
    id: bigserial("id", { mode: "number" }).primaryKey(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    seq: integer("seq").notNull(),
    entity_kind: text("entity_kind").notNull(),
    entity_id: text("entity_id").notNull(),
    event: text("event").notNull(),
    payload: jsonb("payload").notNull().default(sql`'{}'::jsonb`),
    payload_hash: text("payload_hash").notNull(),
    prev_hash: text("prev_hash").notNull(),
    chain_hash: text("chain_hash").notNull(),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    org_seq_unique: uniqueIndex("audit_chain_org_seq_unique").on(t.org_id, t.seq),
    org_entity_idx: index("audit_chain_org_entity_idx").on(
      t.org_id,
      t.entity_kind,
      t.entity_id,
    ),
  }),
);

// OL4 — watchers: condition monitors over GraphJin queries that fire
// their linked workflow when the condition holds (polling v1).
export const watcher = pgTable(
  "watcher",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    workflow_id: uuid("workflow_id")
      .notNull()
      .references(() => workflow_definition.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    description: text("description").notNull().default(""),
    enabled: boolean("enabled").notNull().default(true),
    query: text("query").notNull(),
    value_path: text("value_path").notNull(),
    op: text("op").notNull(),
    threshold: jsonb("threshold"),
    cadence_seconds: integer("cadence_seconds").notNull().default(300),
    debounce_seconds: integer("debounce_seconds").notNull().default(3600),
    severity: text("severity").notNull().default("medium"),
    last_checked_at: ts("last_checked_at"),
    last_fired_at: ts("last_fired_at"),
    last_value: jsonb("last_value"),
    last_error: text("last_error"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_name_unique: uniqueIndex("watcher_org_name_unique").on(t.org_id, t.name),
    org_enabled_idx: index("watcher_org_enabled_idx").on(
      t.org_id,
      t.enabled,
      t.last_checked_at,
    ),
  }),
);

// SEC7 — behavioral threshold alerts raised by the worker sweep over
// the SEC5 audit stream and action/memory write rates.
export const behavior_alert = pgTable(
  "behavior_alert",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    kind: text("kind").notNull(),
    subject: text("subject").notNull().default(""),
    observed: integer("observed").notNull(),
    threshold: integer("threshold").notNull(),
    window_seconds: integer("window_seconds").notNull(),
    details: jsonb("details"),
    acknowledged_at: ts("acknowledged_at"),
    acknowledged_by: text("acknowledged_by"),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    org_idx: index("behavior_alert_org_idx").on(t.org_id, t.created_at.desc()),
    org_kind_subject_idx: index("behavior_alert_org_kind_subject_idx").on(
      t.org_id,
      t.kind,
      t.subject,
      t.created_at.desc(),
    ),
  }),
);

// CV4 — per-member fork baseline for 3-way memory pulls.
export const memory_fork = pgTable(
  "memory_fork",
  {
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    user_id: text("user_id")
      .notNull()
      .references(() => app_user.id, { onDelete: "cascade" }),
    baseline_sha: text("baseline_sha").notNull().default(""),
    baseline_at: ts("baseline_at").notNull().defaultNow(),
    frozen: boolean("frozen").notNull().default(false),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    pk: primaryKey({ columns: [t.org_id, t.user_id] }),
  }),
);

// CV4 — config-artifact audit trail + admin adopt inbox. Attribution
// lives here (DB-deletable), never in git commits (DATA_LIFECYCLE §3).
export const config_change = pgTable(
  "config_change",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    artifact_kind: text("artifact_kind").notNull(),
    artifact_ref: text("artifact_ref").notNull(),
    scope: text("scope").notNull().default("team"),
    user_id: text("user_id").notNull().default(""),
    actor_user_id: text("actor_user_id").references(() => app_user.id, {
      onDelete: "set null",
    }),
    commit_sha: text("commit_sha"),
    summary: text("summary").notNull().default(""),
    semantic_diff: jsonb("semantic_diff"),
    status: text("status").notNull().default("recorded"),
    decided_by: text("decided_by"),
    decided_at: ts("decided_at"),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    org_idx: index("config_change_org_idx").on(t.org_id, t.created_at.desc()),
    org_status_idx: index("config_change_org_status_idx").on(
      t.org_id,
      t.status,
      t.created_at.desc(),
    ),
  }),
);

// OL7 — a muted scope hides matching workflow_output cards from the
// Briefing tributaries until muted_until passes.
export const muted_scope = pgTable(
  "muted_scope",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    scope: text("scope").notNull(),
    muted_until: ts("muted_until").notNull(),
    muted_by_user_id: text("muted_by_user_id"),
    created_at: ts("created_at").notNull().defaultNow(),
  },
  (t) => ({
    org_scope_unique: uniqueIndex("muted_scope_org_scope_unique").on(
      t.org_id,
      t.scope,
    ),
    org_until_idx: index("muted_scope_org_until_idx").on(
      t.org_id,
      t.muted_until.desc(),
    ),
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
    // Origin channel ("web", "telegram", …). The web Ask UI lists only its own
    // ("web") threads so channels stay isolated. See docs/PER_CHANNEL_RENDERING.md.
    channel: text("channel").notNull().default("web"),
    // K1: who opened this thread (web session user). Null for channel /
    // service threads until CH3 links channel senders.
    created_by_user_id: text("created_by_user_id").references(
      () => app_user.id,
      { onDelete: "set null" },
    ),
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
    // K1: the acting principal, snapshotted at run start. Web runs carry
    // (app_user.id, role); channel runs (NULL, 'member') until CH3;
    // cron/workflow runs (NULL, 'service').
    actor_user_id: text("actor_user_id").references(() => app_user.id, {
      onDelete: "set null",
    }),
    actor_role: text("actor_role"),
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
    // CV2 overlay: NULL user_id = team layer; personal rows shadow
    // (overrides_origin_id) or hide (suppressed) the team row of that
    // origin for their owner. promoted_* = promote lineage.
    user_id: text("user_id").references(() => app_user.id, {
      onDelete: "cascade",
    }),
    origin_id: uuid("origin_id"),
    overrides_origin_id: uuid("overrides_origin_id"),
    suppressed: boolean("suppressed").notNull().default(false),
    promoted_from_id: uuid("promoted_from_id"),
    promoted_by: text("promoted_by"),
    promoted_at: ts("promoted_at"),
    // SEC6: per-org HMAC over identity-bearing fields (NULL = pre-SEC6
    // row); expires_at archives short-lived kinds via the nightly sweep.
    integrity_hmac: text("integrity_hmac"),
    expires_at: ts("expires_at"),
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
    // OL7 "pause for today": enabled=false with a re-enable timer the
    // cron sweep honors.
    paused_until: ts("paused_until"),
    // CV1 ownership: '' = org layer; a member's personal workflow carries
    // their user id. Unique is (org, owner, name). origin_id is the stable
    // identity across copy/promote; parent_id the fork/promote source.
    owner_user_id: text("owner_user_id").notNull().default(""),
    origin_id: uuid("origin_id"),
    parent_id: uuid("parent_id"),
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
    org_owner_name_unique: uniqueIndex(
      "workflow_definition_org_owner_name_unique",
    ).on(t.org_id, t.owner_user_id, t.name),
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
    // OL8 card-level dedupe: an identical finding within 24h bumps
    // seen_count on the original card instead of creating a new one.
    seen_count: integer("seen_count").notNull().default(1),
    last_seen_at: ts("last_seen_at").notNull().defaultNow(),
    dedupe_key: text("dedupe_key"),
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

// Persisted poll offset per (org, channel). Inbound polling keeps an in-memory
// cursor while running; persisting it means a restart resumes from the last
// acknowledged offset instead of re-polling the provider from scratch.
export const channel_poll_cursor = pgTable(
  "channel_poll_cursor",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    channel_plugin: text("channel_plugin").notNull(),
    cursor: text("cursor").notNull(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_plugin_unique: uniqueIndex("channel_poll_cursor_org_plugin_unique").on(
      t.org_id,
      t.channel_plugin,
    ),
  }),
);

// Per-update ledger for inbound dispatch. A poll restart (or webhook retry) can
// re-deliver an update; claiming a stable per-update key here makes dispatch
// exactly-once. `status` tracks the lifecycle: 'pending' (claimed, retrying),
// 'done' (dispatched — the dedup marker), 'dead' (gave up after MAX attempts —
// payload + last_error retained for inspection). 'done' rows are TTL-pruned;
// 'dead' rows persist as a queryable dead-letter queue.
export const inbound_dedup = pgTable(
  "inbound_dedup",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    channel_plugin: text("channel_plugin").notNull(),
    update_key: text("update_key").notNull(),
    status: text("status").notNull().default("done"),
    attempts: integer("attempts").notNull().default(0),
    last_error: text("last_error"),
    payload: jsonb("payload"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_plugin_key_unique: uniqueIndex("inbound_dedup_org_plugin_key_unique").on(
      t.org_id,
      t.channel_plugin,
      t.update_key,
    ),
    created_idx: index("inbound_dedup_created_idx").on(t.created_at),
    status_idx: index("inbound_dedup_status_idx").on(t.status),
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

// OL2 — observation-elevation: promote a consumer-side observation onto
// the Briefing as a first-class card (it may have no producing output,
// e.g. an external_event or source_change observation).
export const briefing_card = pgTable(
  "briefing_card",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    org_id: text("org_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    source_observation_id: uuid("source_observation_id")
      .notNull()
      .references(() => observation.id, { onDelete: "cascade" }),
    title: text("title"),
    body: text("body"),
    mood: text("mood"),
    status: text("status").notNull().default("active"),
    elevated_by: text("elevated_by").notNull().default("system"),
    elevated_by_user_id: text("elevated_by_user_id"),
    created_at: ts("created_at").notNull().defaultNow(),
    updated_at: ts("updated_at").notNull().defaultNow(),
  },
  (t) => ({
    org_observation_unique: uniqueIndex(
      "briefing_card_org_observation_unique",
    ).on(t.org_id, t.source_observation_id),
    org_status_idx: index("briefing_card_org_status_idx").on(
      t.org_id,
      t.status,
      t.created_at.desc(),
    ),
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
    // SEC5: dual identity snapshotted at creation — the human principal
    // (K1) and the agent backend that proposed the action.
    actor_user_id: text("actor_user_id"),
    actor_role: text("actor_role"),
    actor_backend: text("actor_backend"),
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
