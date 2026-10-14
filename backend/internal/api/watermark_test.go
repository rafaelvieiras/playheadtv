package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// tinyPNG is a valid 1x1 PNG.
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0d, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0xf8, 0xff, 0xff, 0x3f, 0, 5, 0xfe, 2, 0xfe, 0xdc, 0xcc, 0x59, 0xe7,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

func wmBody(mode string, image []byte, extra string) string {
	b := `{"mode":"` + mode + `"`
	if image != nil {
		b += `,"image_base64":"` + base64.StdEncoding.EncodeToString(image) + `"`
	}
	return b + extra + `}`
}

func newBlockID(t *testing.T, mux http.Handler, name string) string {
	t.Helper()
	rec := doRequestAny(t, mux, "POST", "/blocks", `{"name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create block: %d %s", rec.Code, rec.Body.String())
	}
	var b struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &b)
	return b.ID
}

func TestWatermarkUploadValidation(t *testing.T) {
	db, mux := newTestDB(t)
	newEPGSeed(t, db)

	// A fresh channel has none; a fresh block inherits.
	rec := doRequest(t, mux, "GET", "/channels/c1/watermark", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mode":"disable"`) {
		t.Fatalf("default channel watermark: %d %s", rec.Code, rec.Body.String())
	}
	blockID := newBlockID(t, mux, "B")
	rec = doRequest(t, mux, "GET", "/blocks/"+blockID+"/watermark", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mode":"inherit"`) {
		t.Fatalf("default block watermark: %d %s", rec.Code, rec.Body.String())
	}

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	big := append(append([]byte{}, tinyPNG...), make([]byte, MaxWatermarkImageBytes)...)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0, 0xff, 0xd9}
	webp := append([]byte("RIFF\x1a\x00\x00\x00WEBPVP8 \x0e\x00\x00\x00"), make([]byte, 14)...)
	for _, c := range []struct {
		name string
		body string
		want int
	}{
		{"svg", wmBody("override", svg, ""), http.StatusUnsupportedMediaType},
		{"svg with a png name is still svg", wmBody("override", svg, `,"filename":"logo.png"`), http.StatusUnsupportedMediaType},
		{"html", wmBody("override", []byte("<html><body>hi</body></html>"), ""), http.StatusUnsupportedMediaType},
		{"gif", wmBody("override", []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00"), ""), http.StatusUnsupportedMediaType},
		{"too large", wmBody("override", big, ""), http.StatusRequestEntityTooLarge},
		{"not base64", `{"mode":"override","image_base64":"%%%"}`, http.StatusBadRequest},
		{"empty image", `{"mode":"override","image_base64":""}`, http.StatusUnsupportedMediaType},
		{"bad mode", wmBody("sometimes", tinyPNG, ""), http.StatusBadRequest},
		{"bad position", wmBody("override", tinyPNG, `,"position":"middle"`), http.StatusBadRequest},
		{"opacity", wmBody("override", tinyPNG, `,"opacity_pct":101`), http.StatusBadRequest},
		{"size too small", wmBody("override", tinyPNG, `,"size_pct":1`), http.StatusBadRequest},
		{"size too large", wmBody("override", tinyPNG, `,"size_pct":41`), http.StatusBadRequest},
		{"png", wmBody("override", tinyPNG, `,"position":"top_left","opacity_pct":50,"size_pct":10`), http.StatusOK},
		{"jpeg", wmBody("override", jpeg, ""), http.StatusOK},
		{"webp", wmBody("override", webp, ""), http.StatusOK},
	} {
		rec := doRequest(t, mux, "PUT", "/channels/c1/watermark", c.body)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
	if rec := doRequest(t, mux, "PUT", "/channels/nope/watermark", wmBody("override", tinyPNG, "")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown channel: %d", rec.Code)
	}
	// A rejected upload never replaced the stored image.
	rec = doRequest(t, mux, "GET", "/channels/c1/watermark", "")
	var wm map[string]any
	json.Unmarshal(rec.Body.Bytes(), &wm)
	if wm["content_type"] != "image/webp" {
		t.Errorf("content_type = %v, want the last accepted upload (image/webp)", wm["content_type"])
	}
}

func TestWatermarkImageServedAndCleanedUpWithOwner(t *testing.T) {
	db, mux := newTestDB(t)
	newEPGSeed(t, db)
	rec := doRequest(t, mux, "PUT", "/channels/c1/watermark", wmBody("override", tinyPNG, `,"position":"top_right"`))
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	var wm map[string]any
	json.Unmarshal(rec.Body.Bytes(), &wm)
	id, _ := wm["image_id"].(string)
	if id == "" || wm["position"] != "top_right" || wm["opacity_pct"] != float64(70) || wm["size_pct"] != float64(8) {
		t.Fatalf("stored = %v", wm)
	}
	if strings.Contains(rec.Body.String(), "image_base64") {
		t.Errorf("the response carries the image: %s", rec.Body.String())
	}

	img := doRequest(t, mux, "GET", "/watermarks/"+id+"/image", "")
	if img.Code != 200 || img.Header().Get("Content-Type") != "image/png" || !strings.Contains(img.Header().Get("Cache-Control"), "immutable") ||
		img.Header().Get("X-Content-Type-Options") != "nosniff" || img.Body.Len() != len(tinyPNG) {
		t.Fatalf("image: %d %v len=%d", img.Code, img.Header(), img.Body.Len())
	}

	// Options alone keep the image (same id); a new image gets a new id.
	rec = doRequest(t, mux, "PUT", "/channels/c1/watermark", `{"mode":"override","opacity_pct":20}`)
	json.Unmarshal(rec.Body.Bytes(), &wm)
	if wm["image_id"] != id || wm["opacity_pct"] != float64(20) || wm["position"] != "top_right" {
		t.Errorf("options-only update: %v", wm)
	}
	rec = doRequest(t, mux, "PUT", "/channels/c1/watermark", wmBody("override", tinyPNG, ""))
	json.Unmarshal(rec.Body.Bytes(), &wm)
	if wm["image_id"] == id {
		t.Errorf("a new upload kept the old image id")
	}
	if old := doRequest(t, mux, "GET", "/watermarks/"+id+"/image", ""); old.Code != http.StatusNotFound {
		t.Errorf("the replaced image is still served: %d", old.Code)
	}
	newID := wm["image_id"].(string)

	// Deleting the owner removes the row and the image.
	if rec := doRequest(t, mux, "DELETE", "/channels/c1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete channel: %d %s", rec.Code, rec.Body.String())
	}
	if gone := doRequest(t, mux, "GET", "/watermarks/"+newID+"/image", ""); gone.Code != http.StatusNotFound {
		t.Errorf("image outlived its channel: %d", gone.Code)
	}
	var n []int
	db.Query(context.Background(), &n, "SELECT COUNT(*) FROM watermark")
	if n[0] != 0 {
		t.Errorf("%d watermark rows left", n[0])
	}

	// Same for a block, and an explicit clear.
	blockID := newBlockID(t, mux, "B")
	doRequest(t, mux, "PUT", "/blocks/"+blockID+"/watermark", wmBody("override", tinyPNG, ""))
	if rec := doRequest(t, mux, "DELETE", "/blocks/"+blockID+"/watermark", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("clear: %d", rec.Code)
	}
	db.Query(context.Background(), &n, "SELECT COUNT(*) FROM watermark")
	if n[0] != 0 {
		t.Errorf("clear left %d rows", n[0])
	}
	doRequest(t, mux, "PUT", "/blocks/"+blockID+"/watermark", wmBody("disable", nil, ""))
	doRequest(t, mux, "DELETE", "/blocks/"+blockID, "")
	db.Query(context.Background(), &n, "SELECT COUNT(*) FROM watermark")
	if n[0] != 0 {
		t.Errorf("block delete left %d rows", n[0])
	}
}

func TestWatermarkResolutionPerEntry(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	ctx := context.Background()

	bOver := newBlockID(t, mux, "Override")
	bOff := newBlockID(t, mux, "Disabled")
	bInh := newBlockID(t, mux, "Inherit")
	bNoImg := newBlockID(t, mux, "OverrideNoImage")
	sched := newSchedule(t, mux, "S")
	item := map[string]string{}
	for i, b := range map[string]string{"over": bOver, "off": bOff, "inh": bInh, "noimg": bNoImg} {
		_ = i
		rec := doRequest(t, mux, "POST", "/schedules/"+sched+"/items", fmt.Sprintf(`{"block_id":"%s","recurrence_type":"once","start_at":1000}`, b))
		if rec.Code != http.StatusCreated {
			t.Fatalf("schedule item: %d %s", rec.Code, rec.Body.String())
		}
		var it struct{ ID string }
		json.Unmarshal(rec.Body.Bytes(), &it)
		item[i] = it.ID
	}

	put := func(path, body string) map[string]any {
		t.Helper()
		rec := doRequest(t, mux, "PUT", path, body)
		if rec.Code != 200 {
			t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body.String())
		}
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	chWM := put("/channels/c1/watermark", wmBody("override", tinyPNG, `,"position":"bottom_left","opacity_pct":40,"size_pct":12`))
	ovWM := put("/blocks/"+bOver+"/watermark", wmBody("override", tinyPNG, `,"position":"top_left","opacity_pct":90,"size_pct":5`))
	put("/blocks/"+bOff+"/watermark", wmBody("disable", nil, ""))
	put("/blocks/"+bNoImg+"/watermark", `{"mode":"override"}`)

	// Every kind of entry of each block, plus a fallback with no source.
	at := int64(0)
	add := func(id, ch, kind, src string) {
		t.Helper()
		at += 10000
		s.entry(t, id, ch, kind, "clip0", at, at+10000)
		if src != "" {
			if err := db.Exec(ctx, "UPDATE epg_entry SET source_schedule_item_id = ? WHERE id = ?", src, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("over-prog", "c1", "program", item["over"])
	add("over-pre", "c1", "preroll", item["over"])
	add("off-prog", "c1", "program", item["off"])
	add("off-tail", "c1", "tail", item["off"])
	add("off-mid", "c1", "midroll", item["off"])
	add("inh-prog", "c1", "program", item["inh"])
	add("inh-post", "c1", "postroll", item["inh"])
	add("noimg-prog", "c1", "program", item["noimg"])
	add("fallback", "c1", "fallback", "")
	add("c2-prog", "c2", "program", item["over"]) // channel c2 has no watermark of its own
	add("c2-fallback", "c2", "fallback", "")

	got := map[string]map[string]any{}
	for _, e := range getEPGJSON(t, mux, "/epg") {
		got[e["id"].(string)], _ = e["watermark"].(map[string]any)
	}
	wantCh := map[string]any{"image_id": chWM["image_id"], "position": "bottom_left", "opacity_pct": float64(40), "size_pct": float64(12)}
	wantOv := map[string]any{"image_id": ovWM["image_id"], "position": "top_left", "opacity_pct": float64(90), "size_pct": float64(5)}
	for id, want := range map[string]map[string]any{
		"over-prog":   wantOv,
		"over-pre":    wantOv, // the filler follows the block it decorates
		"off-prog":    nil,
		"off-tail":    nil,
		"off-mid":     nil,
		"inh-prog":    wantCh,
		"inh-post":    wantCh,
		"noimg-prog":  nil, // override with nothing to draw is none, not the channel's
		"fallback":    wantCh,
		"c2-prog":     wantOv, // a block's override applies on any channel
		"c2-fallback": nil,
	} {
		g, ok := got[id]
		if want == nil {
			if g != nil {
				t.Errorf("%s: watermark = %v, want none", id, g)
			}
			continue
		}
		if !ok || fmt.Sprint(g) != fmt.Sprint(want) {
			t.Errorf("%s: watermark = %v, want %v", id, g, want)
		}
	}

	// A tail group carries the watermark of the block it decorates.
	for _, e := range getEPGJSON(t, mux, "/epg?channel_id=c1&group_tail=1") {
		if e["kind"] == "tail" && e["watermark"] != nil {
			t.Errorf("a disabled block's tail group carries %v", e["watermark"])
		}
	}

	// Disabling the channel's watermark leaves the block override untouched.
	put("/channels/c1/watermark", `{"mode":"disable"}`)
	got = map[string]map[string]any{}
	for _, e := range getEPGJSON(t, mux, "/epg?channel_id=c1") {
		got[e["id"].(string)], _ = e["watermark"].(map[string]any)
	}
	if got["fallback"] != nil || got["inh-prog"] != nil || got["over-prog"] == nil {
		t.Errorf("after disabling the channel: fallback=%v inherit=%v override=%v", got["fallback"], got["inh-prog"], got["over-prog"])
	}
}

// With no watermark configured the guide is byte-for-byte what it was: no
// `watermark` key anywhere.
func TestWatermarkAbsentFromGuideWhenNotConfigured(t *testing.T) {
	db, mux := newTestDB(t)
	s := newEPGSeed(t, db)
	s.entry(t, "e1", "c1", "program", "ep", 1000, 2000)
	rec := doRequest(t, mux, "GET", "/epg", "")
	if strings.Contains(rec.Body.String(), "watermark") {
		t.Errorf("body mentions watermark: %s", rec.Body.String())
	}
}
