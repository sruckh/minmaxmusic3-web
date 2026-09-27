// Package abc rewrites the ABC scores YuE2 plans and sings from.
//
// The one operation it offers is transposition, and it exists because of a
// mistake worth recording. This app once concluded that a YuE2 score could not
// be re-keyed, on the grounds that "ABC note tokens are relative, so changing
// K: respells the same letters rather than transposing". The premise is wrong:
// an ABC note letter is an absolute pitch name, and K: only supplies the key
// signature — which letters are sharpened or flattened by default. Changing K:
// alone therefore does change pitches, but not uniformly: K:Cm to K:Dm turns
// every E-flat and A-flat natural, which is a change of mode, not of key. A
// real key change moves every note, every chord symbol and every key field by
// the same interval, and that is what Transpose does.
//
// The dialect is YuE2's native two-voice score, as the worker's
// `worker/abc_score.py` reads it: `^ ^^ _ __ =` accidentals, note letters
// A-G/a-g with `'` and `,` octave marks, chord symbols in double quotes, and
// key changes either as a `K:` line or inline as `[K:...]`. Key names are the
// thirty standard major and minor keys the worker accepts; anything else is
// refused here rather than sent to a job that would refuse it after queueing.
package abc

import (
	"errors"
	"fmt"
	"strings"
)

// letters and naturalPC give each note letter's position and its pitch class
// with no accidental. Position arithmetic (letter + 7*octave) is how a
// transposition keeps its spelling diatonic.
const letters = "CDEFGAB"

var naturalPC = [7]int{0, 2, 4, 5, 7, 9, 11}

// Keys the worker accepts, mapped to their signature: positive is that many
// sharps, negative that many flats. From `_MAJOR_KEYS` / `_MINOR_KEYS` in
// the worker's abc_score.py.
var keySignatures = map[string]int{
	"Cb": -7, "Gb": -6, "Db": -5, "Ab": -4, "Eb": -3, "Bb": -2, "F": -1, "C": 0,
	"G": 1, "D": 2, "A": 3, "E": 4, "B": 5, "F#": 6, "C#": 7,
	"Abm": -7, "Ebm": -6, "Bbm": -5, "Fm": -4, "Cm": -3, "Gm": -2, "Dm": -1, "Am": 0,
	"Em": 1, "Bm": 2, "F#m": 3, "C#m": 4, "G#m": 5, "D#m": 6, "A#m": 7,
}

// MaxSemitones bounds a transposition. Beyond an octave the result is the
// same key, only further from the range the model was trained to sing.
const MaxSemitones = 11

// ErrUnsupportedKey is returned for a key field the worker would not accept,
// before or after transposing.
var ErrUnsupportedKey = errors.New("unsupported key")

type key struct {
	letter int // index into letters
	alt    int // -1 flat, 0 natural, +1 sharp
	minor  bool
}

func parseKey(s string) (key, error) {
	s = strings.TrimSpace(s)
	if _, ok := keySignatures[s]; !ok {
		return key{}, fmt.Errorf("%w %q: use a standard major or minor key", ErrUnsupportedKey, s)
	}
	k := key{letter: strings.IndexByte(letters, s[0])}
	rest := s[1:]
	switch {
	case strings.HasPrefix(rest, "#"):
		k.alt, rest = 1, rest[1:]
	case strings.HasPrefix(rest, "b"):
		k.alt, rest = -1, rest[1:]
	}
	k.minor = rest == "m"
	return k, nil
}

func (k key) String() string {
	s := letters[k.letter : k.letter+1]
	switch k.alt {
	case 1:
		s += "#"
	case -1:
		s += "b"
	}
	if k.minor {
		s += "m"
	}
	return s
}

// signature is the default alteration of each letter under this key.
func (k key) signature() [7]int {
	var sig [7]int
	n := keySignatures[k.String()]
	for i := range n {
		sig[strings.IndexByte(letters, "FCGDAEB"[i])] = 1
	}
	for i := range -n {
		sig[strings.IndexByte(letters, "BEADGCF"[i])] = -1
	}
	return sig
}

// mod is the non-negative remainder.
func mod(a, m int) int { return ((a % m) + m) % m }

// floorDiv rounds toward negative infinity, so octaves below C4 come out right.
func floorDiv(a, m int) int {
	if a < 0 && a%m != 0 {
		return a/m - 1
	}
	return a / m
}

// wrapAlt folds an alteration into -6..5, the smallest signed distance
// between two pitch classes.
func wrapAlt(a int) int {
	a = mod(a, 12)
	if a > 5 {
		a -= 12
	}
	return a
}

// interval is a transposition expressed twice: in semitones, which fixes the
// pitch, and in diatonic steps, which fixes the spelling.
type interval struct{ semitones, steps int }

// targetKey picks the key a transposition lands in: the accepted spelling with
// the fewest accidentals, preferring flats on a tie (Gb over F#), because an
// accepted key must be chosen before any note can be spelled against it.
func targetKey(from key, semitones int) (key, interval, error) {
	pc := mod(naturalPC[from.letter]+from.alt+semitones, 12)
	best, bestCount, found := key{}, 0, false
	for l := range 7 {
		alt := wrapAlt(pc - naturalPC[l])
		if alt < -1 || alt > 1 {
			continue
		}
		cand := key{letter: l, alt: alt, minor: from.minor}
		n, ok := keySignatures[cand.String()]
		if !ok {
			continue
		}
		if !found || abs(n) < abs(bestCount) || (abs(n) == abs(bestCount) && n < 0) {
			best, bestCount, found = cand, n, true
		}
	}
	if !found {
		return key{}, interval{}, fmt.Errorf("%w: no accepted spelling for %s moved %+d", ErrUnsupportedKey, from, semitones)
	}
	// The letter distance is only known modulo 7; the step count is the
	// candidate nearest the semitone distance. C minor down 11 is C# minor —
	// the same letter, but seven steps down, not zero.
	d := mod(best.letter-from.letter, 7)
	steps := d
	for _, s := range []int{d - 7, d + 7} {
		if abs(12*s-7*semitones) < abs(12*steps-7*semitones) {
			steps = s
		}
	}
	return best, interval{semitones: semitones, steps: steps}, nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// moveKey applies an interval already fixed by the header key to a later key
// change, so every key in the score moves by the same spelled interval.
func moveKey(k key, iv interval) (key, error) {
	l := mod(k.letter+iv.steps, 7)
	alt := wrapAlt(naturalPC[k.letter] + k.alt + iv.semitones - naturalPC[l])
	moved := key{letter: l, alt: alt, minor: k.minor}
	if alt < -1 || alt > 1 {
		return key{}, fmt.Errorf("%w: %s moved %+d has no accepted spelling", ErrUnsupportedKey, k, iv.semitones)
	}
	if _, ok := keySignatures[moved.String()]; !ok {
		return key{}, fmt.Errorf("%w: %s moved %+d is %s", ErrUnsupportedKey, k, iv.semitones, moved)
	}
	return moved, nil
}

// TransposeKey names the key a score in k lands in when moved by semitones —
// the same choice Transpose makes for the header.
func TransposeKey(k string, semitones int) (string, error) {
	from, err := parseKey(k)
	if err != nil {
		return "", err
	}
	to, _, err := targetKey(from, semitones)
	if err != nil {
		return "", err
	}
	return to.String(), nil
}

// Transpose moves a whole score by semitones: every note, every chord symbol
// and every key field. Meter, tempo, rhythm, voices, lyrics lines and comments
// are left byte-identical. Zero returns the score unchanged.
//
// Notes are spelled against the new key, and an explicit accidental is written
// whenever the pitch differs from the key signature or its letter has already
// carried an accidental in the bar. That second rule means the result reads the
// same whether a reader applies a bar's accidental to one octave (ABC 2.1) or
// to every octave of that letter — it may write an accidental a strict reader
// would not need, never omit one a loose reader would.
func Transpose(score string, semitones int) (string, error) {
	if semitones < -MaxSemitones || semitones > MaxSemitones {
		return "", fmt.Errorf("transpose by %d: must be within ±%d semitones", semitones, MaxSemitones)
	}
	if semitones == 0 {
		return score, nil
	}
	return rewrite(score, semitones, nil)
}

// rewrite is Transpose without the guards, with an optional hook that sees
// each note's pitch before and after — which is how the tests check pitches
// rather than spellings.
func rewrite(score string, semitones int, onNote func(src, dst int)) (string, error) {
	r := &rewriter{onNote: onNote}
	lines := strings.Split(score, "\n")
	for i, line := range lines {
		out, err := r.line(line, semitones)
		if err != nil {
			return "", fmt.Errorf("line %d: %w", i+1, err)
		}
		lines[i] = out
	}
	if !r.keyed {
		return "", errors.New("score has no K: key field")
	}
	return strings.Join(lines, "\n"), nil
}

type rewriter struct {
	keyed      bool // the header K: has been read, so music lines follow
	iv         interval
	srcSig     [7]int
	dstSig     [7]int
	srcBar     map[int]int // position (letter + 7*octave) -> alteration, this bar
	dstTouched [7]bool     // letters written with an accidental, this bar
	onNote     func(src, dst int)
}

func (r *rewriter) resetBar() {
	r.srcBar = map[int]int{}
	r.dstTouched = [7]bool{}
}

// setKey handles both the header K: and any later key change.
func (r *rewriter) setKey(value string, semitones int) (string, error) {
	// A trailing comment or clef after the key name is kept as written.
	name, rest, _ := strings.Cut(strings.TrimSpace(value), " ")
	from, err := parseKey(name)
	if err != nil {
		return "", err
	}
	var to key
	if !r.keyed {
		to, r.iv, err = targetKey(from, semitones)
		r.keyed = true
	} else {
		to, err = moveKey(from, r.iv)
	}
	if err != nil {
		return "", err
	}
	r.srcSig, r.dstSig = from.signature(), to.signature()
	r.resetBar()
	if rest != "" {
		return to.String() + " " + rest, nil
	}
	return to.String(), nil
}

func isField(line string) bool {
	return len(line) >= 2 && line[1] == ':' &&
		((line[0] >= 'A' && line[0] <= 'Z') || (line[0] >= 'a' && line[0] <= 'z'))
}

func (r *rewriter) line(line string, semitones int) (string, error) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "K:") {
		k, err := r.setKey(trimmed[2:], semitones)
		if err != nil {
			return "", err
		}
		return "K:" + k, nil
	}
	if !r.keyed || trimmed == "" || strings.HasPrefix(trimmed, "%") || isField(trimmed) {
		return line, nil
	}
	return r.music(line)
}

// music rewrites one line of notes. A line always starts a fresh bar.
func (r *rewriter) music(line string) (string, error) {
	r.resetBar()
	var b strings.Builder
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '"':
			end := strings.IndexByte(line[i+1:], '"')
			if end < 0 {
				b.WriteString(line[i:])
				return b.String(), nil
			}
			chord, err := r.chord(line[i+1 : i+1+end])
			if err != nil {
				return "", err
			}
			b.WriteString(`"` + chord + `"`)
			i += end + 2
		case c == '[' && i+2 < len(line) && line[i+2] == ':':
			end := strings.IndexByte(line[i:], ']')
			if end < 0 {
				b.WriteString(line[i:])
				return b.String(), nil
			}
			field := line[i+1 : i+end]
			if field[0] == 'K' {
				k, err := r.setKey(field[2:], r.iv.semitones)
				if err != nil {
					return "", err
				}
				field = "K:" + k
			}
			b.WriteString("[" + field + "]")
			i += end + 1
		case c == '!':
			end := strings.IndexByte(line[i+1:], '!')
			if end < 0 {
				b.WriteString(line[i:])
				return b.String(), nil
			}
			b.WriteString(line[i : i+end+2])
			i += end + 2
		case c == '|':
			r.resetBar()
			b.WriteByte(c)
			i++
		default:
			n, note, err := r.note(line[i:])
			if err != nil {
				return "", err
			}
			if n == 0 {
				b.WriteByte(c)
				i++
				continue
			}
			b.WriteString(note)
			i += n
		}
	}
	return b.String(), nil
}

var accidentals = []struct {
	text string
	alt  int
}{{"^^", 2}, {"__", -2}, {"^", 1}, {"_", -1}, {"=", 0}}

// note reads one pitched note at the start of s — accidental, letter and
// octave marks, leaving duration and tie for the caller to copy — and returns
// how many bytes it consumed and what to write instead. Zero bytes means s
// does not start with a pitched note.
func (r *rewriter) note(s string) (int, string, error) {
	i, explicit, alt := 0, false, 0
	for _, a := range accidentals {
		if strings.HasPrefix(s, a.text) {
			i, explicit, alt = len(a.text), true, a.alt
			break
		}
	}
	if i >= len(s) {
		return 0, "", nil
	}
	c := s[i]
	var letter, octave int
	switch {
	case c >= 'A' && c <= 'G':
		letter, octave = strings.IndexByte(letters, c), 4
	case c >= 'a' && c <= 'g':
		letter, octave = strings.IndexByte(letters, c-'a'+'A'), 5
	default:
		return 0, "", nil
	}
	i++
	for i < len(s) && (s[i] == '\'' || s[i] == ',') {
		if s[i] == '\'' {
			octave++
		} else {
			octave--
		}
		i++
	}

	pos := letter + 7*octave
	if explicit {
		r.srcBar[pos] = alt
	} else if a, ok := r.srcBar[pos]; ok {
		alt = a
	} else {
		alt = r.srcSig[letter]
	}
	src := naturalPC[letter] + 12*octave + alt
	dst := src + r.iv.semitones

	pos2 := pos + r.iv.steps
	l2, o2 := mod(pos2, 7), floorDiv(pos2, 7)
	alt2 := dst - (naturalPC[l2] + 12*o2)
	if alt2 < -2 || alt2 > 2 {
		return 0, "", fmt.Errorf("note %q cannot be spelled after moving %+d", s[:i], r.iv.semitones)
	}
	if r.onNote != nil {
		r.onNote(src, dst)
	}

	var b strings.Builder
	if alt2 != r.dstSig[l2] || r.dstTouched[l2] {
		for _, a := range accidentals {
			if a.alt == alt2 {
				b.WriteString(a.text)
				break
			}
		}
		r.dstTouched[l2] = true
	}
	if o2 >= 5 {
		b.WriteByte(letters[l2] - 'A' + 'a')
		b.WriteString(strings.Repeat("'", o2-5))
	} else {
		b.WriteByte(letters[l2])
		b.WriteString(strings.Repeat(",", 4-o2))
	}
	return i, b.String(), nil
}

// chord moves a chord symbol's root and bass, leaving its quality alone:
// "F#m7b5/A" up two is "G#m7b5/B". A quoted token that does not start with a
// pitch name is copied unchanged — the worker is the judge of whether it is
// a chord at all.
func (r *rewriter) chord(sym string) (string, error) {
	root, n := pitchName(sym)
	if n == 0 {
		return sym, nil
	}
	quality, bass := sym[n:], ""
	if slash := strings.LastIndexByte(quality, '/'); slash >= 0 {
		if b, m := pitchName(quality[slash+1:]); m > 0 && m == len(quality)-slash-1 {
			moved, err := r.movePitch(b)
			if err != nil {
				return "", err
			}
			quality, bass = quality[:slash], "/"+moved
		}
	}
	moved, err := r.movePitch(root)
	if err != nil {
		return "", err
	}
	return moved + quality + bass, nil
}

type pitch struct{ letter, alt int }

// pitchName reads a chord-symbol pitch — a letter and up to two sharps or
// flats — and reports how many bytes it used, zero if there is none. No chord
// quality begins with b or #, so the split is unambiguous.
func pitchName(s string) (pitch, int) {
	if s == "" || s[0] < 'A' || s[0] > 'G' {
		return pitch{}, 0
	}
	p := pitch{letter: strings.IndexByte(letters, s[0])}
	for _, a := range []struct {
		text string
		alt  int
	}{{"##", 2}, {"bb", -2}, {"#", 1}, {"b", -1}} {
		if strings.HasPrefix(s[1:], a.text) {
			p.alt = a.alt
			return p, 1 + len(a.text)
		}
	}
	return p, 1
}

func (r *rewriter) movePitch(p pitch) (string, error) {
	l := mod(p.letter+r.iv.steps, 7)
	alt := wrapAlt(naturalPC[p.letter] + p.alt + r.iv.semitones - naturalPC[l])
	if alt < -2 || alt > 2 {
		return "", fmt.Errorf("chord root cannot be spelled after moving %+d", r.iv.semitones)
	}
	s := letters[l : l+1]
	switch alt {
	case 2:
		s += "##"
	case 1:
		s += "#"
	case -1:
		s += "b"
	case -2:
		s += "bb"
	}
	return s, nil
}
