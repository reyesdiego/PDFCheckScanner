package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func getUI(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d", path, rec.Code)
	}
	return rec
}

func TestUploadPageIsServed(t *testing.T) {
	rec := getUI(t, "/")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="file"`, `id="canvas"`, `id="boxes"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page has no %s", want)
		}
	}
}

// Every script and stylesheet the page asks for must be one the router
// serves, with a type the browser will accept under nosniff.
func TestUploadPageAssetsResolve(t *testing.T) {
	page := getUI(t, "/").Body.String()
	refs := regexp.MustCompile(`(?:src|href)="(/[^"]*)"`).FindAllStringSubmatch(page, -1)
	if len(refs) == 0 {
		t.Fatal("page references no assets")
	}
	wantType := map[string]string{".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml"}
	for _, ref := range refs {
		path := ref[1]
		rec := getUI(t, path)
		for ext, ct := range wantType {
			if strings.HasSuffix(path, ext) && !strings.HasPrefix(rec.Header().Get("Content-Type"), ct) {
				t.Errorf("%s: Content-Type = %q, want %s", path, rec.Header().Get("Content-Type"), ct)
			}
		}
	}
}

// The page shows filenames and field names taken from uploads, so it runs
// under a policy that allows no inline or third-party script at all.
func TestUploadPageIsLockedDown(t *testing.T) {
	for _, path := range []string{"/", "/app.js", "/style.css", "/favicon.svg"} {
		h := getUI(t, path).Header()
		csp := h.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: policy %q lacks %s", path, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: policy %q allows inline or eval'd script", path, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", path)
		}
	}

	page := getUI(t, "/").Body.String()
	if regexp.MustCompile(`<script>|<style>|\son[a-z]+=`).MatchString(page) {
		t.Error("page has inline script, style or event handlers the policy would block")
	}
}
