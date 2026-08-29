-- 0010_fix_reason_code.down.sql
-- Not reversible: the reason_code values were corrupted by the 0007 bug;
-- there is no valid state to restore to.
SELECT 1;