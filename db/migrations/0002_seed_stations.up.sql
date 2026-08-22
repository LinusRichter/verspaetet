-- 0002_seed_stations.up.sql
-- Seed data from docs/data/seed-stations.md (status: locked, verified 2026-07-17).
-- 23 Fernverkehrsknoten. category 1 = Fernverkehrsknoten.

INSERT INTO stations (eva, slug, name, category) VALUES
  ('8000105', 'frankfurt-main-hbf',              'Frankfurt (Main) Hauptbahnhof', 1),
  ('8011160', 'berlin-hauptbahnhof',             'Berlin Hauptbahnhof',           1),
  ('8002549', 'hamburg-hbf',                     'Hamburg Hauptbahnhof',          1),
  ('8003352', 'muenchen-hbf',                    'München Hauptbahnhof',          1),
  ('8000207', 'koeln-hbf',                       'Köln Hauptbahnhof',             1),
  ('8000085', 'nuernberg-hbf',                   'Nürnberg Hauptbahnhof',         1),
  ('8000280', 'stuttgart-hbf',                   'Stuttgart Hauptbahnhof',        1),
  ('8000240', 'mainz-hbf',                       'Mainz Hauptbahnhof',            1),
  ('8000156', 'hannover-hbf',                    'Hannover Hauptbahnhof',         1),
  ('8000068', 'duesseldorf-hbf',                 'Düsseldorf Hauptbahnhof',       1),
  ('8000080', 'dortmund-hbf',                    'Dortmund Hauptbahnhof',         1),
  ('8004128', 'leipzig-hbf',                     'Leipzig Hauptbahnhof',          1),
  ('8010085', 'dresden-hbf',                     'Dresden Hauptbahnhof',          1),
  ('8010224', 'erfurt-hbf',                      'Erfurt Hauptbahnhof',           1),
  ('8010259', 'bremen-hbf',                       'Bremen Hauptbahnhof',           1),
  ('8010306', 'karlsruhe-hbf',                   'Karlsruhe Hauptbahnhof',        1),
  ('8010334', 'mannheim-hbf',                    'Mannheim Hauptbahnhof',         1),
  ('8010400', 'freiburg-breisgau-hbf',           'Freiburg (Breisgau) Hbf',       1),
  ('8011102', 'berlin-ostbahnhof',               'Berlin Ostbahnhof',             1),
  ('8000309', 'wuerzburg-hbf',                   'Würzburg Hauptbahnhof',         1),
  ('8000370', 'augsburg-hbf',                    'Augsburg Hauptbahnhof',         1),
  ('8000096', 'kassel-wilhelmshoehe',            'Kassel-Wilhelmshöhe',           1),
  ('8010327', 'frankfurt-am-main-flughafen-fernbahnhof', 'Frankfurt am Main Flughafen Fernbahnhof', 1)
ON CONFLICT (eva) DO NOTHING;