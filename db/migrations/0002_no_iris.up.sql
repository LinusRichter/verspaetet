-- 0002_no_iris.up.sql
-- Stations flagged no_iris=true are skipped by the scheduler: their EVA has
-- no IRIS Betriebsstelle (StaDa universe includes bus stops / closed halts
-- that IRIS answers with HTTP 400). The worker sets this flag automatically
-- when IRIS answers 400 for a station.

ALTER TABLE stations ADD COLUMN no_iris BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX stations_no_iris_idx ON stations (no_iris) WHERE no_iris;