package userdata

import "strings"

// ProfileSettings holds per-user configuration parsed from the profile message.
// The profile message is treated as space-separated tokens, where each token
// controls a specific aspect of the user's experience.
type ProfileSettings struct {
	// MasterDataVariant selects which master-data file variant to serve.
	// Valid values: "0", "All", "bonus", "r1", "r2", "rAll", "v1", "v2", "vAll".
	// Empty string means use the default variant.
	MasterDataVariant string

	// ForceHiddenStoryMissions controls whether hidden-story missions (category 7)
	// are presented to the client as already completed. When true, the missions
	// are force-completed; when false, they must be cleared legitimately.
	// Default: true (force complete).
	ForceHiddenStoryMissions bool
}

// validMasterDataVariants lists recognized master-data variant tokens.
var validMasterDataVariants = map[string]bool{
	"or":     true,
	"Clear":    true,
	"Rec":  	true,
	"Var":    	true,
	"Gacha":    true,
	"Bonus":  	true,
}

// IsValidMasterDataVariant reports whether variant is a recognized master-data
// variant token (the same set accepted by ParseProfileSettings).
func IsValidMasterDataVariant(variant string) bool {
	return validMasterDataVariants[variant]
}

// ParseProfileSettings extracts configuration tokens from the profile message.
// The message is treated as space-separated tokens. Recognized tokens:
//   - Master-data variants: "Clear" ,"Rec", "Var", "Gacha", "Bonus"
//     → sets MasterDataVariant
//   - "dhsm" (disable hidden story missions) → sets ForceHiddenStoryMissions=true
//
// Unrecognized tokens are silently ignored, allowing future extensibility.
// Default: ForceHiddenStoryMissions=false (force complete hidden story missions).
func ParseProfileSettings(message string) ProfileSettings {
	settings := ProfileSettings{
		ForceHiddenStoryMissions: false, // default
	}

	for _, token := range strings.Fields(strings.TrimSpace(message)) {
		if validMasterDataVariants[token] {
			settings.MasterDataVariant = token
		} else if token == "dhsm" {
			settings.ForceHiddenStoryMissions = true
		}
		// Future tokens can be added here with additional else-if branches
	}

	return settings
}
