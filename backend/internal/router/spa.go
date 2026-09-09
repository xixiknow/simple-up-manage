package router

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func MountSPA(r *gin.Engine, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return os.ErrNotExist
	}

	root := http.Dir(abs)
	fileServer := http.FileServer(root)
	index := filepath.Join(abs, "index.html")

	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		p := path.Clean("/" + c.Request.URL.Path)
		if isReservedPath(p) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		rel := strings.TrimPrefix(p, "/")
		if rel != "" && rel != "." {
			f, err := root.Open(rel)
			if err == nil {
				defer f.Close()
				if info, err := f.Stat(); err == nil && !info.IsDir() {
					fileServer.ServeHTTP(c.Writer, c.Request)
					return
				}
			}
		}
		c.File(index)
	})
	return nil
}

func isReservedPath(p string) bool {
	return p == "/health" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/v1/")
}
