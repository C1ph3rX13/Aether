package database

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const offlineDataRecoveryPointHeader = "aether-offline-data-recovery-point/v1"

type verifiedOfflineDataRecoveryPoint struct {
	recoveryPointPath string
	manifestChecksum  [sha256.Size]byte
}

func loadOfflineDataRecoveryPoint(
	markerPath string,
	databasePath string,
) (*verifiedOfflineDataRecoveryPoint, bool, error) {
	marker, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(marker)), "\n")
	if len(lines) != 3 || lines[0] != offlineDataRecoveryPointHeader {
		return nil, true, fmt.Errorf("invalid recovery point format")
	}
	recoveryPointPath, ok := strings.CutPrefix(lines[1], "recovery_point_path=")
	if !ok || recoveryPointPath == "" || !filepath.IsAbs(recoveryPointPath) || filepath.Clean(recoveryPointPath) != recoveryPointPath {
		return nil, true, fmt.Errorf("invalid recovery point path")
	}
	manifestHex, ok := strings.CutPrefix(lines[2], "manifest_sha256=")
	if !ok {
		return nil, true, fmt.Errorf("invalid recovery point manifest checksum")
	}
	manifestChecksum, err := parseSHA256(manifestHex)
	if err != nil {
		return nil, true, fmt.Errorf("invalid recovery point manifest checksum: %w", err)
	}
	if err := verifyOfflineDataRecoveryPoint(recoveryPointPath, filepath.Base(databasePath), manifestChecksum); err != nil {
		return nil, true, err
	}
	return &verifiedOfflineDataRecoveryPoint{
		recoveryPointPath: recoveryPointPath, manifestChecksum: manifestChecksum,
	}, true, nil
}

func verifyOfflineDataRecoveryPoint(recoveryPointPath, databaseFilename string, wantManifestChecksum [sha256.Size]byte) error {
	recoveryPointInfo, err := os.Lstat(recoveryPointPath)
	if err != nil {
		return fmt.Errorf("recovery point is unavailable: %w", err)
	}
	if !recoveryPointInfo.IsDir() || recoveryPointInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("recovery point is not a directory")
	}
	manifestPath := filepath.Join(recoveryPointPath, "SHA256SUMS")
	if _, err := recoveryPointEntry(recoveryPointPath, "SHA256SUMS"); err != nil {
		return err
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read recovery point checksums: %w", err)
	}
	gotManifestChecksum := sha256.Sum256(manifest)
	if gotManifestChecksum != wantManifestChecksum {
		return fmt.Errorf("recovery point manifest checksum mismatch")
	}

	wantDatabaseEntry := filepath.ToSlash(filepath.Join("data", databaseFilename))
	databaseFound := false
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(strings.NewReader(string(manifest)))
	for scanner.Scan() {
		line := scanner.Text()
		checksumHex, relativePath, ok := strings.Cut(line, "  ")
		if !ok || strings.HasPrefix(line, `\`) {
			return fmt.Errorf("invalid recovery point checksum entry")
		}
		wantChecksum, err := parseSHA256(checksumHex)
		if err != nil {
			return fmt.Errorf("invalid recovery point file checksum: %w", err)
		}
		cleanRelativePath := filepath.Clean(filepath.FromSlash(relativePath))
		if filepath.IsAbs(cleanRelativePath) || cleanRelativePath == "." ||
			cleanRelativePath == ".." || strings.HasPrefix(cleanRelativePath, ".."+string(filepath.Separator)) ||
			(cleanRelativePath != "data" && !strings.HasPrefix(cleanRelativePath, "data"+string(filepath.Separator))) {
			return fmt.Errorf("invalid recovery point checksum path %q", relativePath)
		}
		if _, duplicate := seen[cleanRelativePath]; duplicate {
			return fmt.Errorf("duplicate recovery point checksum path %q", relativePath)
		}
		seen[cleanRelativePath] = struct{}{}
		filePath := filepath.Join(recoveryPointPath, cleanRelativePath)
		fileInfo, err := recoveryPointEntry(recoveryPointPath, cleanRelativePath)
		if err != nil {
			return fmt.Errorf("read recovery point file %q: %w", relativePath, err)
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("recovery point file %q is not regular", relativePath)
		}
		gotChecksum, err := fileSHA256(filePath)
		if err != nil {
			return fmt.Errorf("checksum recovery point file %q: %w", relativePath, err)
		}
		if gotChecksum != wantChecksum {
			return fmt.Errorf("recovery point file checksum mismatch: %s", relativePath)
		}
		if filepath.ToSlash(cleanRelativePath) == wantDatabaseEntry {
			if fileInfo.Size() == 0 {
				return fmt.Errorf("recovery point database is empty")
			}
			databaseFound = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan recovery point checksums: %w", err)
	}
	if !databaseFound {
		return fmt.Errorf("recovery point does not contain database %q", databaseFilename)
	}
	return nil
}

func recoveryPointEntry(recoveryPointPath, relativePath string) (os.FileInfo, error) {
	currentPath := recoveryPointPath
	components := strings.Split(filepath.Clean(relativePath), string(filepath.Separator))
	var entry os.FileInfo
	for index, component := range components {
		currentPath = filepath.Join(currentPath, component)
		var err error
		entry, err = os.Lstat(currentPath)
		if err != nil {
			return nil, err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("recovery point path %q is a symbolic link", relativePath)
		}
		if index < len(components)-1 && !entry.IsDir() {
			return nil, fmt.Errorf("recovery point path %q contains a non-directory component", relativePath)
		}
	}
	return entry, nil
}

func parseSHA256(value string) ([sha256.Size]byte, error) {
	var checksum [sha256.Size]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return checksum, fmt.Errorf("must be %d hexadecimal bytes", sha256.Size)
	}
	copy(checksum[:], decoded)
	return checksum, nil
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	var checksum [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return checksum, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return checksum, err
	}
	copy(checksum[:], hash.Sum(nil))
	return checksum, nil
}
