package web

import (
	"fmt"
	"net/http"

	qrcode "github.com/skip2/go-qrcode"
)

// qrHTML renders text as an inline SVG QR code, so the pairing link can be
// scanned without loading third-party scripts (the CSP forbids them).
func qrHTML(text string) string {
	q, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return ""
	}
	// The QR must stay dark-on-light to be scannable, even in the dark theme.
	q.DisableBorder = false
	bitmap := q.Bitmap()
	n := len(bitmap)
	if n == 0 {
		return ""
	}
	var b []byte
	b = append(b, fmt.Sprintf(
		`<svg viewBox="0 0 %d %d" role="img" aria-label="Pairing QR code" shape-rendering="crispEdges" width="200" height="200">`,
		n, n)...)
	b = append(b, `<rect width="100%" height="100%" fill="#ffffff"/>`...)
	for y, row := range bitmap {
		for x, on := range row {
			if on {
				b = append(b, fmt.Sprintf(`<rect x="%d" y="%d" width="1" height="1" fill="#0c0c0e"/>`, x, y)...)
			}
		}
	}
	b = append(b, `</svg>`...)
	return string(b)
}

// qrHandler serves a QR code as SVG for any ?text= value.
func (s *Server) qrHandler(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" {
		http.Error(w, "missing text", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(qrHTML(text)))
}
