ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS rd_library_added BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS rd_library_added_at TIMESTAMPTZ;

ALTER TABLE media_streams
ADD COLUMN IF NOT EXISTS rd_torrent_id TEXT;

CREATE INDEX IF NOT EXISTS idx_media_streams_rd_library_pending
ON media_streams (rd_library_added, media_type, media_id)
WHERE rd_library_added = FALSE;
