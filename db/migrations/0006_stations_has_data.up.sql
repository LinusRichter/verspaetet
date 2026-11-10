-- stations.has_data: the station delivered events at least once. Replaces the
-- EXISTS(scrape_runs) lookup so the fetch log can be pruned after export.
ALTER TABLE stations ADD COLUMN has_data BOOLEAN NOT NULL DEFAULT false;
UPDATE stations s SET has_data = true
WHERE EXISTS (SELECT 1 FROM scrape_runs r WHERE r.station_eva = s.eva);
