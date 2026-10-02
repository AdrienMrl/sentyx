// event-export streams only explicitly requested Sentry MP4s from a read-only image.
package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/AdrienMrl/teslcam/internal/exfat"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

type request struct {
	Path  string `json:"path"`
	Bytes uint64 `json:"bytes"`
}
type receipt struct {
	Path   string `json:"path"`
	Bytes  uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func validate(reqs []request, max uint64, count int) error {
	if len(reqs) == 0 || len(reqs) > count {
		return fmt.Errorf("invalid request count")
	}
	seen := map[string]bool{}
	var total uint64
	for _, r := range reqs {
		parts := strings.Split(r.Path, "/")
		if len(parts) != 2 || path.Clean(r.Path) != r.Path || len(parts[1]) < 24 || !strings.HasSuffix(parts[1], ".mp4") {
			return fmt.Errorf("invalid path %q", r.Path)
		}
		if _, err := time.Parse("2006-01-02_15-04-05", parts[0]); err != nil {
			return err
		}
		if _, err := time.Parse("2006-01-02_15-04-05", parts[1][:19]); err != nil {
			return err
		}
		if seen[r.Path] || r.Bytes == 0 || r.Bytes > max-total {
			return fmt.Errorf("duplicate, empty or over-budget request")
		}
		seen[r.Path] = true
		total += r.Bytes
	}
	return nil
}

func export(v *exfat.Volume, reqs []request, w io.Writer) error {
	tw := tar.NewWriter(w)
	receipts := []receipt{}
	for _, req := range reqs {
		p := "/TeslaCam/SentryClips/" + req.Path
		e, err := v.Lookup(p)
		if err != nil {
			return err
		}
		if e.IsDir() || !e.ChecksumOK || e.ValidDataLength != req.Bytes || e.DataLength != req.Bytes {
			return fmt.Errorf("entry changed or incomplete: %s", p)
		}
		first, err := v.Open(e)
		if err != nil {
			return err
		}
		h := sha256.New()
		if n, err := io.Copy(h, first); err != nil || uint64(n) != req.Bytes {
			return fmt.Errorf("first read failed: %s (%v)", p, err)
		}
		sum := hex.EncodeToString(h.Sum(nil))
		if err := tw.WriteHeader(&tar.Header{Name: req.Path, Mode: 0600, Size: int64(req.Bytes), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		second, err := v.Open(e)
		if err != nil {
			return err
		}
		h.Reset()
		n, err := io.Copy(io.MultiWriter(tw, h), second)
		if err != nil || uint64(n) != req.Bytes || hex.EncodeToString(h.Sum(nil)) != sum {
			return fmt.Errorf("second read changed: %s (%v)", p, err)
		}
		after, err := v.Lookup(p)
		if err != nil || !after.ChecksumOK || after.FirstCluster != e.FirstCluster || after.ValidDataLength != e.ValidDataLength || after.DataLength != e.DataLength || !after.Modified.Equal(e.Modified) {
			return fmt.Errorf("entry changed during export: %s", p)
		}
		receipts = append(receipts, receipt{req.Path, req.Bytes, sum})
		fmt.Fprintln(os.Stderr, "verified", req.Path)
	}
	b, err := json.Marshal(receipts)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "MANIFEST.json", Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(b); err != nil {
		return err
	}
	return tw.Close()
}

func main() {
	image := flag.String("image", "", "required read-only image path")
	max := flag.Uint64("max-bytes", 0, "required total payload byte ceiling")
	count := flag.Int("max-files", 0, "required request count ceiling")
	flag.Parse()
	if *image == "" || *max == 0 || *max > 1<<30 || *count <= 0 {
		panic("explicit image, max-bytes <= 1 GiB, and max-files required")
	}
	var reqs []request
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&reqs); err != nil {
		panic(err)
	}
	if err := validate(reqs, *max, *count); err != nil {
		panic(err)
	}
	f, err := os.Open(*image)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	v, err := exfat.LocateVolume(f)
	if err != nil {
		panic(err)
	}
	if err := export(v, reqs, os.Stdout); err != nil {
		panic(err)
	}
}
