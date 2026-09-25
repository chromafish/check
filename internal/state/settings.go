package state

import "path/filepath"

// Settings are preferences, as opposed to the review of any particular
// change: they belong to the person, so they are kept once,
// beside the recent list, rather than per repository.
//
// Every field is optional, and a zero value means "whatever the design system
// says". Nothing here is required for the application to start, so a missing
// or unreadable file is not an error — it is the defaults.
type Settings struct {
	// FontFamily is the monospaced typeface the interface is set in. Empty
	// means the face that ships in the binary.
	FontFamily string `json:"font_family,omitempty"`
	// FontSize is the body size in points, which the whole type scale is
	// anchored on. Zero means the scale's own body size.
	FontSize int `json:"font_size,omitempty"`
	// Theme is the id of the colour scheme, which is the file name of a
	// scheme in ThemesDir. Empty means the one that ships in the binary.
	Theme string `json:"theme,omitempty"`
	// Dark is which mode of the scheme was last in force.
	Dark bool `json:"dark,omitempty"`
	// NoWrap disables soft wrapping of long diff lines. Zero means wrapped:
	// wrapping is on by default, and only turning it off is recorded.
	NoWrap bool `json:"no_wrap,omitempty"`
	// NoSemanticFind keeps find to string matching even when a key for the
	// service is set. Asking a question about a change sends the lines it
	// might be about to a third party, so this is how someone who has a key
	// for one repository declines to use it in another.
	NoSemanticFind bool `json:"no_semantic_find,omitempty"`
	// Classic is the three-column revision view instead of the brief. Zero
	// means the brief: it is the default, and only leaving it is recorded.
	Classic bool `json:"classic,omitempty"`
	// CloneDir is where a pasted repository link is cloned, one directory
	// per host and repository under it. Empty means the default, ~/Clones.
	CloneDir string `json:"clone_dir,omitempty"`
	// TypeSafeKey is the API key asking a question uses. It is written here
	// by hand: the settings sheet is a list of choices with no text entry,
	// and this file is rewritten whenever a preference changes, so the key
	// is carried through every save rather than being typed into the
	// interface. CHECK_TYPESAFE_KEY overrides it for a single run.
	TypeSafeKey string `json:"typesafe_key,omitempty"`
}

// ThemesDir is where colour schemes are read from: base16 files, in the same
// directory as the settings. Nothing writes here — a person drops in whatever
// scheme they already use.
func ThemesDir() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "themes"), nil
}

// LoadSettings reads the preferences, returning the defaults if there are none
// to read.
func LoadSettings() Settings {
	var s Settings
	readJSON("settings.json", &s)
	return s
}

// SaveSettings writes the preferences out atomically.
func SaveSettings(s Settings) error { return writeJSON("settings.json", s) }
