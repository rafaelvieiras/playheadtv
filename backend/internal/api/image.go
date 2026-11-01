package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
)

// MaxImageBytes caps a decoded image uploaded through the API (a channel logo,
// a watermark). The image travels inside a JSON body as base64, so the web UI
// and the MCP tool share one route, and it is validated by its content, never
// by a file extension or the declared type.
const MaxImageBytes = 1 << 20

// imageTypes are the accepted content types, as http.DetectContentType names
// them from the first bytes. SVG is deliberately absent: it is a document that
// can carry script.
var imageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

var errImageTooLarge = fmt.Errorf("the image is larger than %d KiB", MaxImageBytes>>10)

// SniffImage validates image bytes and returns their content type.
func SniffImage(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("the image is empty")
	}
	if len(data) > MaxImageBytes {
		return "", errImageTooLarge
	}
	ct := http.DetectContentType(data)
	if !imageTypes[ct] {
		return "", fmt.Errorf("the image must be PNG, JPEG or WebP (SVG is not accepted)")
	}
	return ct, nil
}

// decodeImageBase64 decodes and validates an image uploaded as base64, mapping
// each failure to the status the client deserves.
func decodeImageBase64(encoded string) ([]byte, string, int, error) {
	// Refuse an oversized payload before decoding it.
	if base64.StdEncoding.DecodedLen(len(encoded)) > MaxImageBytes+3 {
		return nil, "", http.StatusRequestEntityTooLarge, errImageTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", http.StatusBadRequest, fmt.Errorf("image_base64 is not valid base64")
	}
	ct, err := SniffImage(data)
	if err != nil {
		status := http.StatusUnsupportedMediaType
		if err == errImageTooLarge {
			status = http.StatusRequestEntityTooLarge
		}
		return nil, "", status, err
	}
	return data, ct, http.StatusOK, nil
}

// bodyLimitForImage is the JSON body cap of an upload: the base64 of the image
// cap plus headroom for the other fields.
const bodyLimitForImage = MaxImageBytes*4/3 + 4096
