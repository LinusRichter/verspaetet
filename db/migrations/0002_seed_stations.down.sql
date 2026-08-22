-- 0002_seed_stations.down.sql — removes all 23 seed stations.
-- The eva list must match the up migration exactly.

DELETE FROM stations WHERE eva IN (
  '8000105','8011160','8002549','8003352','8000207','8000085','8000280',
  '8000240','8000156','8000068','8000080','8004128','8010085','8010224',
  '8010259','8010306','8010334','8010400','8011102','8000309','8000370',
  '8000096','8010327'
);