package dashboard

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/klauspost/compress/gzip"
)

// webFiles is the whole browser application. It is a dependency-free set of
// ES modules and one stylesheet, so there is no build step: the files on disk
// are exactly what the browser runs.
//
//go:embed web
var webFiles embed.FS

// appShell is served for every page route. The client-side router decides
// whether the library, a single bookmark or the backstage view is shown, so
// old deep links such as /bookmarks/12?topics=llm keep working.
var appShell = mustReadWeb("index.html")
var appShellGzip = mustGzipStatic(appShell)

type webAsset struct {
	content     []byte
	gzipContent []byte
	contentType string
	etag        string
	gzipETag    string
}

// assetTypes lists the only media types the application ships. Anything else
// under web/ is refused rather than served with a guessed type.
var assetTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
}

// webAssets is built once at start-up. Each file carries a content hash so a
// browser revalidation of an unchanged module costs a 304, not a download.
var webAssets = mustIndexWebAssets()

func mustReadWeb(name string) []byte {
	content, err := webFiles.ReadFile(path.Join("web", name))
	if err != nil {
		panic(fmt.Sprintf("embedded web file %s is missing: %v", name, err))
	}
	return content
}

func mustIndexWebAssets() map[string]webAsset {
	assets := map[string]webAsset{}
	err := fs.WalkDir(webFiles, "web", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		contentType, ok := assetTypes[path.Ext(name)]
		if !ok {
			return nil
		}
		content, err := webFiles.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		asset := webAsset{
			content:     content,
			contentType: contentType,
			etag:        `"` + hex.EncodeToString(sum[:12]) + `"`,
		}
		if asset.gzipContent, err = gzipStatic(content); err != nil {
			return err
		}
		if len(asset.gzipContent) > 0 {
			gzipSum := sha256.Sum256(asset.gzipContent)
			asset.gzipETag = `"` + hex.EncodeToString(gzipSum[:12]) + `"`
		}
		assets[strings.TrimPrefix(name, "web/")] = asset
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("index embedded web assets: %v", err))
	}
	return assets
}

func mustGzipStatic(content []byte) []byte {
	compressed, err := gzipStatic(content)
	if err != nil {
		panic(fmt.Sprintf("compress embedded application shell: %v", err))
	}
	return compressed
}

func gzipStatic(content []byte) ([]byte, error) {
	if len(content) < minCompressedAssetBytes {
		return nil, nil
	}
	var encoded bytes.Buffer
	compressor, err := gzip.NewWriterLevel(&encoded, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := compressor.Write(content); err != nil {
		return nil, err
	}
	if err := compressor.Close(); err != nil {
		return nil, err
	}
	if encoded.Len() >= len(content) {
		return nil, nil
	}
	return encoded.Bytes(), nil
}

func serveWebAsset(writer http.ResponseWriter, request *http.Request) {
	asset, ok := webAssets[request.PathValue("path")]
	if !ok {
		http.NotFound(writer, request)
		return
	}
	appendVary(writer.Header(), "Accept-Encoding")
	content, etag := asset.content, asset.etag
	if acceptsGzip(request) && len(asset.gzipContent) > 0 {
		content, etag = asset.gzipContent, asset.gzipETag
		writer.Header().Set("Content-Encoding", "gzip")
	}
	writer.Header().Set("ETag", etag)
	// no-cache means "revalidate every time": a new release is picked up on the
	// next load, while an unchanged file is confirmed with a bodiless 304.
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if etagMatches(request.Header.Get("If-None-Match"), etag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writer.Header().Set("Content-Type", asset.contentType)
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(content)
	}
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == etag || candidate == "W/"+etag || candidate == "*" {
			return true
		}
	}
	return false
}
