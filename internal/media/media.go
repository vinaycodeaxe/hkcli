// Package media transfers attachments. Hike stores media unencrypted behind
// signed storage URLs.
//
// Download: GET /media/v1/{fileId}/download-url -> byte-range chunks.
// Upload: POST /media/v1/upload/initiate -> PUT each chunk to a signed URL (keep
// the ETag) -> POST /media/v1/upload/{sessionId}/complete -> fileId.
// The signed URLs belong to a storage provider, so the Hike login token is never
// sent there.
package media

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vinaycodeaxe/hkcli/internal/api"
	"github.com/vinaycodeaxe/hkcli/internal/config"
	"github.com/vinaycodeaxe/hkcli/internal/protocol"
	"github.com/vinaycodeaxe/hkcli/internal/pyjson"
)

// MaxUploadBytes is Hike Web's upload limit (1 GB).
const MaxUploadBytes = 1_000_000_000

var (
	typeByExt   = map[string]string{}
	extByType   = map[string]string{}
	suffixMap   = map[string]string{".svgz": ".svg.gz", ".tgz": ".tar.gz", ".taz": ".tar.gz", ".tz": ".tar.gz", ".tbz2": ".tar.bz2", ".txz": ".tar.xz"}
	encodingExt = map[string]bool{".gz": true, ".Z": true, ".bz2": true, ".xz": true, ".br": true}
)

func init() {
	for _, line := range strings.Split(strings.TrimSpace(pythonTypes), "\n") {
		ext, typ, _ := strings.Cut(line, " ")
		typeByExt[ext] = typ
		if _, seen := extByType[typ]; !seen {
			extByType[typ] = ext
		}
	}
}

// splitExt splits a name at its last dot; leading dots do not start an extension.
func splitExt(name string) (string, string) {
	i := strings.LastIndexByte(name, '.')
	if i <= strings.LastIndexByte(name, '/') || strings.TrimLeft(name[strings.LastIndexByte(name, '/')+1:i], ".") == "" {
		return name, ""
	}
	return name[:i], name[i:]
}

// GuessMIME guesses a file's type from its name with Python's mimetypes table, then
// the system tables; parameters such as charset are dropped.
func GuessMIME(name string) string {
	base, ext := splitExt(name)
	for suffixMap[strings.ToLower(ext)] != "" {
		base, ext = splitExt(base + suffixMap[strings.ToLower(ext)])
	}
	if encodingExt[ext] {
		base, ext = splitExt(base)
	}
	if typ, ok := typeByExt[ext]; ok {
		return typ
	}
	if typ, ok := typeByExt[strings.ToLower(ext)]; ok {
		return typ
	}
	if ext != "" {
		if typ, _, err := mime.ParseMediaType(mime.TypeByExtension(ext)); err == nil {
			return typ
		}
	}
	return "application/octet-stream"
}

// GuessExtension returns the usual extension (with the dot) for a MIME type, or "".
func GuessExtension(mimeType string) string {
	if ext, ok := extByType[strings.ToLower(mimeType)]; ok {
		return ext
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

func storageClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ForceAttemptHTTP2:     true,
	}}
}

// stallReader cancels the request when no data arrives for timeout, like a
// socket read timeout (a large download may take longer than any fixed limit).
type stallReader struct {
	r     io.Reader
	timer *time.Timer
	limit time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	s.timer.Reset(s.limit)
	return s.r.Read(p)
}

// Downloaded describes a saved file.
type Downloaded struct {
	Path string
	Size int64
	MIME any // Hike's reported type (raw)
}

func storageError(format string, args ...any) error {
	return &api.Error{Msg: fmt.Sprintf(format, args...)}
}

// Download saves a file to dest, chunk by chunk, through dest + ".part".
func Download(ctx context.Context, client *api.Client, fileID, dest string) (*Downloaded, error) {
	info, err := client.MediaDownloadInfo(ctx, fileID)
	if err != nil {
		return nil, err
	}
	var chunks []pyjson.Object
	if list, ok := info.Get("chunkDownloads").([]any); ok {
		for _, c := range list {
			obj, _ := c.(pyjson.Object)
			chunks = append(chunks, obj)
		}
	}
	sort.SliceStable(chunks, func(i, j int) bool { return chunkIndex(chunks[i]) < chunkIndex(chunks[j]) })
	if len(chunks) == 0 {
		return nil, storageError("Hike returned no download chunks for file %s", fileID)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
		return nil, err
	}
	tmp := dest + ".part"
	written, err := downloadChunks(ctx, chunks, tmp)
	if err == nil {
		if total := info.Get("fileSizeBytes"); total != nil {
			if n, _ := protocol.AsInt(total); n != written {
				err = storageError("downloaded %d bytes but Hike reported %s", written, pyjson.Str(total))
			}
		}
	}
	if err == nil {
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return &Downloaded{Path: dest, Size: written, MIME: info.Get("mimeType")}, nil
}

func chunkIndex(chunk pyjson.Object) float64 {
	f, _ := protocol.AsFloat(chunk.Get("chunkIndex"))
	return f
}

func downloadChunks(ctx context.Context, chunks []pyjson.Object, tmp string) (int64, error) {
	const timeout = 120 * time.Second
	fh, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	hc := storageClient(timeout)
	var written int64
	for _, chunk := range chunks {
		start, _ := protocol.AsInt(chunk.Get("rangeStart"))
		end, _ := protocol.AsInt(chunk.Get("rangeEnd"))
		index := pyjson.Str(chunk.Get("chunkIndex"))
		got, status, err := fetchRange(ctx, hc, pyjson.Str(chunk.Get("downloadUrl")), start, end, fh, timeout)
		if err != nil {
			return written, err
		}
		if status != 200 && status != 206 {
			return written, storageError("media chunk %s download failed: HTTP %d", index, status)
		}
		written += got
		if status == 200 {
			break // the server ignored Range and sent the whole file
		}
		if got != end-start+1 {
			return written, storageError("media chunk %s was truncated (%d of %d bytes)", index, got, end-start+1)
		}
	}
	return written, fh.Close()
}

func fetchRange(ctx context.Context, client *http.Client, url string, start, end int64, w io.Writer, timeout time.Duration) (int64, int, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return 0, 0, storageError("media download failed: %v", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	req.Header.Set("User-Agent", config.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, transferError(ctx, "media download failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return 0, resp.StatusCode, nil
	}
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()
	n, err := io.Copy(w, &stallReader{r: resp.Body, timer: timer, limit: timeout})
	if err != nil {
		return n, resp.StatusCode, transferError(ctx, "media download failed", err)
	}
	return n, resp.StatusCode, nil
}

// transferError reports a failed storage request, or the interruption that caused it.
func transferError(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return storageError("%s: %v", what, err)
}

// Upload sends a file to Hike's storage and returns its file id.
func Upload(ctx context.Context, client *api.Client, filePath, mimeType string) (string, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return "", err
	}
	size, name := info.Size(), filepath.Base(filePath)
	if size > MaxUploadBytes {
		return "", storageError("%s is larger than Hike's 1 GB limit", name)
	}
	plan, err := client.UploadInitiate(ctx, name, mimeType, size)
	if err != nil {
		return "", err
	}
	chunkSize, _ := protocol.AsInt(plan.Get("chunkSizeBytes"))
	var parts []pyjson.Object
	if list, ok := plan.Get("chunkUrls").([]any); ok {
		for _, p := range list {
			obj, _ := p.(pyjson.Object)
			parts = append(parts, obj)
		}
	}
	sort.SliceStable(parts, func(i, j int) bool { return chunkIndex(parts[i]) < chunkIndex(parts[j]) })
	if len(parts) == 0 || chunkSize <= 0 || chunkIndex(parts[0]) < 0 {
		return "", storageError("Hike did not return upload URLs")
	}
	etags := make([]string, int(chunkIndex(parts[len(parts)-1]))+1)
	fh, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	hc := storageClient(300 * time.Second)
	for _, part := range parts {
		index, _ := protocol.AsInt(part.Get("chunkIndex"))
		start := index * chunkSize
		length := max(0, min(start+chunkSize, size)-start)
		etag, err := putChunk(ctx, hc, pyjson.Str(part.Get("uploadUrl")), io.NewSectionReader(fh, start, length), length, mimeType, index)
		if err != nil {
			return "", err
		}
		etags[index] = etag
	}
	for _, tag := range etags {
		if tag == "" {
			return "", storageError("Hike's upload plan skipped a chunk index")
		}
	}
	return client.UploadComplete(ctx, pyjson.Str(plan.Get("sessionId")), etags)
}

func putChunk(ctx context.Context, client *http.Client, url string, body io.Reader, length int64, mimeType string, index int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
	if err != nil {
		return "", storageError("upload of chunk %d failed: %v", index, err)
	}
	req.ContentLength = length
	if length == 0 {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("User-Agent", config.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return "", transferError(ctx, fmt.Sprintf("upload of chunk %d failed", index), err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", storageError("upload of chunk %d failed: HTTP %d", index, resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", storageError("upload of chunk %d returned no ETag", index)
	}
	return etag, nil
}
