package exfat

import (
	"fmt"
	"io"
	"strings"
)

// ReadDirPath lists the directory at a /-separated path like
// "TeslaCam/SentryClips". An empty path lists the root. Name comparison is
// case-insensitive (exFAT is a case-insensitive filesystem).
func (v *Volume) ReadDirPath(path string) ([]DirEntry, error) {
	first, noFat, size := v.BS.RootDirCluster, false, int64(0)
	for _, part := range splitPath(path) {
		entries, err := v.ReadDirectory(first, noFat, size)
		if err != nil {
			return nil, err
		}
		e := findName(entries, part)
		if e == nil {
			return nil, fmt.Errorf("exfat: %q not found", part)
		}
		if !e.IsDir() {
			return nil, fmt.Errorf("exfat: %q is not a directory", part)
		}
		first, noFat, size = e.FirstCluster, e.NoFatChain, int64(e.DataLength)
	}
	return v.ReadDirectory(first, noFat, size)
}

// Lookup resolves a /-separated path to its directory entry.
func (v *Volume) Lookup(path string) (*DirEntry, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil, fmt.Errorf("exfat: empty path")
	}
	dir, leaf := parts[:len(parts)-1], parts[len(parts)-1]
	entries, err := v.ReadDirPath(strings.Join(dir, "/"))
	if err != nil {
		return nil, err
	}
	e := findName(entries, leaf)
	if e == nil {
		return nil, fmt.Errorf("exfat: %q not found", path)
	}
	return e, nil
}

// Open returns a reader over a file entry's contents. It reads
// ValidDataLength bytes: on a live volume, bytes past ValidDataLength are
// allocated but not yet written (uninitialized), so they are never returned.
func (v *Volume) Open(e *DirEntry) (io.Reader, error) {
	if e.IsDir() {
		return nil, fmt.Errorf("exfat: %q is a directory", e.Name)
	}
	if e.ValidDataLength == 0 || e.FirstCluster == 0 {
		return strings.NewReader(""), nil
	}
	var chain []uint32
	var err error
	if e.NoFatChain {
		chain, err = v.ContiguousChain(e.FirstCluster, int64(e.DataLength))
	} else {
		chain, err = v.Chain(e.FirstCluster)
	}
	// A torn chain can still cover ValidDataLength; only fail if it doesn't.
	cs := v.BS.ClusterSize()
	if int64(len(chain))*cs < int64(e.ValidDataLength) {
		if err != nil {
			return nil, fmt.Errorf("exfat: chain too short for %q (%d clusters for %d bytes): %w",
				e.Name, len(chain), e.ValidDataLength, err)
		}
		return nil, fmt.Errorf("exfat: chain too short for %q (%d clusters for %d bytes)",
			e.Name, len(chain), e.ValidDataLength)
	}
	return &chainReader{v: v, chain: chain, size: int64(e.ValidDataLength)}, nil
}

type chainReader struct {
	v     *Volume
	chain []uint32
	size  int64
	pos   int64
}

func (r *chainReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	cs := r.v.BS.ClusterSize()
	clusterIdx := r.pos / cs
	within := r.pos % cs
	off, err := r.v.BS.ClusterByteOffset(r.chain[clusterIdx])
	if err != nil {
		return 0, err
	}
	// Read at most to the end of this cluster and the end of the file.
	n := cs - within
	if rem := r.size - r.pos; rem < n {
		n = rem
	}
	if int64(len(p)) < n {
		n = int64(len(p))
	}
	m, err := r.v.r.ReadAt(p[:n], off+within)
	r.pos += int64(m)
	return m, err
}

func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func findName(entries []DirEntry, name string) *DirEntry {
	for i := range entries {
		if strings.EqualFold(entries[i].Name, name) {
			return &entries[i]
		}
	}
	return nil
}
