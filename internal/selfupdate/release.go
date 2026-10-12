package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// A release is an archive and nothing else: it is never written to disk before
// its checksum matches. This is the reason an archive is held in memory rather
// than streamed to a file — a release is a few megabytes, and a temporary file
// that turns out to be a bad download is a file somebody has to clean up, which
// is exactly what "a mismatch stops, and nothing changes" rules out.
//
// 200 MiB is far above any mnemo archive and far below anything that would hurt
// a machine. Without a ceiling, a body that never ends is a process that grows
// until the kernel kills it.
const mostBytes = 200 << 20

// latestVersion asks the release index for the newest tag.
func latestVersion(ctx context.Context, opts Options) (string, error) {
	body, err := fetch(ctx, opts, opts.LatestRelease)
	if err != nil {
		return "", fmt.Errorf("asking for the latest release: %w", err)
	}
	var index struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		return "", fmt.Errorf("reading the latest release: %s did not answer with a release, so mnemo cannot "+
			"tell which version is the newest", opts.LatestRelease)
	}
	if !version.MatchString(strings.TrimSpace(index.Tag)) {
		return "", fmt.Errorf("the latest release is tagged %q, which is not a version mnemo understands; "+
			"name the one you want with --version", index.Tag)
	}
	return plainVersion(index.Tag), nil
}

// archiveName is the file GoReleaser publishes for one build, as the Build
// section of docs/specs/release.md names it.
func archiveName(version, goos, arch string) string {
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return fmt.Sprintf("mnemo_%s_%s_%s%s", version, goos, arch, extension)
}

// download fetches the archive for this build and the checksums beside it, and
// answers with the archive only once the two agree.
func download(ctx context.Context, opts Options, version string) ([]byte, error) {
	name := archiveName(version, opts.OS, opts.Arch)
	// The tag carries its v, because that is the tag; the name inside does not,
	// because that is what GoReleaser writes.
	base := strings.TrimSuffix(opts.Downloads, "/") + "/v" + version + "/"

	archive, err := fetch(ctx, opts, base+name)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", name, err)
	}
	checksums, err := fetch(ctx, opts, base+"checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("downloading the checksums for %s: %w", version, err)
	}

	want, listed := checksumFor(string(checksums), name)
	if !listed {
		return nil, fmt.Errorf("checksums.txt for %s does not list %s, so the download cannot be verified and "+
			"nothing was changed", version, name)
	}
	got := sha256.Sum256(archive)
	if !strings.EqualFold(want, hex.EncodeToString(got[:])) {
		return nil, refuse("The download of %s does not match its checksum, so it was thrown away and nothing "+
			"was changed. Try again; if it keeps happening, something between you and the release is altering "+
			"the file.", name)
	}
	return archive, nil
}

// checksumFor finds one file's SHA-256 in a checksums.txt, whose lines are the
// hash, spaces, and the name.
func checksumFor(checksums, name string) (string, bool) {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// The name may carry a leading `*`, which sha256sum writes for a file it
		// read in binary mode.
		if strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			return fields[0], true
		}
	}
	return "", false
}

func fetch(ctx context.Context, opts Options, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := opts.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// 404 is the common one and it is not a fault: it is a version that does
		// not exist, or one that has no build for this machine.
		if response.StatusCode == http.StatusNotFound {
			return nil, refuse("%s is not there (404). Check the version, and that there is a build for %s/%s.",
				url, opts.OS, opts.Arch)
		}
		return nil, fmt.Errorf("%s answered %s", url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, mostBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > mostBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes, which no mnemo release is", url, mostBytes)
	}
	return body, nil
}

// binaryName is what the archive calls the executable.
func binaryName(goos string) string {
	if goos == "windows" {
		return "mnemo.exe"
	}
	return "mnemo"
}

// binaryIn pulls the mnemo binary out of an archive.
//
// Only that one file is taken, and only by its base name: an archive also holds
// the licence and the README, and an archive from somewhere else may hold
// anything at all. Nothing is extracted by the path the archive gives, so no
// entry can write outside where mnemo means to write.
func binaryIn(archive []byte, goos string) ([]byte, error) {
	wanted := binaryName(goos)
	var found []byte
	var err error
	if goos == "windows" {
		found, err = fromZip(archive, wanted)
	} else {
		found, err = fromTarGz(archive, wanted)
	}
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("the archive holds no %s, so it is not a mnemo release archive and nothing was "+
			"changed", wanted)
	}
	return found, nil
}

func fromTarGz(archive []byte, wanted string) ([]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("the archive is not a gzip file, so it is not a mnemo release archive: %w", err)
	}
	defer compressed.Close()
	entries := tar.NewReader(compressed)
	for {
		header, err := entries.Next()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading the archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || path.Base(header.Name) != wanted {
			continue
		}
		return io.ReadAll(io.LimitReader(entries, mostBytes))
	}
}

func fromZip(archive []byte, wanted string) ([]byte, error) {
	entries, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("the archive is not a zip file, so it is not a mnemo release archive: %w", err)
	}
	for _, entry := range entries.File {
		if entry.FileInfo().IsDir() || path.Base(entry.Name) != wanted {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s out of the archive: %w", entry.Name, err)
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, mostBytes))
	}
	return nil, nil
}
