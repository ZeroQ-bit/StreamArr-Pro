DROP INDEX IF EXISTS idx_media_streams_plex_export_pending;

ALTER TABLE media_streams
DROP COLUMN IF EXISTS plex_export_error;

ALTER TABLE media_streams
DROP COLUMN IF EXISTS plex_exported_at;

ALTER TABLE media_streams
DROP COLUMN IF EXISTS plex_export_path;

ALTER TABLE media_streams
DROP COLUMN IF EXISTS plex_exported;
