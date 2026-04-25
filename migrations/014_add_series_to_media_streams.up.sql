-- Add series and episode support to media_streams table.
-- The previous schema stored a generic media_id/media_type pair, while the
-- current code expects explicit movie_id/series_id columns.

ALTER TABLE media_streams ADD COLUMN IF NOT EXISTS movie_id BIGINT REFERENCES library_movies(id) ON DELETE CASCADE;
ALTER TABLE media_streams ADD COLUMN IF NOT EXISTS series_id BIGINT REFERENCES library_series(id) ON DELETE CASCADE;
ALTER TABLE media_streams ADD COLUMN IF NOT EXISTS season INTEGER;
ALTER TABLE media_streams ADD COLUMN IF NOT EXISTS episode INTEGER;

-- Backfill movie rows from the legacy schema.
UPDATE media_streams
SET movie_id = media_id
WHERE movie_id IS NULL
  AND media_type = 'movie';

-- Legacy series rows do not include season/episode identifiers, so drop them
-- and let the cache repopulate with the new schema.
DELETE FROM media_streams
WHERE media_type = 'series'
  AND series_id IS NULL;

DELETE FROM media_streams
WHERE movie_id IS NULL
  AND series_id IS NULL;

ALTER TABLE media_streams DROP CONSTRAINT IF EXISTS unique_movie_stream;
ALTER TABLE media_streams DROP CONSTRAINT IF EXISTS media_streams_media_type_media_id_key;
ALTER TABLE media_streams DROP CONSTRAINT IF EXISTS check_media_type;

DROP INDEX IF EXISTS unique_movie_stream_idx;
DROP INDEX IF EXISTS unique_episode_stream_idx;
DROP INDEX IF EXISTS idx_streams_series_id;
DROP INDEX IF EXISTS idx_streams_series_season;

CREATE UNIQUE INDEX IF NOT EXISTS unique_movie_stream_idx
ON media_streams (movie_id)
WHERE movie_id IS NOT NULL AND series_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS unique_episode_stream_idx
ON media_streams (series_id, season, episode)
WHERE series_id IS NOT NULL;

ALTER TABLE media_streams ADD CONSTRAINT check_media_type CHECK (
    (movie_id IS NOT NULL AND series_id IS NULL) OR
    (movie_id IS NULL AND series_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_streams_series_id
ON media_streams (series_id)
WHERE series_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_streams_series_season
ON media_streams (series_id, season)
WHERE series_id IS NOT NULL;

INSERT INTO schema_migrations (version, applied_at)
VALUES (14, NOW())
ON CONFLICT (version) DO NOTHING;
