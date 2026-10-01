// Package lyrics identifies local recordings and retrieves editable lyric drafts.
package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrUnavailable  = errors.New("lyrics lookup is unavailable")
	ErrInvalidAudio = errors.New("unsupported or invalid audio")
	ErrProvider     = errors.New("lyrics provider could not complete lookup")
	ErrRateLimited  = errors.New("lyrics provider rate limited lookup")
)

const userAgent = "minmaxmusic3-web/1.0 (https://github.com/sruckh/minmaxmusic3-web)"
const maxBody = 2 << 20
const maxLyrics = 10000

var errLyricsTooLong = errors.New("lyrics exceed the editable draft limit; trim manually")

// AutoSelect is -1 unless exactly one reliable lyric result has a clear lead.
type Result struct {
	Candidates []Candidate `json:"candidates"`
	AutoSelect int         `json:"autoSelect"`
	Warnings   []string    `json:"warnings"`
}

type Candidate struct {
	RecordingID  string  `json:"recordingID"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	Version      string  `json:"version"`
	Score        float64 `json:"score"`
	Duration     float64 `json:"duration"`
	Lyrics       string  `json:"lyrics"`
	Instrumental bool    `json:"instrumental"`
	LRCLIBID     int64   `json:"lrclibID"`
	Source       string  `json:"source"`
	SourceURL    string  `json:"sourceURL"`
	Ambiguous    bool    `json:"ambiguous"`
}

type limiter struct {
	mu       sync.Mutex
	gate     chan struct{}
	next     time.Time
	interval time.Duration
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	if l.gate == nil {
		l.gate = make(chan struct{}, 1)
	}
	gate := l.gate
	l.mu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-gate }()
	if delay := time.Until(l.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.next = time.Now().Add(l.interval)
	return nil
}

type Client struct {
	key, ffmpeg, ffprobe, fpcalc  string
	http                          *http.Client
	acoustid, musicbrainz, lrclib string
	acousticRate, brainzRate      limiter
	decodeSlots                   chan struct{}
	fingerprint                   func(context.Context, string) (float64, string, error)
}

func New(apiKey string) *Client {
	c := &Client{key: strings.TrimSpace(apiKey),
		http:     &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		acoustid: "https://api.acoustid.org/v2/lookup", musicbrainz: "https://musicbrainz.org/ws/2/recording/", lrclib: "https://lrclib.net/api/",
		decodeSlots: make(chan struct{}, 2),
	}
	c.acousticRate.interval = time.Second/3 + time.Millisecond
	c.brainzRate.interval = time.Second
	c.ffmpeg, _ = exec.LookPath("ffmpeg")
	c.ffprobe, _ = exec.LookPath("ffprobe")
	c.fpcalc, _ = exec.LookPath("fpcalc")
	c.fingerprint = c.localFingerprint
	return c
}

func (c *Client) Available() bool {
	return c != nil && c.key != "" && c.ffmpeg != "" && c.ffprobe != "" && c.fpcalc != ""
}

func (c *Client) Lookup(parent context.Context, path string) (Result, error) {
	result := Result{Candidates: []Candidate{}, AutoSelect: -1, Warnings: []string{}}
	if !c.Available() {
		return result, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, 40*time.Second)
	defer cancel()
	select {
	case c.decodeSlots <- struct{}{}:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	duration, fingerprint, err := c.fingerprint(ctx, path)
	<-c.decodeSlots
	if err != nil {
		return result, err
	}
	if !validDuration(duration) || fingerprint == "" || len(fingerprint) > 65536 {
		return result, ErrInvalidAudio
	}
	if err = c.acousticRate.wait(ctx); err != nil {
		return result, err
	}
	form := url.Values{"client": {c.key}, "duration": {strconv.Itoa(int(math.Round(duration)))}, "fingerprint": {fingerprint}, "meta": {"recordings recordingids"}, "format": {"json"}}
	var identified struct {
		Status  string `json:"status"`
		Results []struct {
			Score      float64 `json:"score"`
			Recordings []struct {
				ID string `json:"id"`
			} `json:"recordings"`
		} `json:"results"`
	}
	if err = c.request(ctx, http.MethodPost, c.acoustid, form, &identified); err != nil {
		return result, err
	}
	if identified.Status != "ok" {
		return result, ErrProvider
	}
	scores := map[string]float64{}
	for _, r := range identified.Results {
		if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) || r.Score < 0 || r.Score > 1 {
			continue
		}
		for _, recording := range r.Recordings {
			if uuid.MatchString(recording.ID) && r.Score > scores[recording.ID] {
				scores[recording.ID] = r.Score
			}
		}
	}
	for id, score := range scores {
		result.Candidates = append(result.Candidates, Candidate{RecordingID: id, Score: score, Duration: duration})
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].Score == result.Candidates[j].Score {
			return result.Candidates[i].RecordingID < result.Candidates[j].RecordingID
		}
		return result.Candidates[i].Score > result.Candidates[j].Score
	})
	if len(result.Candidates) > 3 {
		result.Candidates = result.Candidates[:3]
	}
	for i := range result.Candidates {
		candidate := &result.Candidates[i]
		if err = c.enrich(ctx, candidate); err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			candidate.Ambiguous = true
			if errors.Is(err, ErrRateLimited) {
				result.Warnings = append(result.Warnings, "MusicBrainz rate limited lookup; retry later.")
			} else {
				result.Warnings = append(result.Warnings, "MusicBrainz metadata unavailable for a candidate.")
			}
			continue
		}
		if err = c.findLyrics(ctx, candidate); err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			candidate.Ambiguous = true
			if errors.Is(err, errLyricsTooLong) {
				result.Warnings = append(result.Warnings, "Retrieved lyrics are too long; enter and trim them manually.")
			} else if errors.Is(err, ErrRateLimited) {
				result.Warnings = append(result.Warnings, "LRCLIB rate limited lookup; retry later.")
			} else {
				result.Warnings = append(result.Warnings, "LRCLIB lyrics unavailable for a candidate.")
			}
		}
	}
	reliable := 0
	for _, candidate := range result.Candidates {
		if candidate.Lyrics != "" && !candidate.Ambiguous {
			reliable++
		}
	}
	if len(result.Candidates) > 0 {
		top := result.Candidates[0]
		clear := len(result.Candidates) == 1 || top.Score-result.Candidates[1].Score >= 0.05
		if top.Score >= 0.9 && clear && reliable == 1 && top.Lyrics != "" && !top.Ambiguous {
			result.AutoSelect = 0
		}
	}
	return result, nil
}

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validDuration(d float64) bool {
	return d > 0 && d <= 24*60*60 && !math.IsInf(d, 0) && !math.IsNaN(d)
}

func (c *Client) request(ctx context.Context, method, endpoint string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return ErrProvider
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrProvider
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return ErrProvider
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return ErrProvider
	}
	if json.Unmarshal(data, out) != nil {
		return ErrProvider
	}
	return nil
}

var errNotFound = errors.New("not found")

func (c *Client) enrich(ctx context.Context, candidate *Candidate) error {
	if err := c.brainzRate.wait(ctx); err != nil {
		return err
	}
	var recording struct {
		Title          string `json:"title"`
		Disambiguation string `json:"disambiguation"`
		Length         int64  `json:"length"`
		Artists        []struct {
			Name   string `json:"name"`
			Join   string `json:"joinphrase"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
		} `json:"artist-credit"`
		Releases []struct {
			Title string `json:"title"`
		} `json:"releases"`
	}
	if err := c.request(ctx, http.MethodGet, c.musicbrainz+candidate.RecordingID+"?inc=artist-credits%2Breleases&fmt=json", nil, &recording); err != nil {
		return err
	}
	title, version := recording.Title, recording.Disambiguation
	var credited strings.Builder
	for i, artist := range recording.Artists {
		if i >= 10 {
			break
		}
		name := artist.Name
		if name == "" {
			name = artist.Artist.Name
		}
		if len(name) > 500 || len(artist.Join) > 100 {
			return ErrProvider
		}
		credited.WriteString(name + artist.Join)
	}
	artist, album := credited.String(), ""
	if len(recording.Releases) > 0 {
		album = recording.Releases[0].Title
	}
	if title == "" || artist == "" || len(title) > 500 || len(artist) > 500 || len(album) > 500 || len(version) > 500 {
		return ErrProvider
	}
	candidate.Title, candidate.Artist, candidate.Album, candidate.Version = title, artist, album, version
	if recording.Length > 0 && math.Abs(float64(recording.Length)/1000-candidate.Duration) > 2 {
		candidate.Ambiguous = true
	}
	return nil
}

type lyricRecord struct {
	ID           int64   `json:"id"`
	Track        string  `json:"trackName"`
	Artist       string  `json:"artistName"`
	Album        string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	Plain        string  `json:"plainLyrics"`
	Synced       string  `json:"syncedLyrics"`
}

func normalized(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
func matches(r lyricRecord, c *Candidate) bool {
	return r.ID > 0 && normalized(r.Track) == normalized(c.Title) && normalized(r.Artist) == normalized(c.Artist) && validDuration(r.Duration) && math.Abs(r.Duration-c.Duration) <= 2
}
func (c *Client) findLyrics(ctx context.Context, candidate *Candidate) error {
	params := url.Values{"track_name": {candidate.Title}, "artist_name": {candidate.Artist}, "album_name": {candidate.Album}, "duration": {strconv.Itoa(int(math.Round(candidate.Duration)))}}
	var exact lyricRecord
	err := c.request(ctx, http.MethodGet, c.lrclib+"get?"+params.Encode(), nil, &exact)
	if err == nil && matches(exact, candidate) {
		// Disambiguated versions cannot be verified from LRCLIB's fields alone.
		if candidate.Version != "" {
			candidate.Ambiguous = true
		}
		return applyLyrics(candidate, exact)
	}
	if err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	params.Del("duration")
	params.Del("album_name")
	var records []lyricRecord
	if err = c.request(ctx, http.MethodGet, c.lrclib+"search?"+params.Encode(), nil, &records); err != nil {
		return err
	}
	if len(records) > 100 {
		return ErrProvider
	}
	var matching []lyricRecord
	for _, r := range records {
		if matches(r, candidate) {
			matching = append(matching, r)
		}
	}
	if len(matching) == 0 {
		return nil
	}
	sort.SliceStable(matching, func(i, j int) bool {
		return normalized(matching[i].Album) == normalized(candidate.Album) && normalized(matching[j].Album) != normalized(candidate.Album)
	})
	// Even an album preference is not proof that two lyric variants are identical.
	candidate.Ambiguous = candidate.Ambiguous || len(matching) > 1 || candidate.Version != ""
	return applyLyrics(candidate, matching[0])
}
func applyLyrics(candidate *Candidate, r lyricRecord) error {
	text := strings.TrimSpace(r.Plain)
	if text == "" {
		text = stripLRC(r.Synced)
	}
	if len(text) > maxLyrics {
		return errLyricsTooLong
	}
	candidate.Lyrics = text
	candidate.Instrumental = r.Instrumental
	if r.Instrumental {
		candidate.Lyrics = ""
	}
	candidate.LRCLIBID = r.ID
	candidate.Source = "LRCLIB"
	candidate.SourceURL = fmt.Sprintf("https://lrclib.net/api/get/%d", r.ID)
	return nil
}

var lrcTime = regexp.MustCompile(`\[(?:[0-9]{1,3}:[0-9]{2}(?:[.:][0-9]{1,3})?)\]`)
var lrcMetadata = regexp.MustCompile(`(?i)^\s*\[(?:ar|ti|al|by|offset|length|re|ve):[^\]]*\]\s*$`)

func stripLRC(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if lrcMetadata.MatchString(line) {
			continue
		}
		out = append(out, lrcTime.ReplaceAllString(line, ""))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
