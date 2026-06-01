-- modernc sqlite ships 3.51.x which supports ALTER TABLE DROP COLUMN (3.35+).
ALTER TABLE repos DROP COLUMN last_regen_error;
ALTER TABLE repos DROP COLUMN metadata_state;
