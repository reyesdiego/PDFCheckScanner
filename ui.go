package main

import (
	"embed"
	"net/http"
)

// uiFiles is the upload page, compiled into the binary so the service needs
// nothing beside it to serve one.
//
//go:embed ui
var uiFiles embed.FS

// uiPolicy lets the page load only its own script and stylesheet, talk only
// to this service, and show only images: the one the user picked, through a
// blob: URL, and the PDF pages /preview draws, as data: URLs.
const uiPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' blob: data:; connect-src 'self'; base-uri 'none'; " +
	"form-action 'none'; frame-ancestors 'none'"

// serveUI answers with one embedded file. Reading it here, when the router is
// built, means a missing file stops the service at startup rather than
// failing a request.
func serveUI(name, contentType string) http.HandlerFunc {
	body, err := uiFiles.ReadFile("ui/" + name)
	if err != nil {
		panic("ui: " + err.Error())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Content-Security-Policy", uiPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		w.Write(body)
	}
}
