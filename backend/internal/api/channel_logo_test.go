package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func logoBody(image []byte) string {
	return `{"image_base64":"` + base64.StdEncoding.EncodeToString(image) + `"}`
}

// TestChannelLogoLifecycle covers the whole logo resource: a channel starts
// without one, an upload names a new image that is served immutably, a second
// upload replaces it (old id gone), and a clear puts the channel back to its
// name.
func TestChannelLogoLifecycle(t *testing.T) {
	db, mux := newTestDB(t)
	newEPGSeed(t, db)

	// A fresh channel has none, and the channel itself advertises none.
	rec := doRequest(t, mux, "GET", "/channels/c1/logo", "")
	if rec.Code != 200 {
		t.Fatalf("fresh logo: %d %s", rec.Code, rec.Body.String())
	}
	var logo map[string]any
	json.Unmarshal(rec.Body.Bytes(), &logo)
	if logo["image_id"] != "" || logo["channel_id"] != "c1" {
		t.Fatalf("fresh logo = %v", logo)
	}
	if rec := doRequest(t, mux, "GET", "/channels/c1", ""); !strings.Contains(rec.Body.String(), `"logo_image_id":null`) {
		t.Errorf("a channel without a logo does not say so: %s", rec.Body.String())
	}

	// Upload.
	rec = doRequest(t, mux, "PUT", "/channels/c1/logo", logoBody(tinyPNG))
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &logo)
	id, _ := logo["image_id"].(string)
	if id == "" || logo["content_type"] != "image/png" {
		t.Fatalf("stored = %v", logo)
	}
	if strings.Contains(rec.Body.String(), "image_base64") {
		t.Errorf("the response carries the image: %s", rec.Body.String())
	}

	// The channel now names the image, which is what the client builds the URL
	// from.
	rec = doRequest(t, mux, "GET", "/channels/c1", "")
	if !strings.Contains(rec.Body.String(), `"logo_image_id":"`+id+`"`) {
		t.Errorf("the channel does not name its logo: %s", rec.Body.String())
	}

	// The bytes are served, cached forever and revalidated by ETag.
	img := doRequest(t, mux, "GET", "/channel-logos/"+id+"/image", "")
	if img.Code != 200 || img.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(img.Header().Get("Cache-Control"), "immutable") ||
		img.Header().Get("X-Content-Type-Options") != "nosniff" ||
		img.Header().Get("Content-Security-Policy") == "" ||
		img.Body.Len() != len(tinyPNG) {
		t.Fatalf("image: %d %v len=%d", img.Code, img.Header(), img.Body.Len())
	}
	conditional := httptest.NewRequest("GET", "/channel-logos/"+id+"/image", nil)
	conditional.Header.Set("Authorization", "Bearer "+testSessionToken)
	conditional.Header.Set("If-None-Match", img.Header().Get("ETag"))
	if notModified := serveRequest(mux, conditional); notModified.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d, want 304", notModified.Code)
	}

	// A new upload replaces the image and gets a new id: the old URL 404s, so
	// no client-cached copy can survive a change.
	rec = doRequest(t, mux, "PUT", "/channels/c1/logo", logoBody([]byte{
		0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0, 0xff, 0xd9,
	}))
	json.Unmarshal(rec.Body.Bytes(), &logo)
	if logo["image_id"] == id || logo["content_type"] != "image/jpeg" {
		t.Errorf("a new upload kept the old id: %v", logo)
	}
	if old := doRequest(t, mux, "GET", "/channel-logos/"+id+"/image", ""); old.Code != http.StatusNotFound {
		t.Errorf("the replaced image is still served: %d", old.Code)
	}
	newID := logo["image_id"].(string)

	// Clearing removes the image and the id.
	if rec := doRequest(t, mux, "DELETE", "/channels/c1/logo", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	if gone := doRequest(t, mux, "GET", "/channel-logos/"+newID+"/image", ""); gone.Code != http.StatusNotFound {
		t.Errorf("image outlived its clear: %d", gone.Code)
	}
	rec = doRequest(t, mux, "GET", "/channels/c1/logo", "")
	json.Unmarshal(rec.Body.Bytes(), &logo)
	if logo["image_id"] != "" {
		t.Errorf("clear left %v", logo)
	}
}

// TestChannelLogoUploadValidation checks that the logo accepts exactly what a
// watermark accepts, and that a rejected upload never replaces the stored one.
func TestChannelLogoUploadValidation(t *testing.T) {
	db, mux := newTestDB(t)
	newEPGSeed(t, db)

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	big := append(append([]byte{}, tinyPNG...), make([]byte, MaxChannelLogoBytes)...)
	webp := append([]byte("RIFF\x1a\x00\x00\x00WEBPVP8 \x0e\x00\x00\x00"), make([]byte, 14)...)
	for _, c := range []struct {
		name string
		body string
		want int
	}{
		{"svg", logoBody(svg), http.StatusUnsupportedMediaType},
		{"html", logoBody([]byte("<html><body>hi</body></html>")), http.StatusUnsupportedMediaType},
		{"gif", logoBody([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00")), http.StatusUnsupportedMediaType},
		{"too large", logoBody(big), http.StatusRequestEntityTooLarge},
		{"not base64", `{"image_base64":"%%%"}`, http.StatusBadRequest},
		{"empty image", `{"image_base64":""}`, http.StatusBadRequest},
		{"missing image", `{}`, http.StatusBadRequest},
		{"webp", logoBody(webp), http.StatusOK},
	} {
		rec := doRequest(t, mux, "PUT", "/channels/c1/logo", c.body)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
	if rec := doRequest(t, mux, "PUT", "/channels/nope/logo", logoBody(tinyPNG)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel: %d", rec.Code)
	}

	// The stored logo is the last accepted upload, not one of the rejected ones.
	rec := doRequest(t, mux, "GET", "/channels/c1/logo", "")
	var logo map[string]any
	json.Unmarshal(rec.Body.Bytes(), &logo)
	if logo["content_type"] != "image/webp" {
		t.Errorf("content_type = %v, want image/webp", logo["content_type"])
	}
}

// TestChannelLogoNeedsAuth pins the auth policy: the bytes are a device-or-
// session GET like a watermark image, while uploading and clearing a logo are
// authoring (session only).
func TestChannelLogoNeedsAuth(t *testing.T) {
	db, mux := newTestDB(t)
	newEPGSeed(t, db)
	rec := doRequest(t, mux, "PUT", "/channels/c1/logo", logoBody(tinyPNG))
	var logo map[string]any
	json.Unmarshal(rec.Body.Bytes(), &logo)
	id := logo["image_id"].(string)

	device := doRequest(t, mux, "POST", "/devices", `{"name":"TV","platform":"linux"}`)
	if device.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", device.Code, device.Body.String())
	}
	var dev createDeviceResponse
	json.Unmarshal(device.Body.Bytes(), &dev)

	cases := []struct {
		name         string
		method, path string
		body, token  string
		want         int
	}{
		{"image with a device token", "GET", "/channel-logos/" + id + "/image", "", dev.Token, http.StatusOK},
		{"image with no token", "GET", "/channel-logos/" + id + "/image", "", "", http.StatusUnauthorized},
		{"upload with a device token", "PUT", "/channels/c1/logo", logoBody(tinyPNG), dev.Token, http.StatusUnauthorized},
		{"clear with a device token", "DELETE", "/channels/c1/logo", "", dev.Token, http.StatusUnauthorized},
	}
	for _, c := range cases {
		var body io.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		req := httptest.NewRequest(c.method, c.path, body)
		if c.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		rec := serveRequest(mux, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

// serveRequest runs one already-built request through the mux.
func serveRequest(mux *http.ServeMux, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
