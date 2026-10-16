-- Public line label (IRIS `l` attribute, e.g. "12" for an S12, "577" for
-- ICE 577). tl.n (train_number) is NOT the public line number for S-Bahn:
-- it usually holds the 5-digit operational trip number, which broke
-- line-level statistics (an S12 appeared as "S 38491"). line_label is
-- captured per event going forward; historical rows stay NULL (the l
-- attribute was never stored before this migration).
ALTER TABLE stop_events ADD COLUMN line_label TEXT;