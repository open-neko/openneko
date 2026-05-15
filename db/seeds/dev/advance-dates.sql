-- advance-dates.sql
-- Idempotently shift operational AdventureWorks date columns forward so the
-- most-recent row in each logical group lands on or after CURRENT_DATE.
-- Runs at most once per 12 hours per database.
--
-- Why: AW2014 ships frozen historical data. The L1 trial sim only ticks
-- sales.salesorderheader (orderdate, shipdate). Production/manufacturing/
-- purchasing tables stay frozen and metric agents end up computing TTMs
-- against year-old data.
--
-- Sales is intentionally NOT shifted here — the sim is its source of truth
-- and shifting historical rows would clash with sim-generated current rows.
--
-- HR, product lifecycle, BOM, cost/listprice history, currency rate, and
-- product reviews are intentionally left alone — they are real historical
-- reference data, not operational throughput.

\set ON_ERROR_STOP on

DO $$
DECLARE
  last_at  timestamptz;
  shift_d  int;
  today    date := CURRENT_DATE;
BEGIN
  SELECT advance_dates_at INTO last_at FROM trial_sim.state;
  IF last_at IS NOT NULL AND now() - last_at < interval '12 hours' THEN
    RAISE NOTICE '[advance-dates] skip — last run %', last_at;
    RETURN;
  END IF;

  -- Group 1: workorder + workorderrouting (one shift, applied to both).
  SELECT GREATEST(0, today - MAX(enddate)::date) INTO shift_d
  FROM production.workorder WHERE enddate IS NOT NULL;
  IF shift_d > 0 THEN
    RAISE NOTICE '[advance-dates] production.workorder + workorderrouting +% days', shift_d;
    UPDATE production.workorder
       SET startdate = startdate + (shift_d || ' days')::interval,
           enddate   = enddate   + (shift_d || ' days')::interval,
           duedate   = duedate   + (shift_d || ' days')::interval;
    UPDATE production.workorderrouting
       SET actualstartdate    = actualstartdate    + (shift_d || ' days')::interval,
           actualenddate      = actualenddate      + (shift_d || ' days')::interval,
           scheduledstartdate = scheduledstartdate + (shift_d || ' days')::interval,
           scheduledenddate   = scheduledenddate   + (shift_d || ' days')::interval;
  END IF;

  -- Group 2: production.transactionhistory.
  SELECT GREATEST(0, today - MAX(transactiondate)::date) INTO shift_d
  FROM production.transactionhistory;
  IF shift_d > 0 THEN
    RAISE NOTICE '[advance-dates] production.transactionhistory +% days', shift_d;
    UPDATE production.transactionhistory
       SET transactiondate = transactiondate + (shift_d || ' days')::interval;
  END IF;

  -- Group 3: purchasing (header + detail + productvendor.lastreceiptdate, one shift).
  SELECT GREATEST(0, today - MAX(orderdate)::date) INTO shift_d
  FROM purchasing.purchaseorderheader;
  IF shift_d > 0 THEN
    RAISE NOTICE '[advance-dates] purchasing.purchaseorderheader/detail + productvendor +% days', shift_d;
    UPDATE purchasing.purchaseorderheader
       SET orderdate = orderdate + (shift_d || ' days')::interval,
           shipdate  = shipdate  + (shift_d || ' days')::interval;
    UPDATE purchasing.purchaseorderdetail
       SET duedate = duedate + (shift_d || ' days')::interval;
    UPDATE purchasing.productvendor
       SET lastreceiptdate = lastreceiptdate + (shift_d || ' days')::interval
     WHERE lastreceiptdate IS NOT NULL;
  END IF;

  UPDATE trial_sim.state SET advance_dates_at = now();
END $$;
