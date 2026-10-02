package eventupload

import (
	"context"
	"testing"
)

func TestDiscardedPendingNotReconciled(t *testing.T) {
	dir := t.TempDir()
	cfg := durableCfg(dir)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := "/TeslaCam/SentryClips/2026-09-01_00-00-00/clip.mp4"
	c.Enqueue(Item{ImagePath: path, LocalPath: writeSpool(t, dir, "clip.mp4", 100)})
	c.store.close()
	cfg.Retain = func(string) bool { return false }
	c, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.Enqueue(Item{ImagePath: path}) {
		t.Fatal("discarded item re-enqueued")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Run(ctx, func(Item) { t.Fatal("discarded upload completed") }, func(Item, error) { t.Fatal("discarded upload retried") }, func(string, int) { t.Fatal("discarded event finalized") })
	store, err := openSpoolStore(cfg.SpoolDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	ids, err := store.sourceIDs()
	if err != nil || len(ids) != 0 {
		t.Fatalf("stale durable records: %v %v", ids, err)
	}
}
