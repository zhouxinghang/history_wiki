package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSPAHandlerServesAssetsAndDeepLinksWithoutMaskingAPI(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<main>history</main>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "assets", "app.js"), []byte("ready"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := spaHandler(directory)
	for _, test := range []struct {
		path, body, cache string
		status            int
	}{
		{path: "/admin/events/1", body: "<main>history</main>", cache: "no-cache", status: 200},
		{path: "/assets/app.js", body: "ready", cache: "public, max-age=31536000, immutable", status: 200},
		{path: "/api/v1/unknown", status: 404},
		{path: "/metrics", status: 404},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.status || (test.body != "" && response.Body.String() != test.body) {
			t.Fatalf("%s => status %d body %q", test.path, response.Code, response.Body.String())
		}
		if test.cache != "" && response.Header().Get("Cache-Control") != test.cache {
			t.Fatalf("%s cache = %q", test.path, response.Header().Get("Cache-Control"))
		}
	}
}
