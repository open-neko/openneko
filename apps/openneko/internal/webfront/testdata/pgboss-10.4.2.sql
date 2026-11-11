-- pg-boss 10.4.2 schema 24: plans.create("pgboss", 24)

    BEGIN;
    SET LOCAL lock_timeout = '30s';
    SET LOCAL idle_in_transaction_session_timeout = '30s';
    SELECT pg_advisory_xact_lock(
      ('x' || encode(sha224((current_database() || '.pgboss.pgboss')::bytea), 'hex'))::bit(64)::bigint
  );
    
    CREATE SCHEMA IF NOT EXISTS pgboss
  ;

    CREATE TYPE pgboss.job_state AS ENUM (
      'created',
      'retry',
      'active',
      'completed',
      'cancelled',
      'failed'
    )
  ;

    CREATE TABLE pgboss.version (
      version int primary key,
      maintained_on timestamp with time zone,
      cron_on timestamp with time zone,
      monitored_on timestamp with time zone
    )
  ;

    CREATE TABLE pgboss.queue (
      name text,
      policy text,
      retry_limit int,
      retry_delay int,
      retry_backoff bool,
      expire_seconds int,
      retention_minutes int,
      dead_letter text REFERENCES pgboss.queue (name),
      partition_name text,
      created_on timestamp with time zone not null default now(),
      updated_on timestamp with time zone not null default now(),
      PRIMARY KEY (name)
    )
  ;

    CREATE TABLE pgboss.schedule (
      name text REFERENCES pgboss.queue ON DELETE CASCADE,
      cron text not null,
      timezone text,
      data jsonb,
      options jsonb,
      created_on timestamp with time zone not null default now(),
      updated_on timestamp with time zone not null default now(),
      PRIMARY KEY (name)
    )
  ;

    CREATE TABLE pgboss.subscription (
      event text not null,
      name text not null REFERENCES pgboss.queue ON DELETE CASCADE,
      created_on timestamp with time zone not null default now(),
      updated_on timestamp with time zone not null default now(),
      PRIMARY KEY(event, name)
    )
  ;

    CREATE TABLE pgboss.job (
      id uuid not null default gen_random_uuid(),
      name text not null,
      priority integer not null default(0),
      data jsonb,
      state pgboss.job_state not null default('created'),
      retry_limit integer not null default(2),
      retry_count integer not null default(0),
      retry_delay integer not null default(0),
      retry_backoff boolean not null default false,
      start_after timestamp with time zone not null default now(),
      started_on timestamp with time zone,
      singleton_key text,
      singleton_on timestamp without time zone,
      expire_in interval not null default interval '15 minutes',
      created_on timestamp with time zone not null default now(),
      completed_on timestamp with time zone,
      keep_until timestamp with time zone NOT NULL default now() + interval '14 days',
      output jsonb,
      dead_letter text,
      policy text
    ) PARTITION BY LIST (name)
  ;
ALTER TABLE pgboss.job ADD PRIMARY KEY (name, id);
CREATE TABLE pgboss.archive (LIKE pgboss.job);
ALTER TABLE pgboss.archive ADD PRIMARY KEY (name, id);
ALTER TABLE pgboss.archive ADD archived_on timestamptz NOT NULL DEFAULT now();
CREATE INDEX archive_i1 ON pgboss.archive(archived_on);

    CREATE FUNCTION pgboss.create_queue(queue_name text, options json)
    RETURNS VOID AS
    $$
    DECLARE
      table_name varchar := 'j' || encode(sha224(queue_name::bytea), 'hex');
      queue_created_on timestamptz;
    BEGIN

      WITH q as (
      INSERT INTO pgboss.queue (
        name,
        policy,
        retry_limit,
        retry_delay,
        retry_backoff,
        expire_seconds,
        retention_minutes,
        dead_letter,
        partition_name
      )
      VALUES (
        queue_name,
        options->>'policy',
        (options->>'retryLimit')::int,
        (options->>'retryDelay')::int,
        (options->>'retryBackoff')::bool,
        (options->>'expireInSeconds')::int,
        (options->>'retentionMinutes')::int,
        options->>'deadLetter',
        table_name
      )
      ON CONFLICT DO NOTHING
      RETURNING created_on
      )
      SELECT created_on into queue_created_on from q;

      IF queue_created_on IS NULL THEN
        RETURN;
      END IF;

      EXECUTE format('CREATE TABLE pgboss.%I (LIKE pgboss.job INCLUDING DEFAULTS)', table_name);

      EXECUTE format('ALTER TABLE pgboss.%1$I ADD PRIMARY KEY (name, id)', table_name);
      EXECUTE format('ALTER TABLE pgboss.%1$I ADD CONSTRAINT q_fkey FOREIGN KEY (name) REFERENCES pgboss.queue (name) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED', table_name);
      EXECUTE format('ALTER TABLE pgboss.%1$I ADD CONSTRAINT dlq_fkey FOREIGN KEY (dead_letter) REFERENCES pgboss.queue (name) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED', table_name);
      EXECUTE format('CREATE UNIQUE INDEX %1$s_i1 ON pgboss.%1$I (name, COALESCE(singleton_key, '''')) WHERE state = ''created'' AND policy = ''short''', table_name);
      EXECUTE format('CREATE UNIQUE INDEX %1$s_i2 ON pgboss.%1$I (name, COALESCE(singleton_key, '''')) WHERE state = ''active'' AND policy = ''singleton''', table_name);
      EXECUTE format('CREATE UNIQUE INDEX %1$s_i3 ON pgboss.%1$I (name, state, COALESCE(singleton_key, '''')) WHERE state <= ''active'' AND policy = ''stately''', table_name);
      EXECUTE format('CREATE UNIQUE INDEX %1$s_i4 ON pgboss.%1$I (name, singleton_on, COALESCE(singleton_key, '''')) WHERE state <> ''cancelled'' AND singleton_on IS NOT NULL', table_name);
      EXECUTE format('CREATE INDEX %1$s_i5 ON pgboss.%1$I (name, start_after) INCLUDE (priority, created_on, id) WHERE state < ''active''', table_name);

      EXECUTE format('ALTER TABLE pgboss.%I ADD CONSTRAINT cjc CHECK (name=%L)', table_name, queue_name);
      EXECUTE format('ALTER TABLE pgboss.job ATTACH PARTITION pgboss.%I FOR VALUES IN (%L)', table_name, queue_name);
    END;
    $$
    LANGUAGE plpgsql;
  ;

    CREATE FUNCTION pgboss.delete_queue(queue_name text)
    RETURNS VOID AS
    $$
    DECLARE
      table_name varchar;
    BEGIN
      WITH deleted as (
        DELETE FROM pgboss.queue
        WHERE name = queue_name
        RETURNING partition_name
      )
      SELECT partition_name from deleted INTO table_name;

      EXECUTE format('DROP TABLE IF EXISTS pgboss.%I', table_name);
    END;
    $$
    LANGUAGE plpgsql;
  ;
INSERT INTO pgboss.version(version) VALUES ('24');
    COMMIT;
  
