// Command probeclean deletes one account through the same code path the admin
// screen uses: store.DeleteUser, then unlink the files it returns.
//
// It exists because the admin UI needs credentials this shell does not have,
// and the deletion should still go through the real implementation rather than
// a hand-written SQL script that could drift from it.
//
//	go run ./cmd/probeclean -db /data/mm3.db -user cover-probe-...
//
// Dry by default: it prints what would go and exits. Pass -apply to commit.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

func main() {
	db := flag.String("db", "/data/mm3.db", "path to the sqlite database")
	user := flag.String("user", "", "username to delete")
	apply := flag.Bool("apply", false, "actually delete; without it this is a dry run")
	flag.Parse()

	if *user == "" {
		fmt.Fprintln(os.Stderr, "probeclean: -user is required")
		os.Exit(2)
	}

	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probeclean: opening %s: %v\n", *db, err)
		os.Exit(1)
	}
	defer st.Close()

	u, err := st.GetUserByUsername(*user)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probeclean: looking up %q: %v\n", *user, err)
		os.Exit(1)
	}
	if u == nil {
		fmt.Printf("probeclean: no account named %q — nothing to do\n", *user)
		return
	}
	fmt.Printf("account : %s  id=%s  status=%s role=%s\n", u.Username, u.ID, u.Status, u.Role)

	songs, err := st.Songs(500, 0, store.Access{Admin: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "probeclean: listing songs: %v\n", err)
		os.Exit(1)
	}
	var owned []string
	for _, s := range songs {
		if s.UserID == u.ID {
			owned = append(owned, s.ID)
		}
	}
	fmt.Printf("songs   : %d %v\n", len(owned), owned)

	uploads, err := st.CoverUploadsByUser(u.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probeclean: listing uploads: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("uploads : %d %v\n", len(uploads), uploads)

	if !*apply {
		fmt.Println("\nDRY RUN — nothing changed. Re-run with -apply to delete.")
		return
	}

	paths, err := st.DeleteUser(u.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probeclean: deleting: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\ndeleted account; %d file(s) to unlink\n", len(paths))
	var failed int
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "  FAILED %s: %v\n", p, err)
			failed++
			continue
		}
		fmt.Printf("  removed %s\n", filepath.Base(p))
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "probeclean: %d file(s) could not be removed\n", failed)
		os.Exit(1)
	}
	fmt.Println("done")
}
