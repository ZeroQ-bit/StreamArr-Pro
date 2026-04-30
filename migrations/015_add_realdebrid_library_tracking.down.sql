DROP INDEX IF EXISTS idx_media_streams_rd_library_pending;

ALTER TABLE media_streams DROP COLUMN IF EXISTS rd_torrent_id;
ALTER TABLE media_streams DROP COLUMN IF EXISTS rd_library_added_at;
ALTER TABLE media_streams DROP COLUMN IF EXISTS rd_library_added;
