-- 0002_no_iris.down.sql
DROP INDEX IF EXISTS stations_no_iris_idx;
ALTER TABLE stations DROP COLUMN IF EXISTS no_iris;