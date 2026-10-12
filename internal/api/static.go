package api

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// RegisterStatic serves the embedded Next.js frontend from the given filesystem.
// If fsys is nil, static serving is disabled.
func RegisterStatic(r *gin.Engine, fsys fs.FS) {
	if fsys == nil {
		return
	}

	// Embedded assets are immutable. Compress each requested text asset once,
	// then serve its seekable representation with normal HTTP semantics.
	var compressed sync.Map
	serveFile := func(c *gin.Context, name string) bool {
		name = path.Clean(name)
		if !fs.ValidPath(name) {
			return false
		}
		file, err := http.FS(fsys).Open(name)
		if err != nil {
			return false
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			return false
		}
		if strings.HasPrefix(name, "_next/static/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}
		switch path.Ext(name) {
		case ".html", ".txt", ".js", ".css", ".json", ".svg":
			c.Writer.Header().Add("Vary", "Accept-Encoding")
			if c.GetHeader("Range") == "" && acceptsGzip(c.GetHeader("Accept-Encoding")) {
				load, _ := compressed.LoadOrStore(name, sync.OnceValues(func() ([]byte, error) {
					data, err := fs.ReadFile(fsys, name)
					if err != nil {
						return nil, err
					}
					var buffer bytes.Buffer
					writer := gzip.NewWriter(&buffer)
					if _, err := writer.Write(data); err != nil {
						return nil, err
					}
					if err := writer.Close(); err != nil {
						return nil, err
					}
					return buffer.Bytes(), nil
				}))
				data, err := load.(func() ([]byte, error))()
				if err != nil {
					c.AbortWithStatus(http.StatusInternalServerError)
					return true
				}
				c.Header("Content-Type", mime.TypeByExtension(path.Ext(name)))
				c.Header("Content-Encoding", "gzip")
				c.Header("Content-Length", strconv.Itoa(len(data)))
				http.ServeContent(c.Writer, c.Request, name, info.ModTime(), bytes.NewReader(data))
				return true
			}
		}
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
		return true
	}
	serveIndex := func(c *gin.Context) bool { return serveFile(c, "index.html") }

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		// Don't serve static files for API routes
		if len(path) >= 4 && path[:4] == "/api" {
			return
		}
		c.Writer.Header().Add("Vary", "RSC")

		cleanPath := strings.Trim(path, "/")
		if cleanPath == "" {
			serveIndex(c)
			return
		}
		isRSCRequest := c.GetHeader("RSC") == "1" || c.Query("_rsc") != ""
		if isRSCRequest && serveFile(c, cleanPath+".txt") {
			return
		}
		// Next's static export writes clean routes as route.html while also
		// creating route/ directories for RSC data. Resolve the HTML first so
		// a data directory cannot take precedence over the exported page.
		if serveFile(c, cleanPath+".html") {
			return
		}
		if serveFile(c, cleanPath) {
			return
		}
		// SPA fallback: for dynamic routes like /projects/{id},
		// serve the route's placeholder HTML instead of root index.html
		// (root index.html contains a redirect that breaks deep links)
		if strings.HasPrefix(path, "/projects/") && path != "/projects/" {
			segments := strings.Split(strings.Trim(path, "/"), "/")
			// /projects/{id} → serve /projects/__placeholder__.html
			if len(segments) == 2 {
				if isRSCRequest && serveFile(c, "projects/__placeholder__.txt") {
					return
				}
				if serveFile(c, "projects/__placeholder__.html") {
					return
				}
			}
		}
	})
}

func acceptsGzip(header string) bool {
	wildcard := false
	for _, coding := range strings.Split(header, ",") {
		parts := strings.Split(coding, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if name != "gzip" && name != "*" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "q") {
				parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if err != nil || !(parsed >= 0 && parsed <= 1) {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if name == "gzip" {
			return quality > 0
		}
		wildcard = quality > 0
	}
	return wildcard
}
