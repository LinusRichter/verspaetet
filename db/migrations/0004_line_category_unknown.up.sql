-- 0004_line_category_unknown.up.sql
-- Add an "unknown" line_category so unclassifiable line labels (foreign trains
-- like Thalys/Eurostar, unknown prefixes) can be persisted instead of being
-- silently dropped by PersistStopEvent. The parser now maps such labels to
-- "unknown" (see activities/process.go categoryByPrefix default case).
-- A future improvement can refine the prefix mapping.

ALTER TABLE lines DROP CONSTRAINT lines_line_category_check;
ALTER TABLE lines ADD CONSTRAINT lines_line_category_check CHECK (line_category IN
  ('fern','regio','s_bahn','u_bahn','strassenbahn','bus','ersatz','unknown'));

ALTER TABLE stop_events DROP CONSTRAINT stop_events_line_category_check;
ALTER TABLE stop_events ADD CONSTRAINT stop_events_line_category_check CHECK (line_category IN
  ('fern','regio','s_bahn','u_bahn','strassenbahn','bus','ersatz','unknown'));