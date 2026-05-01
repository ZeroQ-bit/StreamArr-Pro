ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS plex_exported BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS plex_export_path TEXT NOT NULL DEFAULT '';

ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS plex_exported_at TIMESTAMPTZ;

ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS plex_export_error TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_media_streams_plex_export_pending
ON media_streams (plex_exported, rd_library_added, media_type, media_id)
WHERE rd_library_added = TRUE AND plex_exported = FALSE;
