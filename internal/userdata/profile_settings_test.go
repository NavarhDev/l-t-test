package userdata

import "testing"

func TestParseProfileSettings_Defaults(t *testing.T) {
	settings := ParseProfileSettings("")
	if !settings.ForceHiddenStoryMissions {
		t.Fatal("expected ForceHiddenStoryMissions=true by default")
	}
	if settings.MasterDataVariant != "" {
		t.Fatalf("expected empty MasterDataVariant by default, got %q", settings.MasterDataVariant)
	}
}

func TestParseProfileSettings_MasterDataVariant(t *testing.T) {
	tests := []struct {
		message  string
		expected string
	}{
		{"rAll", "rAll"},
		{"vAll", "vAll"},
		{"All", "All"},
		{"r1", "r1"},
		{"bonus", "bonus"},
		{"  rAll  ", "rAll"}, // whitespace trimmed
	}
	for _, tt := range tests {
		settings := ParseProfileSettings(tt.message)
		if settings.MasterDataVariant != tt.expected {
			t.Errorf("ParseProfileSettings(%q).MasterDataVariant = %q, want %q", tt.message, settings.MasterDataVariant, tt.expected)
		}
	}
}

func TestParseProfileSettings_DisableHiddenStoryMissions(t *testing.T) {
	settings := ParseProfileSettings("dhsm")
	if settings.ForceHiddenStoryMissions {
		t.Fatal("expected ForceHiddenStoryMissions=false when dhsm is present")
	}
}

func TestParseProfileSettings_Combined(t *testing.T) {
	// Profile: "rAll dhsm" → master data variant + disable hidden story missions
	settings := ParseProfileSettings("rAll dhsm")
	if settings.MasterDataVariant != "rAll" {
		t.Errorf("expected MasterDataVariant=rAll, got %q", settings.MasterDataVariant)
	}
	if settings.ForceHiddenStoryMissions {
		t.Error("expected ForceHiddenStoryMissions=false when dhsm is present")
	}
}

func TestParseProfileSettings_OrderIndependent(t *testing.T) {
	// Order shouldn't matter
	s1 := ParseProfileSettings("dhsm rAll")
	s2 := ParseProfileSettings("rAll dhsm")
	if s1.MasterDataVariant != s2.MasterDataVariant {
		t.Errorf("order matters for MasterDataVariant: %q vs %q", s1.MasterDataVariant, s2.MasterDataVariant)
	}
	if s1.ForceHiddenStoryMissions != s2.ForceHiddenStoryMissions {
		t.Errorf("order matters for ForceHiddenStoryMissions: %v vs %v", s1.ForceHiddenStoryMissions, s2.ForceHiddenStoryMissions)
	}
}

func TestParseProfileSettings_IgnoresUnknownTokens(t *testing.T) {
	// Unknown tokens should be silently ignored
	settings := ParseProfileSettings("rAll unknown_token dhsm another")
	if settings.MasterDataVariant != "rAll" {
		t.Errorf("expected MasterDataVariant=rAll, got %q", settings.MasterDataVariant)
	}
	if settings.ForceHiddenStoryMissions {
		t.Error("expected ForceHiddenStoryMissions=false when dhsm is present")
	}
}

func TestParseProfileSettings_LastVariantWins(t *testing.T) {
	// If multiple variants are present, the last one wins
	settings := ParseProfileSettings("rAll vAll")
	if settings.MasterDataVariant != "vAll" {
		t.Errorf("expected last variant to win: got %q, want vAll", settings.MasterDataVariant)
	}
}
