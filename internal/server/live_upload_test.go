//go:build live

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// TestLiveUploadCover exercises the upload path against the running app.
//
// The library path is already proven: a 6.9 MB song was served byte-for-byte
// and the worker covered it. What this proves is the *other* branch of
// resolveCoverSource — a file arriving as multipart, being staged under a
// random name, and coming back out through a freshly minted link.
//
// It talks to the real HTTP endpoint rather than calling the handler, so
// routing, multipart parsing, the session check and the response are all
// exercised. Run it inside the app container, where the database, the data
// volume and the reachable origin all are.
//
// READ THIS BEFORE CHANGING IT. The probe posts through the app, which queues a
// job — and the app's own background worker will then submit that job to RunPod
// like any other. An earlier version ALSO submitted the same recording directly,
// to check the worker could fetch the link. That produced two covers from one
// recording, both billed:
//
//	probe's direct submit  27059ff4-...-u1  completed
//	app worker's submit    6451bd72-...-u2  completed
//
// So the direct submission is gone. The worker's ability to fetch a signed link
// is already established by TestLiveCoverFetch (library path) and by the
// earlier reachability experiment; re-proving it here bought nothing and cost a
// GPU run. One probe, one job, one bill.
func TestLiveUploadCover(t *testing.T) {
	appURL := os.Getenv("PROBE_APP_URL")
	if appURL == "" {
		// The probe runs in its own container, so 127.0.0.1 is the probe, not
		// the app. Compose puts both on the shared network, where the service
		// answers to its name.
		appURL = "http://mm3-app:8080"
	}
	dbPath := os.Getenv("MM3_DB_PATH")
	if dbPath == "" {
		dbPath = "/data/mm3.db"
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("no database at %s — run this inside the app container", dbPath)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("opening %s: %v", dbPath, err)
	}
	// Closed via t.Cleanup, NOT defer, and registered FIRST so it runs LAST.
	//
	// Cleanups are LIFO, and t.Cleanup runs *after* the test function returns —
	// which is after any deferred call. A `defer st.Close()` therefore closed
	// the database before the fixture cleanups below ran, so every delete was
	// executed against a closed handle and silently failed, leaving a probe
	// account, a probe song and a staged upload behind. The probe still passed:
	// nothing asserted the cleanup had worked.
	t.Cleanup(func() { _ = st.Close() })

	// Everything this probe needs is created for it and removed afterwards.
	//
	// It deliberately does NOT borrow a real song: reassigning an existing
	// song's owner, or minting a session against a real account, would be
	// mutating live data to suit a test. A throwaway song owned by a throwaway
	// account exercises exactly the same code path with nothing at stake.
	admin := store.Access{Admin: true}

	// A real audio file to upload. Read-only: it is a fixture, not something
	// the probe changes.
	songs, err := st.Songs(200, 0, admin)
	if err != nil {
		t.Fatalf("listing songs: %v", err)
	}
	var uploadPath string
	for _, s := range songs {
		if s.AudioPath == "" {
			continue
		}
		if fi, err := os.Stat(s.AudioPath); err == nil && fi.Size() > 100_000 {
			uploadPath = s.AudioPath
			break
		}
	}
	if uploadPath == "" {
		t.Skip("no audio file on disk to use as an upload")
	}
	want, err := os.ReadFile(uploadPath)
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	t.Logf("uploading %s (%d bytes)", filepath.Base(uploadPath), len(want))

	// A throwaway account and a throwaway YuE2 song, both cleaned up.
	owner := &store.User{
		ID: newUserID(), Username: "cover-probe-" + fmt.Sprint(time.Now().UnixNano()),
		PasswordHash: "$2a$04$" + strings.Repeat("x", 53),
		Status:       store.StatusApproved, Role: store.RoleUser,
	}
	if err := st.CreateUser(owner); err != nil {
		t.Fatalf("creating the probe account: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteUserRow(owner.ID) })

	songID := newJobIDForProbe()
	audioDir := os.Getenv("MM3_AUDIO_DIR")
	if audioDir == "" {
		audioDir = "/data/audio"
	}
	// A copy of the fixture, so deleting the probe song cannot touch the
	// original it was copied from.
	probeAudio := filepath.Join(audioDir, songID+".m4a")
	if err := os.WriteFile(probeAudio, want, 0o640); err != nil {
		t.Fatalf("staging probe audio: %v", err)
	}
	song := &store.Song{
		ID: songID, JobID: "job-" + songID, UserID: owner.ID,
		Lyrics: "[Verse]\nla", Caption: "probe fixture", Duration: 60,
		Engine: store.EngineYue2, Mode: store.ModeCreate, Cot: "full",
		Delivery: "s3", AudioPath: probeAudio, Title: "Cover probe",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateSong(song); err != nil {
		t.Fatalf("creating the probe song: %v", err)
	}
	t.Cleanup(func() {
		_ = st.DeleteSongRow(songID)
		_ = os.Remove(probeAudio)
	})
	t.Logf("created throwaway song %s owned by %s", songID, owner.Username)

	// --- a session for the probe account, minted the way a sign-in would ---
	token, err := store.NewSessionToken()
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}
	now := time.Now().UTC()
	if err := st.CreateSession(token, &store.Session{
		UserID: owner.ID, Username: owner.Username,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("creating a session: %v", err)
	}

	// --- the upload, as a browser sends it ---
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("instructions", "live probe: sparse piano, slow")
	part, err := mw.CreateFormFile("source_upload", filepath.Base(uploadPath))
	if err != nil {
		t.Fatalf("building the multipart body: %v", err)
	}
	if _, err := part.Write(want); err != nil {
		t.Fatalf("writing the upload: %v", err)
	}
	_ = mw.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		appURL+"/songs/"+song.ID+"/cover", &buf)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "mm3_session", Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("posting the cover: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	t.Logf("app answered %d", resp.StatusCode)
	if resp.StatusCode >= 400 {
		t.Fatalf("the upload was refused: %s", body)
	}

	// --- what did the handler queue? ---
	jobs, err := st.DequeueQueued(50)
	if err != nil {
		t.Fatalf("reading the queue: %v", err)
	}
	var job *store.Job
	for _, j := range jobs {
		if j.Mode == store.ModeCover && j.SourceAudio != "" {
			job = j
		}
	}
	if job == nil {
		t.Fatal("no cover job was queued")
	}
	t.Logf("queued job %s mode=%s source=%s", job.ID, job.Mode, maskLink(job.SourceAudio))
	if !strings.Contains(job.SourceAudio, "/signed/") {
		t.Fatalf("the upload did not become a signed link: %s", maskLink(job.SourceAudio))
	}
	// An uploaded recording is not one of ours, so this must stay empty.
	if job.SourceSongID != "" {
		t.Errorf("SourceSongID = %q, want empty for an uploaded recording", job.SourceSongID)
	}

	// --- and does that link serve the bytes we uploaded? ---
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer scancel()
	sreq, _ := http.NewRequestWithContext(sctx, http.MethodGet, job.SourceAudio, nil)
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatalf("fetching the minted link: %v", err)
	}
	got, _ := io.ReadAll(sresp.Body)
	sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("the minted link answered %d", sresp.StatusCode)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the link served %d bytes, %d were uploaded", len(got), len(want))
	}
	t.Logf("the minted link served the uploaded %d bytes byte-for-byte", len(got))

	// Done. The job is queued and the app's worker will run it; asserting on
	// the cover's own result would mean waiting on a GPU for something this
	// probe is not about. The upload path ends at "a fetchable link carrying
	// the right bytes, queued as a cover", and that is what is asserted above.
	t.Logf("PROVEN: an uploaded recording became a fetchable signed link and was queued as mode=cover")
}

// maskLink hides the token, which is a bearer credential for that file.
func maskLink(u string) string {
	i := strings.Index(u, "/signed/")
	if i < 0 {
		return u
	}
	return u[:i] + "/signed/<token>"
}

// newJobIDForProbe mints an id in the same shape the app uses, so a probe row
// looks like any other to everything that reads it.
func newJobIDForProbe() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "probe" + fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
