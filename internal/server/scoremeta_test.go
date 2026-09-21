package server

import "testing"

// The three properties are read back out of the score the model wrote. That is
// the only honest source: YuE2 has no request field for them, and putting them
// in the style line does not control them — measured over six jobs, two of
// three seeds returned identical tempos with and without the hint, and key and
// meter were ignored outright.
func TestScoreMetaReadsTheThreeHeaders(t *testing.T) {
	// The shape the worker actually returns, from a live run.
	abc := "X:1\nT:\nM:2/4\nL:1/32\nQ:1/4=60\n" +
		"V: Vocal clef=treble name=\"Vocal Melody\" snm=\"Vocal\"\n" +
		"V: Ins clef=treble name=\"Ins Melody\" snm=\"Inst.\"\n" +
		"K:Cm\n% intro\nV: Vocal\nZ|\"Cm\"z16|\n"

	m := scoreMetaOf(abc)
	if m.Key != "Cm" {
		t.Errorf("Key = %q, want Cm", m.Key)
	}
	if m.Meter != "2/4" {
		t.Errorf("Meter = %q, want 2/4", m.Meter)
	}
	// Only the value survives; "1/4=" is the beat note and is not worth showing.
	if m.Tempo != "60" {
		t.Errorf("Tempo = %q, want 60", m.Tempo)
	}
	if !m.Any() {
		t.Error("a score with all three headers reports nothing to show")
	}
}

// A MiniMax song has no score, so there is nothing to render — not a row of
// empty labels.
func TestScoreMetaIsEmptyWithoutAScore(t *testing.T) {
	if m := scoreMetaOf(""); m.Any() {
		t.Errorf("an empty score produced %+v", m)
	}
}

// A partial score shows what it has. Half a metadata row beats none, and the
// template guards each field separately for exactly this.
func TestScoreMetaShowsWhatIsPresent(t *testing.T) {
	m := scoreMetaOf("X:1\nM:4/4\nK:G\nV: Vocal\nZ|\n")
	if m.Meter != "4/4" || m.Key != "G" {
		t.Errorf("Key/Meter = %q/%q, want G/4/4", m.Key, m.Meter)
	}
	if m.Tempo != "" {
		t.Errorf("Tempo = %q, want empty — the score has no Q: header", m.Tempo)
	}
	if !m.Any() {
		t.Error("a score with two of three headers should still display")
	}
}

// A "K:" after the header block is not a header. The first one wins, because
// that is the one the format defines and the one abc_score.py reads.
func TestScoreMetaTakesTheFirstHeaderNotALaterOne(t *testing.T) {
	abc := "X:1\nK:Am\nM:3/4\nQ:1/4=90\nV: Vocal\nZ|\n% K: not a header\n"
	m := scoreMetaOf(abc)
	if m.Key != "Am" {
		t.Errorf("Key = %q, want Am — a later line must not overwrite it", m.Key)
	}
	if m.Meter != "3/4" || m.Tempo != "90" {
		t.Errorf("Meter/Tempo = %q/%q, want 3/4/90", m.Meter, m.Tempo)
	}
}
