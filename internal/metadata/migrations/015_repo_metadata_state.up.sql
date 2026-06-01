-- Migration 015_repo_metadata_state:
--
-- Adds metadata_state + last_regen_error columns to repos so the regen
-- coalescer (internal/protocol/regen) can publish dirty/regenerating
-- transitions inside the writer tx that mutated packages. Values are
-- clean|dirty|regenerating enforced at the DDL layer.

ALTER TABLE repos ADD COLUMN metadata_state TEXT NOT NULL DEFAULT 'clean'
    CHECK (metadata_state IN ('clean','dirty','regenerating'));
ALTER TABLE repos ADD COLUMN last_regen_error TEXT NOT NULL DEFAULT '';
