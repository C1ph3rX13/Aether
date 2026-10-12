package docker

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/moby/moby/client"
)

const (
	stoppedProcessFileMaximumBytes = int64(8 << 20)
	stoppedProcessFileReadTimeout  = 5 * time.Second
)

var errStoppedProcessFileUnavailable = errors.New("stopped Process file evidence unavailable")

// stoppedProcessFileReader is created only after the owning Exec has a
// confirmed non-running inspection. It deliberately exposes no Docker
// details or requested path outside the Adapter's in-memory capability.
type stoppedProcessFileReader struct {
	client      *client.Client
	containerID string

	mu   sync.Mutex
	used bool
}

func newStoppedProcessFileReader(dockerClient *client.Client, containerID string) *stoppedProcessFileReader {
	return &stoppedProcessFileReader{client: dockerClient, containerID: containerID}
}

func (reader *stoppedProcessFileReader) ReadFiles(ctx context.Context, requestedPaths []string) map[string]rundomain.StoppedProcessFile {
	results := make(map[string]rundomain.StoppedProcessFile, len(requestedPaths))
	reader.mu.Lock()
	if reader.used {
		reader.mu.Unlock()
		for _, requestedPath := range requestedPaths {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: errStoppedProcessFileUnavailable}
		}
		return results
	}
	reader.used = true
	reader.mu.Unlock()

	if len(requestedPaths) == 0 || len(requestedPaths) > 2 {
		for _, requestedPath := range requestedPaths {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: errStoppedProcessFileUnavailable}
		}
		return results
	}
	seen := make(map[string]struct{}, len(requestedPaths))
	for _, requestedPath := range requestedPaths {
		if _, duplicate := seen[requestedPath]; duplicate || validateStoppedProcessFilePath(requestedPath) != nil {
			for _, path := range requestedPaths {
				results[path] = rundomain.StoppedProcessFile{Err: errStoppedProcessFileUnavailable}
			}
			return results
		}
		seen[requestedPath] = struct{}{}
	}
	if ctx == nil {
		for _, requestedPath := range requestedPaths {
			results[requestedPath] = rundomain.StoppedProcessFile{Err: errStoppedProcessFileUnavailable}
		}
		return results
	}
	readContext, cancel := context.WithTimeout(ctx, stoppedProcessFileReadTimeout)
	defer cancel()
	for _, requestedPath := range requestedPaths {
		archive, err := reader.client.CopyFromContainer(readContext, reader.containerID, client.CopyFromContainerOptions{
			SourcePath: requestedPath,
		})
		if err != nil {
			if readContext.Err() != nil {
				results[requestedPath] = rundomain.StoppedProcessFile{Err: readContext.Err()}
			} else {
				results[requestedPath] = rundomain.StoppedProcessFile{Err: errStoppedProcessFileUnavailable}
			}
			continue
		}
		content, readErr := readStoppedProcessFileArchive(archive.Content, path.Base(requestedPath))
		_ = archive.Content.Close()
		results[requestedPath] = rundomain.StoppedProcessFile{Bytes: content, Err: readErr}
	}
	return results
}

func validateStoppedProcessFilePath(requestedPath string) error {
	if requestedPath == "" || strings.IndexByte(requestedPath, 0) >= 0 || !path.IsAbs(requestedPath) ||
		path.Clean(requestedPath) != requestedPath || strings.HasSuffix(requestedPath, "/") {
		return errStoppedProcessFileUnavailable
	}
	base := path.Base(requestedPath)
	if base == "." || base == ".." || base == "/" || strings.Contains(base, "/") {
		return errStoppedProcessFileUnavailable
	}
	return nil
}

func readStoppedProcessFileArchive(reader io.Reader, requestedBasename string) ([]byte, error) {
	archive := tar.NewReader(reader)
	header, err := archive.Next()
	if err != nil {
		return nil, errStoppedProcessFileUnavailable
	}
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA ||
		!header.FileInfo().Mode().IsRegular() || !safeStoppedProcessArchiveName(header.Name) ||
		header.Name != requestedBasename || header.Size <= 0 || header.Size > stoppedProcessFileMaximumBytes {
		return nil, errStoppedProcessFileUnavailable
	}

	payload := make([]byte, int(header.Size))
	if _, err := io.ReadFull(archive, payload); err != nil {
		return nil, errStoppedProcessFileUnavailable
	}
	if _, err := archive.Next(); err != io.EOF {
		return nil, errStoppedProcessFileUnavailable
	}
	var remainder [512]byte
	for {
		read, err := reader.Read(remainder[:])
		for _, value := range remainder[:read] {
			if value != 0 {
				return nil, errStoppedProcessFileUnavailable
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errStoppedProcessFileUnavailable
		}
	}
	return payload, nil
}

func safeStoppedProcessArchiveName(name string) bool {
	return name != "" && !path.IsAbs(name) && path.Clean(name) == name &&
		name != "." && name != ".." && !strings.Contains(name, "/") && !strings.Contains(name, "\\")
}

var _ rundomain.StoppedProcessFileReader = (*stoppedProcessFileReader)(nil)
