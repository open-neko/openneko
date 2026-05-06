import { relations, sql } from "drizzle-orm";
import {
  bigserial,
  boolean,
  date,
  index,
  integer,
  jsonb,
  numeric,
  pgTable,
  smallint,
  text,
  timestamp,
  uniqueIndex,
  uuid,
} from "drizzle-orm/pg-core";

const ts = (name: string) =>
  timestamp(name, { withTimezone: true, mode: "date" });

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
