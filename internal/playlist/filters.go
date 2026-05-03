package playlist

import (
	"github.com/Zerr0-C00L/StreamArr/internal/config"
	"github.com/Zerr0-C00L/StreamArr/internal/services"
)

func playlistContentFilters(cfg *config.Config) services.ContentFilterOptions {
	if cfg == nil {
		return services.ContentFilterOptions{}
	}
	return services.ContentFilterOptions{
		MinYear:             cfg.MinYear,
		MinRuntime:          cfg.MinRuntime,
		IncludeAdultVOD:     cfg.IncludeAdultVOD,
		OnlyReleasedContent: cfg.OnlyReleasedContent,
		BlockBollywood:      cfg.BlockBollywood,
	}
}

func playlistMovieDiscoveryFilters(cfg *config.Config) services.DiscoverMovieFilters {
	if cfg == nil {
		return services.DiscoverMovieFilters{}
	}
	return services.DiscoverMovieFilters{
		MinYear:             cfg.MinYear,
		MinRuntime:          cfg.MinRuntime,
		IncludeAdultVOD:     cfg.IncludeAdultVOD,
		OnlyReleasedContent: cfg.OnlyReleasedContent,
	}
}
