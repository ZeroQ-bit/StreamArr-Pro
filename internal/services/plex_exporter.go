package services

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Zerr0-C00L/StreamArr/internal/database"
	"github.com/Zerr0-C00L/StreamArr/internal/models"
	"github.com/Zerr0-C00L/StreamArr/internal/settings"
)

type PlexExporter struct {
	movieStore      *database.MovieStore
	seriesStore     *database.SeriesStore
	streamCache     *database.StreamCacheStore
	settingsManager *settings.Manager
	rdClient        *RealDebridClient
	httpClient      *http.Client
	stopChan        chan struct{}
}

func NewPlexExporter(
	movieStore *database.MovieStore,
	seriesStore *database.SeriesStore,
	streamCache *database.StreamCacheStore,
	settingsManager *settings.Manager,
) *PlexExporter {
	return &PlexExporter{
		movieStore:      movieStore,
		seriesStore:     seriesStore,
		streamCache:     streamCache,
		settingsManager: settingsManager,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		stopChan: make(chan struct{}),
	}
}

func (p *PlexExporter) Start() {
	go func() {
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()

		for {
			select {
			case <-p.stopChan:
				return
			case <-timer.C:
			}

			cfg := p.settingsManager.Get()
			interval := time.Duration(maxInt(cfg.PlexExportIntervalMinutes, 1)) * time.Minute

			if cfg.PlexExportEnabled {
				GlobalScheduler.MarkRunning(ServicePlexExport)
				err := p.ExportPending(context.Background())
				GlobalScheduler.MarkComplete(ServicePlexExport, err, interval)
				if err != nil {
					log.Printf("[PLEX-EXPORT] Export run failed: %v", err)
				}
			}

			timer.Reset(interval)
		}
	}()
}

func (p *PlexExporter) Stop() {
	close(p.stopChan)
}

func (p *PlexExporter) ExportPending(ctx context.Context) error {
	if p.streamCache == nil || p.settingsManager == nil {
		return fmt.Errorf("plex exporter dependencies not initialized")
	}

	cfg := p.settingsManager.Get()
	if !cfg.PlexExportEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.PlexExportMode) == "" {
		cfg.PlexExportMode = "symlink"
	}
	if !strings.EqualFold(cfg.PlexExportMode, "symlink") {
		return fmt.Errorf("unsupported plex export mode %q", cfg.PlexExportMode)
	}

	apiKey := strings.TrimSpace(cfg.RealDebridAPIKey)
	if apiKey == "" {
		return fmt.Errorf("real-debrid api key is required for plex export")
	}
	p.rdClient = NewRealDebridClient(apiKey)

	pending, err := p.streamCache.GetPendingPlexExports(ctx, 100)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		GlobalScheduler.UpdateProgress(ServicePlexExport, 0, 0, "No pending Plex exports")
		return nil
	}

	GlobalScheduler.UpdateProgress(ServicePlexExport, 0, len(pending), "Exporting cached items to Plex")

	var firstErr error
	for i, cached := range pending {
		label := fmt.Sprintf("%s #%d", cached.MediaType, cached.MediaID)
		if cached.MediaType == "movie" {
			if movie, getErr := p.movieStore.Get(ctx, int64(cached.MovieID)); getErr == nil {
				label = movie.Title
			}
		} else if cached.MediaType == "series" {
			if series, getErr := p.seriesStore.Get(ctx, int64(cached.SeriesID)); getErr == nil {
				label = fmt.Sprintf("%s S%02dE%02d", series.Title, cached.Season, cached.Episode)
			}
		}

		GlobalScheduler.UpdateProgress(ServicePlexExport, i, len(pending), fmt.Sprintf("Exporting %s", label))

		exportPath, exportErr := p.exportSingle(ctx, cfg, cached)
		if exportErr != nil {
			if firstErr == nil {
				firstErr = exportErr
			}
			log.Printf("[PLEX-EXPORT] Failed to export %s: %v", label, exportErr)
			_ = p.streamCache.MarkPlexExportFailedByID(ctx, cached.ID, truncateString(exportErr.Error(), 500))
			continue
		}

		if err := p.streamCache.MarkPlexExportedByID(ctx, cached.ID, exportPath); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("[PLEX-EXPORT] Failed to mark export complete for %s: %v", label, err)
			continue
		}

		GlobalScheduler.UpdateProgress(ServicePlexExport, i+1, len(pending), fmt.Sprintf("Exported %s", label))
	}

	return firstErr
}

func (p *PlexExporter) exportSingle(ctx context.Context, cfg *settings.Settings, cached *models.CachedStream) (string, error) {
	if strings.TrimSpace(cached.RDTorrentID) == "" {
		return "", fmt.Errorf("missing rd torrent id")
	}

	sourcePath, err := p.resolveSourcePath(ctx, cfg, cached)
	if err != nil {
		return "", err
	}

	destPath, sectionID, err := p.buildDestinationPath(ctx, cfg, cached, sourcePath)
	if err != nil {
		return "", err
	}

	if err := ensureDir(filepath.Dir(destPath)); err != nil {
		return "", fmt.Errorf("create destination directory: %w", err)
	}

	if err := p.ensureSymlink(sourcePath, destPath); err != nil {
		return "", err
	}

	if cfg.PlexRefreshEnabled {
		if refreshErr := p.refreshPlexPath(ctx, cfg, sectionID, filepath.Dir(destPath)); refreshErr != nil {
			log.Printf("[PLEX-EXPORT] Plex refresh warning for %s: %v", destPath, refreshErr)
		}
	}

	return destPath, nil
}

func (p *PlexExporter) resolveSourcePath(ctx context.Context, cfg *settings.Settings, cached *models.CachedStream) (string, error) {
	rdMount := filepath.Clean(strings.TrimSpace(cfg.PlexExportRDMountPath))
	if rdMount == "." || rdMount == "" {
		return "", fmt.Errorf("plex export rd mount path is required")
	}

	info, err := p.rdClient.GetTorrentInfo(ctx, cached.RDTorrentID)
	if err != nil {
		return "", fmt.Errorf("load rd torrent info: %w", err)
	}

	if stat, statErr := os.Stat(rdMount); statErr != nil || !stat.IsDir() {
		if statErr != nil {
			return "", fmt.Errorf("rd mount path unavailable: %w", statErr)
		}
		return "", fmt.Errorf("rd mount path is not a directory")
	}

	var candidates []string
	if name := strings.TrimSpace(info.Filename); name != "" {
		candidates = append(candidates,
			filepath.Join(rdMount, name),
			filepath.Join(rdMount, filepath.Base(name)),
		)
	}

	for _, candidate := range candidates {
		resolved, ok := resolveCandidatePath(candidate)
		if ok {
			return resolved, nil
		}
	}

	match, err := findBestMountedMatch(rdMount, info.Filename, cached)
	if err != nil {
		return "", err
	}
	if match == "" {
		return "", fmt.Errorf("could not locate mounted RD file for torrent %s", cached.RDTorrentID)
	}
	return match, nil
}

func (p *PlexExporter) buildDestinationPath(ctx context.Context, cfg *settings.Settings, cached *models.CachedStream, sourcePath string) (string, string, error) {
	ext := filepath.Ext(sourcePath)
	if ext == "" {
		ext = ".mkv"
	}

	switch cached.MediaType {
	case "movie":
		root := filepath.Clean(strings.TrimSpace(cfg.PlexExportMoviesPath))
		if root == "." || root == "" {
			return "", "", fmt.Errorf("plex movies path is required")
		}
		movie, err := p.movieStore.Get(ctx, int64(cached.MovieID))
		if err != nil {
			return "", "", fmt.Errorf("load movie metadata: %w", err)
		}
		title := sanitizePathComponent(movie.Title)
		year := movie.Year
		if year == 0 && movie.ReleaseDate != nil {
			year = movie.ReleaseDate.Year()
		}
		folderBase := title
		if year > 0 {
			folderBase = fmt.Sprintf("%s (%d)", title, year)
		}
		folderName := folderBase
		if movie.TMDBID > 0 {
			folderName = fmt.Sprintf("%s {tmdb-%d}", folderBase, movie.TMDBID)
		}
		fileName := folderBase
		return filepath.Join(root, folderName, fileName+ext), strings.TrimSpace(cfg.PlexMoviesSectionID), nil

	case "series":
		root := filepath.Clean(strings.TrimSpace(cfg.PlexExportShowsPath))
		if root == "." || root == "" {
			return "", "", fmt.Errorf("plex shows path is required")
		}
		series, err := p.seriesStore.Get(ctx, int64(cached.SeriesID))
		if err != nil {
			return "", "", fmt.Errorf("load series metadata: %w", err)
		}
		title := sanitizePathComponent(series.Title)
		year := series.Year
		if year == 0 && series.FirstAirDate != nil {
			year = series.FirstAirDate.Year()
		}
		showBase := title
		if year > 0 {
			showBase = fmt.Sprintf("%s (%d)", title, year)
		}
		showFolder := showBase
		if tvdbID := p.resolveSeriesTVDBID(ctx, cfg, series); tvdbID > 0 {
			showFolder = fmt.Sprintf("%s {tvdb-%d}", showBase, tvdbID)
		}
		seasonFolder := fmt.Sprintf("Season %02d", cached.Season)
		fileName := fmt.Sprintf("%s - s%02de%02d%s", title, cached.Season, cached.Episode, ext)
		return filepath.Join(root, showFolder, seasonFolder, fileName), strings.TrimSpace(cfg.PlexShowsSectionID), nil

	default:
		return "", "", fmt.Errorf("unsupported media type %q", cached.MediaType)
	}
}

func (p *PlexExporter) resolveSeriesTVDBID(ctx context.Context, cfg *settings.Settings, series *models.Series) int {
	if series == nil {
		return 0
	}
	if series.Metadata != nil {
		if tvdbID := metadataInt(series.Metadata["tvdb_id"]); tvdbID > 0 {
			return tvdbID
		}
	}
	if series.TMDBID <= 0 {
		return 0
	}
	apiKey := strings.TrimSpace(cfg.TMDBAPIKey)
	if apiKey == "" {
		return 0
	}
	tmdbClient := NewTMDBClient(apiKey)
	externalIDs, err := tmdbClient.GetSeriesExternalIDs(ctx, series.TMDBID)
	if err != nil {
		log.Printf("[PLEX-EXPORT] Could not resolve TVDB ID for %s: %v", series.Title, err)
		return 0
	}
	return externalIDs.TVDBID
}

func (p *PlexExporter) ensureSymlink(sourcePath, destPath string) error {
	if existingInfo, err := os.Lstat(destPath); err == nil {
		if existingInfo.Mode()&os.ModeSymlink != 0 {
			currentTarget, readErr := os.Readlink(destPath)
			if readErr == nil {
				absCurrent, _ := filepath.Abs(currentTarget)
				absSource, _ := filepath.Abs(sourcePath)
				if absCurrent == absSource || currentTarget == sourcePath {
					return nil
				}
			}
			if removeErr := os.Remove(destPath); removeErr != nil {
				return fmt.Errorf("replace existing symlink: %w", removeErr)
			}
		} else {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination path: %w", err)
	}

	if err := os.Symlink(sourcePath, destPath); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}
	return nil
}

func (p *PlexExporter) refreshPlexPath(ctx context.Context, cfg *settings.Settings, sectionID, targetPath string) error {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.PlexURL), "/")
	token := strings.TrimSpace(cfg.PlexToken)
	if baseURL == "" || token == "" || sectionID == "" {
		return nil
	}

	refreshURL, err := url.Parse(baseURL + "/library/sections/" + sectionID + "/refresh")
	if err != nil {
		return err
	}
	query := refreshURL.Query()
	query.Set("path", targetPath)
	query.Set("X-Plex-Token", token)
	refreshURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, refreshURL.String(), nil)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("plex refresh returned %s", resp.Status)
	}
	return nil
}

func resolveCandidatePath(path string) (string, bool) {
	stat, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if stat.IsDir() {
		file, err := findLargestVideoFile(path)
		if err != nil || file == "" {
			return "", false
		}
		return file, true
	}
	return path, true
}

func findBestMountedMatch(root, torrentName string, cached *models.CachedStream) (string, error) {
	torrentBase := strings.ToLower(filepath.Base(strings.TrimSpace(torrentName)))
	episodeToken := ""
	if cached.MediaType == "series" {
		episodeToken = fmt.Sprintf("s%02de%02d", cached.Season, cached.Episode)
	}

	type candidate struct {
		path  string
		score int
		size  int64
	}

	var matches []candidate
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := strings.ToLower(d.Name())
		score := 0

		if torrentBase != "" && name == torrentBase {
			score += 100
		}
		if torrentBase != "" && strings.Contains(name, torrentBase) {
			score += 50
		}
		if cached.StreamHash != "" && strings.Contains(name, strings.ToLower(cached.StreamHash)) {
			score += 25
		}
		if episodeToken != "" && strings.Contains(name, episodeToken) {
			score += 20
		}

		if score == 0 {
			return nil
		}

		if d.IsDir() {
			videoPath, fileErr := findLargestVideoFile(path)
			if fileErr == nil && videoPath != "" {
				if info, statErr := os.Stat(videoPath); statErr == nil {
					matches = append(matches, candidate{path: videoPath, score: score, size: info.Size()})
				}
			}
			return nil
		}

		if !isVideoFile(path) {
			return nil
		}

		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		matches = append(matches, candidate{path: path, score: score, size: info.Size()})
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "", nil
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].size > matches[j].size
		}
		return matches[i].score > matches[j].score
	})

	return matches[0].path, nil
}

func findLargestVideoFile(root string) (string, error) {
	type candidate struct {
		path string
		size int64
	}

	var matches []candidate
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isVideoFile(path) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		matches = append(matches, candidate{path: path, size: info.Size()})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].size > matches[j].size
	})
	return matches[0].path, nil
}

func isVideoFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".ts":
		return true
	default:
		return false
	}
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func sanitizePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Unknown"
	}
	replacer := strings.NewReplacer(
		"/", " ",
		"\\", " ",
		":", " -",
		"*", "",
		"?", "",
		"\"", "'",
		"<", "",
		">", "",
		"|", "",
	)
	value = replacer.Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	return strings.TrimSpace(value)
}

func truncateString(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}

func metadataInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func parseSectionID(value string) int {
	id, _ := strconv.Atoi(strings.TrimSpace(value))
	return id
}
