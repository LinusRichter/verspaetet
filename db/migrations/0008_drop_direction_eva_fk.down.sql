-- 0008_drop_direction_eva_fk.down.sql
-- Re-add the FK constraint (may fail if orphaned direction_eva values exist).
ALTER TABLE stop_events
  ADD CONSTRAINT stop_events_direction_eva_fkey
  FOREIGN KEY (direction_eva) REFERENCES stations(eva);