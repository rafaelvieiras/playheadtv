// Command server is the Playhead TV backend entrypoint: open the database,
// apply pending migrations, run the EPG materializer (once at boot, then in
// the background), and serve the REST API and the MCP surface
// (docs/ROADMAP.md Phase 2, internal/mcpserver) alongside a health check.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/mcpserver"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pullsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/internal/webui"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// defaultEPGWindow is how far ahead of "now" the materializer keeps epg_entry
// populated unless PLAYHEADTV_EPG_WINDOW says otherwise. A hosted deployment
// may want longer: clients cache the window locally and can play offline for
// about that long.
const defaultEPGWindow = 48 * time.Hour

// Bounds of an accepted PLAYHEADTV_EPG_WINDOW: below the minimum a client
// could not even ride out a short outage, above the maximum a full
// regeneration and every client download grow without a real use.
const (
	minEPGWindow = time.Hour
	maxEPGWindow = 30 * 24 * time.Hour
)

// epgWindowFromEnv parses PLAYHEADTV_EPG_WINDOW (a Go duration such as "72h"),
// returning the default for an empty, invalid or out-of-bounds value, plus a
// message to log in the latter two cases.
func epgWindowFromEnv(value string) (time.Duration, string) {
	if value == "" {
		return defaultEPGWindow, ""
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < minEPGWindow || d > maxEPGWindow {
		return defaultEPGWindow, "ignoring invalid PLAYHEADTV_EPG_WINDOW " + strconv.Quote(value) +
			" (want a duration between " + minEPGWindow.String() + " and " + maxEPGWindow.String() + ")"
	}
	return d, ""
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dbPath := envOrDefault("PLAYHEADTV_DB_PATH", "./playheadtv.db")
	addr := envOrDefault("PLAYHEADTV_ADDR", ":9394")

	db, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// The encryption key lives next to the database file so a backup of one
	// without the other is an incomplete backup either way — see
	// crypto.LoadOrGenerateKey for what happens on first run.
	keyPath := filepath.Join(filepath.Dir(dbPath), "encryption.key")
	encKey, err := crypto.LoadOrGenerateKey(keyPath)
	if err != nil {
		return err
	}

	// Migrations and the derived-data backfills run synchronously, before the
	// server listens, with their own generous budget: they are one-time
	// upgrade work whose duration grows with the catalog (batched, they take
	// seconds per 50k items, but a very large tenant or a slow disk must not
	// hit the short deadline below and crash-loop). Every step is resumable
	// and idempotent, so even a killed upgrade continues where it stopped.
	// Doing them before listening (rather than in the background) means no
	// request or materializer run ever sees half-filled search_text,
	// overview_norm or genre links.
	upgradeCtx, upgradeCancel := context.WithTimeout(context.Background(), upgradeBudget)
	defer upgradeCancel()
	if err := storage.RunMigrations(upgradeCtx, db, migrations.FS); err != nil {
		return err
	}
	log.Printf("storage: migrations applied (db=%s)", dbPath)
	if err := catalogsync.BackfillSearchText(upgradeCtx, db); err != nil {
		return err
	}
	if err := catalogsync.BackfillOverviewNorm(upgradeCtx, db); err != nil {
		return err
	}
	// Convert legacy {kind, genres, year_from, year_to} criteria of dynamic
	// blocks and pools into condition trees, holding back (and recording for
	// review) any whose matched set would change.
	if _, err := catalogsync.MigrateLegacyCriteria(upgradeCtx, db); err != nil {
		return err
	}
	// Move the query of every dynamic block, filler pool and criteria item into
	// a collection (owners with identical trees share one), holding back any
	// whose matched set would change. Batched and idempotent like the
	// backfills above; rows still held for review stay held.
	if _, err := catalogsync.MigrateToCollections(upgradeCtx, db); err != nil {
		return err
	}

	// Blocks the list_mode migration switched to repeat while scheduled at
	// fixed times: logged for review (also served by GET /blocks/list-mode-review).
	if err := api.LogListModeReview(upgradeCtx, db); err != nil {
		return err
	}

	// The short deadline covers only the remaining, non-upgrade startup work.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// TODO: multi-tenant once Auth exists. Until then every handler in
	// internal/api operates on this one implicit tenant.
	err = db.Exec(ctx,
		"INSERT OR IGNORE INTO tenant (id, name, tier, created_at) VALUES (?, ?, ?, ?)",
		api.DefaultTenantID, "Default", string(domain.TierSelfHosted), time.Now().UnixMilli(),
	)
	if err != nil {
		return err
	}

	// Opt-in dev/debug seed: only fires when both vars are set (never true
	// for a real cloud-sync tenant's environment) and only if the tenant has
	// no app_user yet — see api.SeedAdminUser's doc comment.
	if seedEmail := os.Getenv("PLAYHEADTV_SEED_ADMIN_EMAIL"); seedEmail != "" {
		seedPassword := os.Getenv("PLAYHEADTV_SEED_ADMIN_PASSWORD")
		if seedPassword == "" {
			return errors.New("PLAYHEADTV_SEED_ADMIN_EMAIL is set but PLAYHEADTV_SEED_ADMIN_PASSWORD is not")
		}
		if err := api.SeedAdminUser(ctx, db, seedEmail, seedPassword); err != nil {
			return err
		}
		log.Printf("api: PLAYHEADTV_SEED_ADMIN_EMAIL is set — seeded/kept dev admin user %s (never set this in a real deployment)", seedEmail)
	}

	epgWindow, windowWarning := epgWindowFromEnv(os.Getenv("PLAYHEADTV_EPG_WINDOW"))
	if windowWarning != "" {
		log.Printf("materializer: %s", windowWarning)
	}
	mat := materializer.New(db, epgWindow)
	// PLAYHEADTV_EPG_FREEZE_MARGIN (a Go duration, e.g. "3h") overrides the
	// default freeze margin; an invalid value is logged and ignored.
	if v := os.Getenv("PLAYHEADTV_EPG_FREEZE_MARGIN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			mat.SetFreezeMargin(d)
		} else {
			log.Printf("materializer: ignoring invalid PLAYHEADTV_EPG_FREEZE_MARGIN %q", v)
		}
	}
	// Run once synchronously at boot so GET /epg has something to serve as
	// soon as the server comes up, without blocking startup on a broken
	// schedule forever — a failure here is logged, not fatal.
	if err := mat.Run(ctx); err != nil {
		log.Printf("materializer: initial run failed: %v", err)
	}
	matCtx, matCancel := context.WithCancel(context.Background())
	defer matCancel()
	mat.Start(matCtx)

	// Unlike mat above, sync is NOT run synchronously here: SyncOne is a
	// real network round trip to a Jellyfin server, which shouldn't be
	// allowed to delay startup on a slow/unreachable one. Start's own
	// goroutine runs the first sweep immediately, just not blocking this
	// function.
	sync := pullsync.New(db, encKey, mat)
	syncCtx, syncCancel := context.WithCancel(context.Background())
	defer syncCancel()
	sync.Start(syncCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	api.Register(mux, db, mat, sync, encKey)
	// MCP surface (docs/ROADMAP.md Phase 2): same db/mat/encKey as the REST
	// API above, mounted at /mcp. It falls into RequireAuth's default
	// session-only branch by exclusion (see internal/mcpserver's package
	// comment) — no auth.go change needed.
	mcpserver.Register(mux, db, mat, encKey)

	webuiDist, err := fs.Sub(webui.FS, "dist")
	if err != nil {
		return err
	}
	// The admin SPA (Phase 3, webui/) is served under /admin/ by the same
	// binary — no separate Node server, no CORS, same origin as the API.
	// isOpenPath in internal/api/auth.go makes GET /admin/* public so the
	// static shell (including the login screen) loads before any session
	// exists; the API calls the SPA makes still go through RequireAuth as
	// usual.
	mux.Handle("/admin/", http.StripPrefix("/admin/", spaFileServer(webuiDist)))

	srv := &http.Server{Addr: addr, Handler: api.RequireAuth(db, mux)}

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		return err
	case <-stop:
		log.Print("shutting down")
		matCancel()
		syncCancel()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// upgradeBudget bounds the one-time upgrade work at startup (migrations and
// backfills), independent of the short deadline of the rest of startup.
const upgradeBudget = 30 * time.Minute

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// spaFileServer serves fsys (the SPA's dist/ directory, already stripped of
// the /admin/ prefix by the caller) and falls back to index.html for any
// path that doesn't match a real file — so a client-side route like
// /admin/channels still works on a hard reload, and React Router takes over
// from there.
func spaFileServer(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "."
		}
		if _, err := fs.Stat(fsys, p); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
