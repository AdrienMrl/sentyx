// event-inventory reads only directory entries and event.json, never MP4 data.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/clipselect"
	"github.com/AdrienMrl/teslcam/internal/exfat"
)

type event struct {
	Name     string            `json:"event"`
	Metadata json.RawMessage   `json:"metadata,omitempty"`
	Files    map[string]uint64 `json:"files,omitempty"`
	Selected string            `json:"trigger_camera_segment,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func metadata(v *exfat.Volume, p string, max uint64) ([]byte, error) {
	e, err := v.Lookup(p)
	if err != nil {
		return nil, err
	}
	if !e.ChecksumOK || e.IsDir() || e.ValidDataLength == 0 || e.DataLength > max || e.ValidDataLength > max {
		return nil, fmt.Errorf("metadata entry invalid, incomplete or oversized")
	}
	r, err := v.Open(e)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(b)) != e.ValidDataLength || !json.Valid(b) {
		return nil, fmt.Errorf("metadata invalid JSON or incomplete")
	}
	return b, nil
}

func inventory(v *exfat.Volume, limit int, max uint64) ([]event, error) {
	const root = "/TeslaCam/SentryClips"
	dirs, err := v.ReadDirPath(root)
	if err != nil {
		return nil, err
	}
	if len(dirs) > limit {
		return nil, fmt.Errorf("directory entries exceed explicit limit %d", limit)
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	result := []event{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		row := event{Name: d.Name, Files: map[string]uint64{}}
		if _, err := time.Parse("2006-01-02_15-04-05", d.Name); err != nil || !d.ChecksumOK {
			row.Error = "invalid event directory"
			result = append(result, row)
			continue
		}
		p := path.Join(root, d.Name)
		files, err := v.ReadDirPath(p)
		if err != nil {
			row.Error = err.Error()
			result = append(result, row)
			continue
		}
		names := []string{}
		for _, f := range files {
			if !f.IsDir() && f.ChecksumOK && strings.HasSuffix(f.Name, ".mp4") {
				row.Files[f.Name] = f.ValidDataLength
				names = append(names, f.Name)
			}
		}
		a, err := metadata(v, path.Join(p, "event.json"), max)
		if err == nil {
			b, e := metadata(v, path.Join(p, "event.json"), max)
			if e != nil {
				err = e
			} else if !bytes.Equal(a, b) {
				err = fmt.Errorf("metadata changed across reads")
			}
		}
		if err == nil {
			row.Metadata = a
			m, e := clipselect.ParseEventJSON(a)
			if e == nil {
				_, e = time.Parse("2006-01-02T15:04:05", m.Timestamp)
			}
			if e == nil {
				row.Selected, e = clipselect.Select(names, m)
			}
			err = e
		}
		if err != nil {
			row.Error = err.Error()
		}
		result = append(result, row)
	}
	return result, nil
}

func main() {
	image := flag.String("image", "", "required image path, opened read-only")
	limit := flag.Int("max-events", 0, "required maximum event directory entries")
	max := flag.Uint64("max-metadata-bytes", 0, "required metadata byte ceiling")
	flag.Parse()
	if *image == "" || *limit <= 0 || *max == 0 || *max > 1<<20 {
		fmt.Fprintln(os.Stderr, "explicit image, positive max-events and metadata limit <=1 MiB required")
		os.Exit(2)
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
	rows, err := inventory(v, *limit, *max)
	if err != nil {
		panic(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(struct {
		Events []event `json:"events"`
		Caveat string  `json:"caveat"`
	}{rows, "Live read-only observation, not an atomic snapshot. Metadata double-read; selected camera mapping is clipselect, not learned camera scoring."}); err != nil {
		panic(err)
	}
}
