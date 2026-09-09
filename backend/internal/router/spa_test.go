package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMountSPA(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	if err := MountSPA(r, dir); err != nil {
		t.Fatal(err)
	}

	assertBody := func(method, path string, wantStatus int, wantBody string) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != wantStatus {
			t.Fatalf("%s %s: status %d, want %d", method, path, w.Code, wantStatus)
		}
		if wantBody != "" && w.Body.String() != wantBody {
			t.Fatalf("%s %s: body %q, want %q", method, path, w.Body.String(), wantBody)
		}
	}

	assertBody(http.MethodGet, "/", http.StatusOK, "<html>spa</html>")
	assertBody(http.MethodGet, "/upstreams", http.StatusOK, "<html>spa</html>")
	assertBody(http.MethodGet, "/assets/app.js", http.StatusOK, "console.log(1)")
	assertBody(http.MethodGet, "/health", http.StatusOK, "ok")
	assertBody(http.MethodGet, "/api/v1/admin/upstreams", http.StatusNotFound, "")
	assertBody(http.MethodGet, "/v1/models", http.StatusNotFound, "")
}

func TestMountSPAEmptyDir(t *testing.T) {
	if err := MountSPA(gin.New(), ""); err != nil {
		t.Fatal(err)
	}
}
