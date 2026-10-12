package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/moby/moby/client"
)

func (r *Runtime) writeBlackboardInput(ctx context.Context, containerID, inputPath string, yaml []byte) error {
	if !rundomain.ValidBlackboardInputPath(inputPath) {
		return fmt.Errorf("Blackboard input path is invalid")
	}
	return r.writeStartupFile(ctx, containerID, "/", inputPath, yaml)
}

func (r *Runtime) writeProtocolFile(ctx context.Context, containerID string, file rundomain.ProtocolFile) error {
	if err := file.Validate(); err != nil {
		return err
	}
	// /tmp belongs to the image. Do not archive it and overwrite its mode;
	// the non-root agent still needs its normal writable temporary directory.
	mode := int64(0o644)
	if file.Writable {
		mode = 0o666
	}
	return r.writeStartupFileWithMode(ctx, containerID, "/tmp", strings.TrimPrefix(file.Path(), "/tmp/"), file.Bytes, mode)
}

func (r *Runtime) writeStartupFile(ctx context.Context, containerID, destination, filePath string, payload []byte) error {
	return r.writeStartupFileWithMode(ctx, containerID, destination, filePath, payload, 0o644)
}

func (r *Runtime) writeStartupFileWithMode(ctx context.Context, containerID, destination, filePath string, payload []byte, mode int64) error {
	archive, err := startupFileArchiveWithMode(filePath, payload, mode)
	if err != nil {
		return err
	}
	if _, err := r.client.CopyToContainer(
		ctx, containerID, client.CopyToContainerOptions{DestinationPath: destination, Content: bytes.NewReader(archive)},
	); err != nil {
		return fmt.Errorf("copy startup file to Project container %s: %w", containerID, err)
	}
	return nil
}

func startupFileArchive(filePath string, payload []byte) ([]byte, error) {
	return startupFileArchiveWithMode(filePath, payload, 0o644)
}

func startupFileArchiveWithMode(filePath string, payload []byte, mode int64) ([]byte, error) {
	relative := strings.TrimPrefix(path.Clean(filePath), "/")
	parts := strings.Split(relative, "/")
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for index := 1; index < len(parts); index++ {
		name := strings.Join(parts[:index], "/")
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			return nil, fmt.Errorf("write startup file directory archive: %w", err)
		}
	}
	if err := archive.WriteHeader(&tar.Header{
		Name: relative, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(payload)),
	}); err != nil {
		return nil, fmt.Errorf("write startup file archive: %w", err)
	}
	if _, err := archive.Write(payload); err != nil {
		return nil, fmt.Errorf("write startup file payload archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close startup file archive: %w", err)
	}
	return buffer.Bytes(), nil
}
