-- 0001_init.down.sql — drops views then tables in reverse dependency order.

DROP VIEW IF EXISTS platform_changes;
DROP VIEW IF EXISTS delays;

DROP TABLE IF EXISTS stop_events;
DROP TABLE IF EXISTS scrape_runs;
DROP TABLE IF EXISTS lines;
DROP TABLE IF EXISTS stations;