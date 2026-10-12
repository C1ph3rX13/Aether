package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"
)

func TestStaticRoutesResolveNextExportWithoutDirectoryRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	assets := fstest.MapFS{
		"index.html":                         {Data: []byte("index")},
		"projects.html":                      {Data: []byte("catalog")},
		"projects.txt":                       {Data: []byte("catalog RSC")},
		"projects/__next.projects.txt":       {Data: []byte("RSC")},
		"projects/__placeholder__.html":      {Data: []byte("workspace")},
		"projects/__placeholder__.txt":       {Data: []byte("workspace RSC")},
		"_next/static/chunks/application.js": {Data: []byte("asset")},
	}
	router := gin.New()
	RegisterStatic(router, assets)

	for _, test := range []struct {
		path       string
		statusCode int
		body       string
	}{
		{path: "/", statusCode: http.StatusOK, body: "index"},
		{path: "/projects", statusCode: http.StatusOK, body: "catalog"},
		{path: "/projects/", statusCode: http.StatusOK, body: "catalog"},
		{path: "/projects/p001", statusCode: http.StatusOK, body: "workspace"},
		{path: "/_next/static/chunks/application.js", statusCode: http.StatusOK, body: "asset"},
		{path: "/api/v1/missing", statusCode: http.StatusNotFound, body: "404 page not found"},
		{path: "/unknown", statusCode: http.StatusNotFound, body: "404 page not found"},
		{path: "/settings/project", statusCode: http.StatusNotFound, body: "404 page not found"},
		{path: "/settings/system", statusCode: http.StatusNotFound, body: "404 page not found"},
		{path: "/_next/static/chunks/missing.js", statusCode: http.StatusNotFound, body: "404 page not found"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.statusCode || response.Body.String() != test.body {
				t.Fatalf("GET %s status=%d body=%q, want status=%d body=%q", test.path, response.Code, response.Body.String(), test.statusCode, test.body)
			}
		})
	}

	for _, test := range []struct {
		path string
		body string
	}{
		{path: "/projects?_rsc=one", body: "catalog RSC"},
		{path: "/projects/p001?_rsc=two", body: "workspace RSC"},
	} {
		t.Run("RSC "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.Header.Set("RSC", "1")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Body.String() != test.body {
				t.Fatalf("GET %s status=%d body=%q, want RSC body=%q", test.path, response.Code, response.Body.String(), test.body)
			}
		})
	}
}

func TestStaticRoutesNegotiateCompressedRepresentations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	asset := strings.Repeat("export const workspace = 'Aether';\n", 100)
	router := gin.New()
	RegisterStatic(router, fstest.MapFS{
		"index.html":                    {Data: []byte("index"), ModTime: modTime},
		"projects.html":                 {Data: []byte("catalog")},
		"projects.txt":                  {Data: []byte("catalog RSC")},
		"projects/__placeholder__.html": {Data: []byte("workspace")},
		"projects/__placeholder__.txt":  {Data: []byte("workspace RSC")},
		"_next/static/chunks/app.js":    {Data: []byte(asset), ModTime: modTime},
	})
	request := func(method, path, encoding, byteRange, rsc string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Accept-Encoding", encoding)
		if byteRange != "" {
			r.Header.Set("Range", byteRange)
		}
		if rsc != "" {
			r.Header.Set("RSC", rsc)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	for _, test := range []struct {
		path, rsc, body string
	}{
		{"/", "", "index"},
		{"/projects", "", "catalog"},
		{"/projects", "1", "catalog RSC"},
		{"/projects/p001", "", "workspace"},
		{"/projects/p001", "1", "workspace RSC"},
		{"/_next/static/chunks/app.js", "", asset},
	} {
		t.Run(test.path+" RSC="+test.rsc, func(t *testing.T) {
			w := request(http.MethodGet, test.path, "br, gzip", "", test.rsc)
			if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
				t.Fatalf("status=%d headers=%v", w.Code, w.Header())
			}
			reader, err := gzip.NewReader(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			body, err := io.ReadAll(reader)
			if err != nil || string(body) != test.body {
				t.Fatalf("decoded body=%q err=%v", body, err)
			}
			vary := strings.Join(w.Header().Values("Vary"), ",")
			if !strings.Contains(vary, "Accept-Encoding") || !strings.Contains(vary, "RSC") {
				t.Fatalf("Vary=%q", vary)
			}
		})
	}

	const assetPath = "/_next/static/chunks/app.js"
	compressed := request(http.MethodGet, assetPath, "gzip", "", "")
	if compressed.Body.Len() >= len(asset) || compressed.Header().Get("Content-Length") != strconv.Itoa(compressed.Body.Len()) {
		t.Fatalf("compressed size=%d, original size=%d, headers=%v", compressed.Body.Len(), len(asset), compressed.Header())
	}
	if compressed.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control=%q", compressed.Header().Get("Cache-Control"))
	}
	if request(http.MethodGet, "/projects", "gzip", "", "").Header().Get("Cache-Control") != "" {
		t.Fatal("route HTML must not have immutable asset caching")
	}
	head := request(http.MethodHead, assetPath, "gzip", "", "")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Encoding") != "gzip" || head.Header().Get("Content-Length") != compressed.Header().Get("Content-Length") {
		t.Fatalf("HEAD status=%d body=%q headers=%v", head.Code, head.Body.String(), head.Header())
	}
	if rootHead := request(http.MethodHead, "/", "", "", ""); rootHead.Body.Len() != 0 || rootHead.Header().Get("Content-Length") != "5" {
		t.Fatalf("root HEAD body=%q headers=%v", rootHead.Body.String(), rootHead.Header())
	}
	ranged := request(http.MethodGet, assetPath, "gzip", "bytes=0-2", "")
	if ranged.Code != http.StatusPartialContent || ranged.Header().Get("Content-Encoding") != "" || ranged.Header().Get("Content-Range") != "bytes 0-2/"+strconv.Itoa(len(asset)) || ranged.Body.String() != asset[:3] {
		t.Fatalf("Range status=%d body=%q headers=%v", ranged.Code, ranged.Body.String(), ranged.Header())
	}
	for _, encoding := range []string{"", "br", "gzip;q=0", "gzip;q=0, *;q=1", "gzip;q=invalid"} {
		w := request(http.MethodGet, assetPath, encoding, "", "")
		if w.Header().Get("Content-Encoding") != "" || w.Body.String() != asset || !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Accept-Encoding") {
			t.Fatalf("Accept-Encoding=%q body length=%d headers=%v", encoding, w.Body.Len(), w.Header())
		}
	}
	for _, encoding := range []string{"GZip; q=0.5", "*;q=1", "br, gzip;q=0.8"} {
		if w := request(http.MethodGet, assetPath, encoding, "", ""); w.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("Accept-Encoding=%q headers=%v", encoding, w.Header())
		}
	}
	r := httptest.NewRequest(http.MethodGet, assetPath, nil)
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("If-Modified-Since", modTime.Format(http.TimeFormat))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("conditional GET status=%d body=%q", w.Code, w.Body.String())
	}
	if api := request(http.MethodGet, "/api/v1/missing", "gzip", "", ""); api.Code != http.StatusNotFound || api.Header().Get("Content-Encoding") != "" {
		t.Fatalf("API status=%d headers=%v", api.Code, api.Header())
	}
}
