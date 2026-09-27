package abc

import (
	"errors"
	"strings"
	"testing"
)

// score is YuE2's native two-voice shape, as the worker's own tests write it:
// headers, two voices, chord symbols on the Ins line, ties and rests.
const score = `X:1
T:
M:4/4
L:1/32
Q:1/4=77
V: Vocal clef=treble name="Vocal Melody" snm="Vocal"
V: Ins clef=treble name="Ins Melody" snm="Inst."
K:Cm
% verse
V: Vocal
z8c8e8g8|^f8=e8e8d8|c16-c4B,4G,8|
V: Ins
"Cm"C8E8G8c8|"F#m7b5/A"A8c8_e8f8|"G7"G8B8d8f8|
w: ignored lyrics line
`

func head(key string, body string) string { return "X:1\nM:4/4\nL:1/8\nK:" + key + "\n" + body }

func TestTransposeSpelling(t *testing.T) {
	cases := []struct {
		name, key, body string
		n               int
		wantKey, want   string
	}{
		{"diatonic up a tone", "C", "CDEFGABc|", 2, "D", "DEFGABcd|"},
		// E is flat in C minor, and a tone up it is F, natural in D minor.
		{"minor uses the signature", "Cm", "cdef|", 2, "Dm", "defg|"},
		{"explicit sharp outside the key", "C", "^F|", 2, "D", "^G|"},
		{"natural against the key", "G", "=F|", 2, "A", "=G|"},
		// The second F inherits the bar's sharp; the barline clears it.
		{"bar accidental carries, barline clears", "C", "^FF|F|", 2, "D", "^G^G|G|"},
		{"octave marks upward", "C", "bc'|", 2, "D", "c'd'|"},
		{"octave marks downward", "C", "C,|", -1, "B", "B,,|"},
		// C up a semitone is Db (5 flats), not C# (7 sharps).
		{"fewest accidentals", "C", "C|", 1, "Db", "D|"},
		{"tie on accidentals prefers flats", "C", "C|", 6, "Gb", "G|"},
		{"rests and rhythm untouched", "C", "z4C2-C2|Z|", 2, "D", "z4D2-D2|Z|"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Transpose(head(tc.key, tc.body), tc.n)
			if err != nil {
				t.Fatal(err)
			}
			if want := head(tc.wantKey, tc.want); got != want {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestTransposeChords(t *testing.T) {
	cases := []struct {
		chord string
		n     int
		want  string
	}{
		{"Cm7", 2, "Dm7"},
		{"F#m7b5/A", 2, "G#m7b5/B"},
		{"C", 1, "Db"},
		{"E", 1, "F"},
		{"Bb7sus4", -2, "Ab7sus4"},
		{"m(maj7)", 2, "m(maj7)"}, // no pitch name: copied as written
	}
	for _, tc := range cases {
		got, err := Transpose(head("C", `"`+tc.chord+`"z|`), tc.n)
		if err != nil {
			t.Fatalf("%s: %v", tc.chord, err)
		}
		if !strings.Contains(got, `"`+tc.want+`"`) {
			t.Errorf("%s %+d: got %q, want chord %q", tc.chord, tc.n, got, tc.want)
		}
	}
}

// pitchesOf reads every note's pitch, the property Transpose exists to move.
func pitchesOf(t *testing.T, s string) []int {
	t.Helper()
	var ps []int
	if _, err := rewrite(s, 0, func(src, _ int) { ps = append(ps, src) }); err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestTransposeMovesEveryPitch(t *testing.T) {
	before := pitchesOf(t, score)
	if len(before) == 0 {
		t.Fatal("sample score read as no notes")
	}
	for n := -MaxSemitones; n <= MaxSemitones; n++ {
		got, err := Transpose(score, n)
		if err != nil {
			t.Fatalf("%+d: %v", n, err)
		}
		after := pitchesOf(t, got)
		if len(after) != len(before) {
			t.Fatalf("%+d: %d notes became %d", n, len(before), len(after))
		}
		for i := range before {
			if after[i] != before[i]+n {
				t.Errorf("%+d: note %d is %d, want %d\n%s", n, i, after[i], before[i]+n, got)
				break
			}
		}
		// And back again lands on the original pitches.
		back, err := Transpose(got, -n)
		if err != nil {
			t.Fatalf("%+d and back: %v", n, err)
		}
		if b := pitchesOf(t, back); len(b) != len(before) || b[0] != before[0] || b[len(b)-1] != before[len(before)-1] {
			t.Errorf("%+d and back does not return to the original pitches", n)
		}
	}
}

func TestTransposeLeavesEverythingElse(t *testing.T) {
	got, err := Transpose(score, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{
		"M:4/4", "L:1/32", "Q:1/4=77", `V: Vocal clef=treble name="Vocal Melody" snm="Vocal"`,
		"% verse", "w: ignored lyrics line",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q:\n%s", keep, got)
		}
	}
	if !strings.Contains(got, "K:Ebm") {
		t.Errorf("C minor up 3 should be E-flat minor:\n%s", got)
	}
}

func TestTransposeInlineKeyChange(t *testing.T) {
	got, err := Transpose(head("C", "C[K:G]F|"), 2)
	if err != nil {
		t.Fatal(err)
	}
	// In G the F is sharp; up a tone it is G#, which A major also sharpens.
	if want := head("D", "D[K:A]G|"); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTransposeRefuses(t *testing.T) {
	if _, err := Transpose(head("Cmix", "C|"), 2); !errors.Is(err, ErrUnsupportedKey) {
		t.Errorf("a mode the worker does not accept: err = %v", err)
	}
	if _, err := Transpose("X:1\nC|", 2); err == nil {
		t.Error("a score with no K: was accepted")
	}
	if _, err := Transpose(score, 12); err == nil {
		t.Error("an octave was accepted")
	}
	if got, err := Transpose(score, 0); err != nil || got != score {
		t.Error("zero must return the score unchanged")
	}
}

func TestTransposeKey(t *testing.T) {
	for _, tc := range []struct {
		from string
		n    int
		want string
	}{
		{"Cm", 2, "Dm"}, {"D#m", 2, "Fm"}, {"Eb", -3, "C"}, {"A", 1, "Bb"}, {"F#m", -1, "Fm"},
	} {
		if got, err := TransposeKey(tc.from, tc.n); err != nil || got != tc.want {
			t.Errorf("TransposeKey(%s, %+d) = %q, %v; want %s", tc.from, tc.n, got, err, tc.want)
		}
	}
}
