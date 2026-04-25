package settings

import "testing"

func TestApplyLegacySettingAliasesMapsRealDebridToken(t *testing.T) {
	cfg := getDefaultSettings()

	err := applyLegacySettingAliases([]byte(`{"realdebrid_token":"legacy-rd-token"}`), cfg)
	if err != nil {
		t.Fatalf("expected legacy alias parsing to succeed, got %v", err)
	}

	if cfg.RealDebridAPIKey != "legacy-rd-token" {
		t.Fatalf("expected legacy Real-Debrid token to populate API key, got %q", cfg.RealDebridAPIKey)
	}
}
