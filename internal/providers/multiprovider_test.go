package providers

import "testing"

func TestBuildRuntimeAddonsKeepsConfiguredEnabledAddons(t *testing.T) {
	addons := []StremioAddon{
		{Name: "Custom", URL: "https://example.com/manifest.json/", Enabled: true},
		{Name: "Disabled", URL: "https://disabled.example.com/manifest.json", Enabled: false},
	}

	runtimeAddons := BuildRuntimeAddons(addons, true, "rd-token", true, "https://comet.example.com")

	if len(runtimeAddons) != 1 {
		t.Fatalf("expected 1 configured addon, got %d", len(runtimeAddons))
	}
	if runtimeAddons[0].Name != "Custom" {
		t.Fatalf("expected configured addon name to be preserved, got %q", runtimeAddons[0].Name)
	}
	if runtimeAddons[0].URL != "https://example.com/manifest.json" {
		t.Fatalf("expected addon URL to be normalized, got %q", runtimeAddons[0].URL)
	}
}

func TestBuildRuntimeAddonsBootstrapsDefaultsForRealDebrid(t *testing.T) {
	runtimeAddons := BuildRuntimeAddons(nil, true, "rd-token", true, "")

	if len(runtimeAddons) != 2 {
		t.Fatalf("expected 2 default addons, got %d", len(runtimeAddons))
	}

	if runtimeAddons[0].Name != "Comet" || runtimeAddons[0].URL != DefaultCometAddonURL {
		t.Fatalf("expected Comet default addon first, got %#v", runtimeAddons[0])
	}

	if runtimeAddons[1].Name != "Torrentio" || runtimeAddons[1].URL != DefaultTorrentioAddonURL {
		t.Fatalf("expected Torrentio default addon second, got %#v", runtimeAddons[1])
	}
}

func TestBuildRuntimeAddonsReturnsEmptyWithoutRealDebrid(t *testing.T) {
	runtimeAddons := BuildRuntimeAddons(nil, false, "", true, "")

	if len(runtimeAddons) != 0 {
		t.Fatalf("expected no runtime addons without Real-Debrid, got %#v", runtimeAddons)
	}
}
