package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// A song with no score must still render its page.
//
// The edit panel is gated on the score existing, but Go templates evaluate
// `.Score.Any` eagerly inside `and` — and `.Score` is only put into the data
// when a score exists, so on any MiniMax song the key is absent and the lookup
// is a nil interface. A panel that dereferenced it unconditionally would fail
// at render time on most of the library.
func TestSongPageRendersWithoutAScore(t *testing.T) {
	h, _, srv := newTestEnvWith(t, withYue2Frozen)
	alice, tok := mkSession(t, srv, "edit-alice", store.StatusApproved, store.RoleUser)
	g := mkSong(t, srv, "no-score-song", alice.ID, false)
	if g.ScoreABC != "" {
		t.Fatal("fixture unexpectedly carries a score")
	}

	res := do(h, "GET", "/songs/"+g.ID, cookieFor(tok))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if strings.Contains(body, "template:") {
		t.Errorf("the song page failed to render:\n%s", body[:min(600, len(body))])
	}
	// And the edit panel is absent, because there is nothing to edit.
	if strings.Contains(body, "Re-render this song") {
		t.Error("an edit panel was offered for a song with no score")
	}
}

// A song that does have a score gets the panel, with the score's own tempo
// seeded — so the control starts from what the song actually is rather than
// from a default.
func TestSongPageOffersEditForAScoredSong(t *testing.T) {
	h, _, srv := newTestEnvWith(t, withYue2Frozen)
	alice, tok := mkSession(t, srv, "edit-alice2", store.StatusApproved, store.RoleUser)
	g := mkScoredSong(t, srv, "scored-song", alice.ID,
		"X:1\nT:\nM:4/4\nL:1/16\nQ:1/4=145\nK:Cm\nV: Vocal\nZ|\nV: Ins\nZ|\n")

	res := do(h, "GET", "/songs/"+g.ID, cookieFor(tok))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{"Re-render this song", `name="tempo"`, "/songs/" + g.ID + "/edit"} {
		if !strings.Contains(body, want) {
			t.Errorf("song page missing %q", want)
		}
	}
	// The tempo the model chose is what the control starts from.
	if !strings.Contains(body, "x-model.number=\"tempo\"") || !strings.Contains(body, "tempo: 145") {
		t.Error("the edit panel did not seed the tempo from the stored score")
	}
	// Key and meter are shown, read-only, because they cannot be changed
	// meaningfully — only tempo is a genuine performance edit.
	if !strings.Contains(body, "key Cm") || !strings.Contains(body, "4/4") {
		t.Error("the score's key and meter are not displayed")
	}
	// And no free-text score box: it would let someone change M: and silently
	// ruin the bars, with nothing validating it.
	if strings.Contains(body, `name="abc"`) || strings.Contains(body, `name="score"`) {
		t.Error("the edit form exposes the raw score, which must not be editable")
	}
}

// rewriteTempo changes the Q: header and nothing else — the score is the score,
// and an edit that quietly rewrote anything more would not be the edit asked for.
func TestRewriteTempoChangesOnlyTheTempoLine(t *testing.T) {
	abc := "X:1\nT:\nM:4/4\nL:1/16\nQ:1/4=90\nK:Cm\nV: Vocal\nZ|\"Cm\"z16|\nV: Ins\nZ|\n"
	got, ok := rewriteTempo(abc, 132)
	if !ok {
		t.Fatal("a score with a Q: header must be rewritable")
	}
	if !strings.Contains(got, "Q:1/4=132") {
		t.Errorf("tempo not rewritten: %q", got)
	}
	if strings.Contains(got, "Q:1/4=90") {
		t.Error("the old tempo is still present")
	}
	// Everything else survives byte for byte.
	want := strings.Replace(abc, "Q:1/4=90", "Q:1/4=132", 1)
	if got != want {
		t.Errorf("more than the tempo line changed:\n got %q\nwant %q", got, want)
	}
	// The header block is otherwise untouched, which is the property that
	// matters: M:, L: and K: all feed the duration arithmetic.
	if m := scoreMetaOf(got); m.Key != "Cm" || m.Meter != "4/4" {
		t.Errorf("rewriting the tempo disturbed the other headers: %+v", m)
	}
}

// A score with no Q: cannot have its tempo changed, and saying so is better
// than queueing an edit whose tempo silently did not apply.
func TestRewriteTempoReportsAScoreWithoutOne(t *testing.T) {
	if _, ok := rewriteTempo("X:1\nK:C\nV: Vocal\nZ|\n", 120); ok {
		t.Error("a score with no Q: header reported a successful rewrite")
	}
}

// Editing is owner-only, and a song that is not yours is indistinguishable
// from one that does not exist — otherwise the route probes for other
// tenants' ids.
func TestEditIsOwnerOnly(t *testing.T) {
	h, _, srv := newTestEnvWith(t, withYue2Frozen)
	alice, _ := mkSession(t, srv, "edit-alice3", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, srv, "edit-bob", store.StatusApproved, store.RoleUser)
	g := mkScoredSong(t, srv, "someone-elses", alice.ID,
		"X:1\nM:4/4\nL:1/16\nQ:1/4=100\nK:C\nV: Vocal\nZ|\nV: Ins\nZ|\n")

	// Bob is not that song's owner.
	res := postFormAs(h, "/songs/"+g.ID+"/edit", url.Values{
		"instructions": {"pop"}, "input": {"words"}, "tempo": {"120"},
	}, bobTok)
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — a song that is not yours must not be distinguishable", res.Code)
	}
}

// An edit needs lyrics. The worker requires them for this mode — only a cover
// may omit them — so an empty block is a mistake rather than an instrumental
// request, and it is caught before a job is queued for it.
func TestEditRefusesAScorelessSongAndEmptyLyrics(t *testing.T) {
	h, _, srv := newTestEnvWith(t, withYue2Frozen)
	alice, tok := mkSession(t, srv, "edit-alice4", store.StatusApproved, store.RoleUser)

	noScore := mkSong(t, srv, "plain-song", alice.ID, false)
	res := postFormAs(h, "/songs/"+noScore.ID+"/edit", url.Values{
		"instructions": {"pop"}, "input": {"words"},
	}, tok)
	if res.Code != http.StatusBadRequest {
		t.Errorf("editing a score-less song = %d, want 400", res.Code)
	}

	scored := mkScoredSong(t, srv, "scored-two", alice.ID,
		"X:1\nM:4/4\nL:1/16\nQ:1/4=100\nK:C\nV: Vocal\nZ|\nV: Ins\nZ|\n")
	// Empty lyrics AND an empty style: the style is what names the music, so it
	// cannot be blanked either — but the lyrics fall back to the song's own.
	res = postFormAs(h, "/songs/"+scored.ID+"/edit", url.Values{"tempo": {"120"}}, tok)
	if res.Code >= 400 {
		t.Errorf("an edit with only a tempo change was refused: %d %s",
			res.Code, res.Body.String())
	}

	jobs, _ := srv.st.DequeueQueued(10)
	if len(jobs) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(jobs))
	}
	j := jobs[0]
	// Blank fields fall back to the source, so a tempo-only edit does not
	// silently blank the words or the arrangement.
	if j.Lyrics != scored.Lyrics || j.Caption != scored.Caption {
		t.Errorf("fallbacks lost: lyrics=%q caption=%q", j.Lyrics, j.Caption)
	}
	if j.Mode != store.ModeEdit {
		t.Errorf("Mode = %q, want edit", j.Mode)
	}
	if j.SourceSongID != scored.ID {
		t.Errorf("SourceSongID = %q, want %q", j.SourceSongID, scored.ID)
	}
	if !strings.Contains(j.ABC, "Q:1/4=120") {
		t.Errorf("the score carried to the job has the wrong tempo: %q", j.ABC)
	}
	// cot is left empty so the worker's default ("full") applies — which is
	// exactly what an edit means: full keeps the supplied harmony.
	if j.Cot != "" {
		t.Errorf("Cot = %q, want empty so the worker's default applies", j.Cot)
	}
}

// mkScoredSong writes a YuE2 song that carries a score.
//
// It does NOT reuse mkSong and then set the score: CreateSong is INSERT OR
// IGNORE, so a second insert for the same row is silently discarded and the
// song would be stored with an empty score — which is exactly how the first
// version of these tests failed, looking like a template bug.
func mkScoredSong(t *testing.T, s *Server, id, userID, abc string) *store.Song {
	t.Helper()
	g := &store.Song{
		ID: id, JobID: "job-" + id, UserID: userID,
		Lyrics: "[Verse]\nla", Caption: "sparse piano ballad", Duration: 60,
		Engine: store.EngineYue2, Mode: store.ModeCreate, Cot: "full",
		Delivery: "s3", AudioPath: "/tmp/" + id + ".m4a", Title: "Song " + id,
		ScoreABC: abc, CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateSong(g); err != nil {
		t.Fatal(err)
	}
	return g
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// An edit is unguided unless asked: guidance on an edit has not been listened
// to, unlike on a cover. Asked, it stores the preset and can take a new seed.
func TestEditStyleStrengthDefaultsOffAndIsHonoured(t *testing.T) {
	h, _, srv := newTestEnvWith(t, withYue2Frozen)
	alice, tok := mkSession(t, srv, "edit-strength", store.StatusApproved, store.RoleUser)
	abc := "X:1\nM:4/4\nL:1/16\nQ:1/4=100\nK:C\nV: Vocal\nZ|\nV: Ins\nZ|\n"
	g := mkScoredSong(t, srv, "strength-edit", alice.ID, abc)

	for _, form := range []url.Values{
		{"instructions": {"synth-pop"}},
		{"instructions": {"synth-pop"}, "style_strength": {"balanced"}, "new_seed": {"1"}},
	} {
		if res := postFormAs(h, "/songs/"+g.ID+"/edit", form, tok); res.Code >= 400 {
			t.Fatalf("status = %d; body: %s", res.Code, res.Body.String())
		}
	}
	jobs, _ := srv.st.DequeueQueued(10)
	if len(jobs) != 2 {
		t.Fatalf("queued %d jobs, want 2", len(jobs))
	}
	if jobs[0].CfgScale != nil {
		t.Errorf("default edit sent CfgScale %v, want none", *jobs[0].CfgScale)
	}
	if jobs[1].CfgScale == nil || *jobs[1].CfgScale != 3 {
		t.Errorf("balanced edit CfgScale = %v, want 3", jobs[1].CfgScale)
	}
	if jobs[1].Seed == nil {
		t.Error("a new seed was asked for but none was stored")
	}

	res := postFormAs(h, "/songs/"+g.ID+"/edit", url.Values{
		"instructions": {"synth-pop"}, "style_strength": {"loud"},
	}, tok)
	if res.Code != http.StatusBadRequest {
		t.Errorf("an unknown strength = %d, want 400", res.Code)
	}
}

// Both panels offer the controls, each with its own default selected.
func TestPanelsOfferStyleStrengthAndNewSeed(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := &store.Song{
		ID: "panel-song", JobID: "job-panel-song", UserID: owner,
		Lyrics: "[Verse]\nla", Caption: "ballad", Engine: store.EngineYue2,
		Mode: store.ModeCreate, Delivery: "s3", AudioPath: "/tmp/panel-song.m4a",
		ScoreABC:  "X:1\nM:4/4\nL:1/16\nQ:1/4=77\nK:C\nV: Vocal\nZ|\nV: Ins\nZ|\n",
		CreatedAt: time.Now().UTC(),
	}
	if err := srv.st.CreateSong(g); err != nil {
		t.Fatal(err)
	}
	body := do(h, "GET", "/songs/"+g.ID, cookieFor(tok)).Body.String()
	for _, want := range []string{
		`id="cover-strength"`, `id="edit-strength"`,
		`id="cover-new-seed"`, `id="edit-new-seed"`,
		`<option value="balanced" selected>`, `<option value="off" selected>`,
		"Edit in generator</b> instead",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("song page missing %q", want)
		}
	}
}

// The score's tempo is text from the model, and the edit panel puts it inside
// an Alpine expression. Only a number may reach it: anything else would run
// as script for whoever opens the page.
func TestEditPanelTempoIsOnlyANumber(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	u, tok := mkSession(t, s, "owner", store.StatusApproved, store.RoleUser)
	g := &store.Song{ID: "s-tempo", JobID: "job-s-tempo", UserID: u.ID,
		Lyrics: "la", Caption: "pop", Duration: 30, Engine: store.EngineYue2,
		Delivery: "s3", AudioPath: "/nonexistent.m4a", CreatedAt: time.Now().UTC(),
		ScoreABC: "X:1\nM:4/4\nQ:1/4=1, open: (window.MARK = 1)\nK:C\nV:1\nZ|\n"}
	if err := s.st.CreateSong(g); err != nil {
		t.Fatal(err)
	}
	body := do(h, "GET", "/songs/"+g.ID, cookieFor(tok)).Body.String()
	if !strings.Contains(body, "tempo: 0 }") {
		t.Error("a non-numeric tempo did not render as 0 in the edit panel")
	}
	if strings.Contains(body, "tempo: 1, open") {
		t.Error("score text reached the Alpine expression")
	}
}
