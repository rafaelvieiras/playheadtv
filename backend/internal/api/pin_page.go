// Pin page: the human-facing half of device pairing. GET /pin serves a tiny
// server-rendered form (html/template, no JS, no external requests) where the
// user types the PIN shown on the device and signs in with their app_user
// credentials; it posts to POST /pin/approve. See docs/ARCHITECTURE.md and
// pairing.go for the rest of the handshake. Everything is escaped by
// html/template; the optional ?code= prefill is normalised first.
package api

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

const pinPageCSP = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'"

// Inline CSS only: the page must render with zero external requests, and CSP
// forbids anything but 'unsafe-inline' styles.
const pinPageCSS = `body{font-family:system-ui,sans-serif;margin:0;padding:1.5rem;background:#111;color:#eee}
main{max-width:24rem;margin:0 auto}
h1{font-size:1.4rem}
p{line-height:1.4}
label{display:block;margin:.9rem 0 .25rem}
input{width:100%;box-sizing:border-box;padding:.6rem;font-size:1rem;border-radius:.4rem;border:1px solid #555;background:#1c1c1c;color:#eee}
button{width:100%;margin-top:1.25rem;padding:.7rem;font-size:1rem;border-radius:.4rem;border:0;background:#3b82f6;color:#fff}
.error{color:#f87171}`

// The PIN value is pre-filled from ?code= (the QR link); every field is
// escaped by the template. The Jellyfin fields only appear while the server
// still has no Jellyfin config (readJellyfinConfig reports found == false).
var pinPageTemplate = template.Must(template.New("pin").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pair a device</title>
<style>` + pinPageCSS + `</style>
</head>
<body>
<main>
<h1>Pair a device</h1>
<p>Enter the PIN shown on your device, then sign in to approve it.</p>
<form method="post" action="/pin/approve">
<label for="pin">PIN</label>
<input id="pin" name="pin" value="{{.Code}}" autocapitalize="characters" autocomplete="off" required>
<label for="email">E-mail</label>
<input id="email" name="email" type="email" autocomplete="username" required>
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="current-password" required>
{{if .ShowJellyfin}}
<label for="jellyfin_url">Jellyfin URL</label>
<input id="jellyfin_url" name="jellyfin_url" type="url" placeholder="https://example.com" required>
<label for="jellyfin_api_key">Jellyfin API key</label>
<input id="jellyfin_api_key" name="jellyfin_api_key" type="password" autocomplete="off" required>
{{end}}
<button type="submit">Approve</button>
</form>
</main>
</body>
</html>
`))

// The result page after a form submit: success or a generic failure. It never
// echoes any submitted value, so a password or PIN cannot leak into the HTML.
var pinResultTemplate = template.Must(template.New("pin-result").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>` + pinPageCSS + `</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
</main>
</body>
</html>
`))

// setPinPageHeaders applies the headers the contract requires on every HTML
// response of the pairing flow.
func setPinPageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", pinPageCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Type", "text/html; charset=utf-8")
}

// formatPairingPinDisplay renders a normalised PIN in the human-facing
// XXXX-XXXX form. Any other length is returned unchanged, so a hostile ?code=
// cannot smuggle markup: the template escapes whatever comes through.
func formatPairingPinDisplay(pin string) string {
	if len(pin) != 8 {
		return pin
	}
	return pin[:4] + "-" + pin[4:]
}

// wantsHTML reports whether POST /pin/approve should answer with the HTML
// result page instead of JSON. Only the plain form a browser submits does:
// it is form-encoded and its Accept header asks for text/html.
func wantsHTML(r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// writePinResultPage renders the success/error page. approvePairing calls it
// for a form-encoded request so the browser sees a readable result.
func writePinResultPage(w http.ResponseWriter, status int, title, message string) {
	setPinPageHeaders(w)
	w.WriteHeader(status)
	if err := pinResultTemplate.Execute(w, struct {
		Title   string
		Message string
	}{Title: title, Message: message}); err != nil {
		logging.Errorf("api: render pin result page: %v", err)
	}
}

// pinPageHandler is GET /pin. It never fails on a missing or malformed code:
// the form just renders with whatever prefill survived normalisation.
func pinPageHandler(db storage.DB, encKey [32]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, found, err := readJellyfinConfig(r.Context(), db, encKey)
		if err != nil {
			// Generic page: a storage error must not surface details or secrets.
			writePinResultPage(w, http.StatusInternalServerError,
				"Something went wrong", "The pairing page could not be loaded. Please try again.")
			return
		}
		data := struct {
			Code         string
			ShowJellyfin bool
		}{
			Code:         formatPairingPinDisplay(normalizePairingPin(r.URL.Query().Get("code"))),
			ShowJellyfin: !found,
		}
		setPinPageHeaders(w)
		w.WriteHeader(http.StatusOK)
		if err := pinPageTemplate.Execute(w, data); err != nil {
			logging.Errorf("api: render pin page: %v", err)
		}
	}
}
