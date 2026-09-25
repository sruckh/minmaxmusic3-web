package server

import "testing"

// Text sharing a section tag's line is dropped by the model, so the form
// refuses it. Anything else in square brackets is the user's business.
func TestBadTagLine(t *testing.T) {
	for lyrics, want := range map[string]bool{
		"[Verse]\nla la la":         false,
		"  [CHORUS]  \nla":          false,
		"[Verse] la la la":          true,
		"la\n[Pre-Chorus] oh":       true,
		"[Verse":                    false, // unclosed: nothing after a tag
		"[laughs] then the words":   false, // not a section tag
		"la la [Verse] mid-line":    false, // a tag only counts at line start
		"[Instrumental]\n[Outro] x": true,
	} {
		if got := badTagLine(lyrics); got != want {
			t.Errorf("badTagLine(%q) = %v, want %v", lyrics, got, want)
		}
	}
}

