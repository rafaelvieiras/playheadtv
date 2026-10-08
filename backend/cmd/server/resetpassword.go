package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// resetPasswordPath is the web UI page that accepts the token. The token goes
// in the URL fragment, which a browser never sends to the server, so no access
// log or proxy sees it.
const resetPasswordPath = "/admin/reset-password"

// runResetPassword is `server reset-password [--email <address>] [--ttl <d>]`.
//
// It opens its own short-lived connection to the same SQLite file the server
// uses (PLAYHEADTV_DB_PATH) instead of talking to the running server. SQLite
// in WAL mode lets a second process write beside the server's single
// connection: storage.Open sets a busy timeout so either side waits for the
// other's short write. The server's pool stays at one connection. Going
// through the file also works with the server stopped, and needs no admin
// endpoint or credential, which is the point of a recovery path: whoever can
// reach the data directory (docker exec, the host) is the operator. The
// command never creates a database: a missing file is an error. It applies
// pending migrations only when the password_reset table is absent (a database
// never started by this version), which the server itself would do at boot.
//
// The token goes to stdout only, never to the log.
func runResetPassword(args []string, dbPath string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "email of the user (optional while there is exactly one)")
	ttl := fs.Duration("ttl", api.DefaultResetTTL,
		fmt.Sprintf("how long the token stays valid (%s to %s)", api.MinResetTTL, api.MaxResetTTL))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database not found at %s (set PLAYHEADTV_DB_PATH): %w", dbPath, err)
	}

	db, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var tables []string
	if err := db.Query(ctx, &tables,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'password_reset'"); err != nil {
		return err
	}
	if len(tables) == 0 {
		if err := storage.RunMigrations(ctx, db, migrations.FS); err != nil {
			return err
		}
	}

	reset, err := api.CreatePasswordReset(ctx, db, *email, *ttl)
	if err != nil {
		return errors.New("reset-password: " + err.Error())
	}
	fmt.Fprintf(stdout, "Password reset token for %s (valid until %s, single use):\n\n  %s\n\n",
		reset.Email, reset.ExpiresAt.Format(time.RFC3339), reset.Token)
	fmt.Fprintf(stdout, "Open <your server address>%s#token=%s\nor paste the token into the page.\n",
		resetPasswordPath, reset.Token)
	fmt.Fprintln(stdout, "Running the command again replaces this token. The old password is not shown or kept.")
	return nil
}
