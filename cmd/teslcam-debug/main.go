// teslcam-debug is a development server that exposes a debug UI + API for
// exercising the exFAT live-reader against a mounted volume.
//
// It sits on both sides of the architecture at once:
//   - writer actions (fake Tesla) go through the OS-mounted filesystem
//     (-mount), exactly like the car writing to the gadget LUN;
//   - read views come from our own out-of-band exFAT parser reading the raw
//     backing image (-image), never the mount.
//
// In the Lima VM: -image is the g_mass_storage backing file, -mount is where
// the host side mounted /dev/sda. On a Mac: -image is a raw image attached
// with hdiutil, -mount its /Volumes mountpoint.
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AdrienMrl/teslcam/internal/exfat"
)

//go:embed ui
var uiFS embed.FS

func main() {
	imagePath := flag.String("image", "", "path to the raw exFAT backing image (read out-of-band)")
	mountPath := flag.String("mount", "", "path to the OS-mounted volume (writer actions)")
	listen := flag.String("listen", "", "listen address, e.g. 0.0.0.0:8080")
	flag.Parse()
	if *imagePath == "" || *mountPath == "" || *listen == "" {
		log.Fatal("all of -image, -mount, -listen are required")
	}
	if _, err := os.Stat(*imagePath); err != nil {
		log.Fatalf("-image: %v", err)
	}
	if st, err := os.Stat(*mountPath); err != nil || !st.IsDir() {
		log.Fatalf("-mount: not a directory (%v)", err)
	}

	s := &server{imagePath: *imagePath, mountPath: *mountPath}
	ui, err := fs.Sub(uiFS, "ui")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("POST /api/actions/sentry-event", s.handleSentryEvent)
	mux.HandleFunc("POST /api/actions/stream/start", s.handleStreamStart)
	mux.HandleFunc("POST /api/actions/stream/append", s.handleStreamAppend)
	mux.HandleFunc("POST /api/actions/stream/finish", s.handleStreamFinish)
	mux.HandleFunc("POST /api/actions/sync", s.handleSync)

	log.Printf("teslcam-debug: image=%s mount=%s listening on http://%s", *imagePath, *mountPath, *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}

type server struct {
	imagePath string
	mountPath string

	mu      sync.Mutex
	streams map[string]*stream
}

type stream struct {
	f      *os.File
	relDir string // event dir relative to mount, e.g. TeslaCam/SentryClips/<ts>
	name   string // file name within the dir
	wrote  int64
}

// ---- read side (out-of-band exFAT parse of the raw image) ----

func (s *server) openVolume() (*exfat.Volume, *os.File, error) {
	f, err := os.Open(s.imagePath)
	if err != nil {
		return nil, nil, err
	}
	v, err := exfat.NewVolume(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return v, f, nil
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	v, f, err := s.openVolume()
	if err != nil {
		httpErr(w, err)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	bs := v.BS
	writeJSON(w, map[string]any{
		"imagePath":      s.imagePath,
		"mountPath":      s.mountPath,
		"imageSizeBytes": st.Size(),
		"volumeSerial":   fmt.Sprintf("%08X", bs.VolumeSerial),
		"bytesPerSector": bs.BytesPerSector(),
		"clusterSize":    bs.ClusterSize(),
		"clusterCount":   bs.ClusterCount,
		"fatOffset":      bs.FatOffset,
		"rootDirCluster": bs.RootDirCluster,
		"dirty":          bs.Dirty(),
		"time":           time.Now().Format(time.RFC3339),
	})
}

type treeNode struct {
	Name       string     `json:"name"`
	IsDir      bool       `json:"isDir"`
	Size       uint64     `json:"size"`
	ValidSize  uint64     `json:"validSize"`
	Modified   string     `json:"modified"`
	ChecksumOK bool       `json:"checksumOK"`
	NoFatChain bool       `json:"noFatChain"`
	Children   []treeNode `json:"children,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func (s *server) handleTree(w http.ResponseWriter, r *http.Request) {
	v, f, err := s.openVolume()
	if err != nil {
		httpErr(w, err)
		return
	}
	defer f.Close()
	root := treeNode{Name: "/", IsDir: true}
	root.Children, root.Error = walkDir(v, v.BS.RootDirCluster, false, 0, 8)
	writeJSON(w, root)
}

func walkDir(v *exfat.Volume, firstCluster uint32, noFatChain bool, dataLength int64, depth int) ([]treeNode, string) {
	entries, err := v.ReadDirectory(firstCluster, noFatChain, dataLength)
	var errStr string
	if err != nil {
		errStr = err.Error() // partial listings still render
	}
	nodes := make([]treeNode, 0, len(entries))
	for _, e := range entries {
		n := treeNode{
			Name:       e.Name,
			IsDir:      e.IsDir(),
			Size:       e.DataLength,
			ValidSize:  e.ValidDataLength,
			Modified:   e.Modified.Format(time.RFC3339),
			ChecksumOK: e.ChecksumOK,
			NoFatChain: e.NoFatChain,
		}
		if e.IsDir() && depth > 0 && e.FirstCluster >= 2 {
			n.Children, n.Error = walkDir(v, e.FirstCluster, e.NoFatChain, int64(e.DataLength), depth-1)
		}
		nodes = append(nodes, n)
	}
	return nodes, errStr
}

// ---- write side (fake Tesla, through the OS mount) ----

// pattern writes n bytes of the deterministic fixture pattern.
func writePattern(f *os.File, tag string, n int64) (int64, error) {
	line := []byte("TESLCAM-FIXTURE:" + tag + "\n")
	var wrote int64
	buf := make([]byte, 0, 64*1024)
	for int64(len(buf)) < 64*1024 {
		buf = append(buf, line...)
	}
	for wrote < n {
		chunk := int64(len(buf))
		if n-wrote < chunk {
			chunk = n - wrote
		}
		m, err := f.Write(buf[:chunk])
		wrote += int64(m)
		if err != nil {
			return wrote, err
		}
	}
	return wrote, nil
}

var cameras = []string{"front", "back", "left_repeater", "right_repeater"}

func (s *server) handleSentryEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SizeKBPerCamera int64 `json:"sizeKBPerCamera"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SizeKBPerCamera <= 0 {
		httpErrf(w, http.StatusBadRequest, "sizeKBPerCamera (positive int) required")
		return
	}
	now := time.Now()
	eventDir := now.Format("2006-01-02_15-04-05")
	clipStamp := now.Add(-30 * time.Second).Format("2006-01-02_15-04-05")
	dir := filepath.Join(s.mountPath, "TeslaCam", "SentryClips", eventDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpErr(w, err)
		return
	}
	var files []string
	for _, cam := range cameras {
		name := fmt.Sprintf("%s-%s.mp4", clipStamp, cam)
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			httpErr(w, err)
			return
		}
		_, werr := writePattern(f, cam, req.SizeKBPerCamera*1024)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			httpErrf(w, 500, "writing %s: %v %v", name, werr, cerr)
			return
		}
		files = append(files, name)
	}
	eventJSON := fmt.Sprintf(`{"timestamp":"%s","city":"North Las Vegas","est_lat":"36.28","est_lon":"-115.13","reason":"sentry_aware_object_detection","camera":"5"}`,
		now.Format("2006-01-02T15:04:05"))
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(eventJSON), 0o644); err != nil {
		httpErr(w, err)
		return
	}
	files = append(files, "event.json")
	writeJSON(w, map[string]any{"eventDir": "TeslaCam/SentryClips/" + eventDir, "files": files})
}

func (s *server) handleStreamStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Camera string `json:"camera"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Camera == "" {
		httpErrf(w, http.StatusBadRequest, "camera required")
		return
	}
	now := time.Now()
	relDir := filepath.Join("TeslaCam", "SentryClips", now.Format("2006-01-02_15-04-05"))
	name := fmt.Sprintf("%s-%s.mp4", now.Format("2006-01-02_15-04-05"), req.Camera)
	if err := os.MkdirAll(filepath.Join(s.mountPath, relDir), 0o755); err != nil {
		httpErr(w, err)
		return
	}
	f, err := os.Create(filepath.Join(s.mountPath, relDir, name))
	if err != nil {
		httpErr(w, err)
		return
	}
	id := fmt.Sprintf("s%d", now.UnixNano())
	s.mu.Lock()
	if s.streams == nil {
		s.streams = map[string]*stream{}
	}
	s.streams[id] = &stream{f: f, relDir: relDir, name: name}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "path": filepath.Join(relDir, name)})
}

func (s *server) streamByID(id string) *stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

func (s *server) handleStreamAppend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     string `json:"id"`
		SizeKB int64  `json:"sizeKB"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" || req.SizeKB <= 0 {
		httpErrf(w, http.StatusBadRequest, "id and sizeKB (positive int) required")
		return
	}
	st := s.streamByID(req.ID)
	if st == nil {
		httpErrf(w, http.StatusNotFound, "unknown stream %q", req.ID)
		return
	}
	n, err := writePattern(st.f, "stream", req.SizeKB*1024)
	st.wrote += n
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": filepath.Join(st.relDir, st.name), "totalBytes": st.wrote})
}

func (s *server) handleStreamFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		httpErrf(w, http.StatusBadRequest, "id required")
		return
	}
	s.mu.Lock()
	st := s.streams[req.ID]
	delete(s.streams, req.ID)
	s.mu.Unlock()
	if st == nil {
		httpErrf(w, http.StatusNotFound, "unknown stream %q", req.ID)
		return
	}
	if err := st.f.Close(); err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"path": filepath.Join(st.relDir, st.name), "totalBytes": st.wrote})
}

func (s *server) handleSync(w http.ResponseWriter, r *http.Request) {
	// Flush page cache to the backing store so out-of-band reads see the
	// latest writes. Global sync is crude but exactly what we want here.
	if err := syncAll(); err != nil {
		httpErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"synced": true})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	httpErrf(w, http.StatusInternalServerError, "%v", err)
}

func httpErrf(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": strings.TrimSpace(fmt.Sprintf(format, args...))})
}
