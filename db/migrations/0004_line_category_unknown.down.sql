-- 0004_line_category_unknown.down.sql
-- Reverts 0004_line_category_unknown.up.sql.
-- NOTE: any rows with line_category = 'unknown' must be removed or
-- re-categorised before this down migration runs, or the restored CHECK
-- constraint will reject them.

ALTER TABLE stop_events DROP CONSTRAINT stop_events_line_category_check;
ALTER TABLE stop_events ADD CONSTRAINT stop_events_line_category_check CHECK (line_category IN
  ('fern','regio','s_bahn','u_bahn','strassenbahn','bus','ersatz'));

ALTER TABLE lines DROP CONSTRAINT lines_line_category_check;
ALTER TABLE lines ADD CONSTRAINT lines_line_category_check CHECK (line_category IN
  ('fern','regio','s_bahn','u_bahn','strassenbahn','bus','ersatz'));