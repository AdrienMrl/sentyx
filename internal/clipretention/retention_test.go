package clipretention

import (
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, max int64) *Manager {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(root, "/TeslaCam/SentryClips", max, 100, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	m.free = func() (int64, error) { return 10000, nil }
	return m
}
func put(t *testing.T, m *Manager, id string, size int) {
	t.Helper()
	dir := filepath.Join(m.root, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), make([]byte, size), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestCapAllFilesAndRestart(t *testing.T) {
	m := fixture(t, 100)
	put(t, m, "2026-09-01_00-00-00", 70)
	put(t, m, "2026-09-02_00-00-00", 70)
	if err := m.Sweep(); err != nil {
		t.Fatal(err)
	}
	old := "/TeslaCam/SentryClips/2026-09-01_00-00-00/clip.mp4"
	if m.Allowed(old) {
		t.Fatal("discarded event allowed")
	}
	if _, err := os.Stat(filepath.Join(m.root, "2026-09-01_00-00-00")); !os.IsNotExist(err) {
		t.Fatal("old untracked footage retained")
	}
	again, err := New(filepath.Dir(m.state), m.prefix, 100, 100, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if again.Allowed(old) {
		t.Fatal("restart forgot discard")
	}
	if !again.Allowed("/TeslaCam/SentryClips/2026-09-02_00-00-00/clip.mp4") {
		t.Fatal("new event lost")
	}
}
func TestReserveAndAdmission(t *testing.T) {
	m := fixture(t, 1000)
	put(t, m, "2026-09-01_00-00-00", 60)
	m.free = func() (int64, error) {
		if _, err := os.Stat(filepath.Join(m.root, "2026-09-01_00-00-00")); os.IsNotExist(err) {
			return 160, nil
		}
		return 100, nil
	}
	release, err := m.Begin("/TeslaCam/SentryClips/2026-09-02_00-00-00/clip.mp4", 50)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if m.cutoff != "2026-09-01_00-00-00" {
		t.Fatal("reserve did not evict")
	}
	m.free = func() (int64, error) { return 99, nil }
	if release, err = m.Begin("/TeslaCam/SentryClips/2026-09-03_00-00-00/clip.mp4", 1); err == nil {
		release()
		t.Fatal("admitted write below reserve")
	}
}
func TestCrashAfterWatermarkBeforeDelete(t *testing.T) {
	m := fixture(t, 1000)
	put(t, m, "2026-09-01_00-00-00", 1)
	if err := m.persist("2026-09-01_00-00-00"); err != nil {
		t.Fatal(err)
	}
	if err := m.Sweep(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, m.cutoff)); !os.IsNotExist(err) {
		t.Fatal("unfinished deletion not replayed")
	}
}
func TestRejectUnsafeTreeAndOversizedWrite(t *testing.T) {
	m := fixture(t, 100)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(m.root, "2026-09-01_00-00-00")); err != nil {
		t.Fatal(err)
	}
	if err := m.Sweep(); err == nil {
		t.Fatal("symlink accepted")
	}
	if release, err := m.Begin("/TeslaCam/SentryClips/2026-09-02_00-00-00/x", 101); err == nil {
		release()
		t.Fatal("oversized write accepted")
	}
	if m.Allowed("/TeslaCam/SentryClips/../../backing.img") {
		t.Fatal("unsafe event allowed")
	}
}
func TestWriteReservationBlocksSweep(t *testing.T) {
	m := fixture(t, 100)
	release, err := m.Begin("/TeslaCam/SentryClips/2026-09-02_00-00-00/x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.mu.TryLock() {
		m.mu.Unlock()
		t.Fatal("write not protected")
	}
	release()
	if err := m.Sweep(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleRebaselineCannotEvictNewerFootage(t *testing.T) {
	m := fixture(t, 100)
	put(t, m, "2026-09-02_00-00-00", 100)
	if err := m.persist("2026-09-01_00-00-00"); err != nil {
		t.Fatal(err)
	}
	if release, err := m.Begin("/TeslaCam/SentryClips/2026-09-01_00-00-00/x", 100); err == nil {
		release()
		t.Fatal("old write accepted")
	}
	if _, err := os.Stat(filepath.Join(m.root, "2026-09-02_00-00-00", "clip.mp4")); err != nil {
		t.Fatal("newer footage evicted by stale work")
	}
	if m.Allowed("/TeslaCam/SentryClips/2026-09-02_00-00-00/../../../backing.img") {
		t.Fatal("traversal accepted")
	}
}
