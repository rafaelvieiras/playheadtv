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
	"strings"
	"syscall"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/mcpserver"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pullsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/internal/webui"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// epgWindow is how far ahead of "now" the materializer keeps epg_entry
// populated (docs/ROADMAP.md's EPG materializer bullet default).
const epgWindow = 48 * time.Hour

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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
		return err
	}
	log.Printf("storage: migrations applied (db=%s)", dbPath)

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

	mat := materializer.New(db, epgWindow)
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
