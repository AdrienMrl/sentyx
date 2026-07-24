package lte

import (
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestRealFallback exercises the full LTE fallback on real hardware: DNS over
// the LTE resolver, TCP bound to the dongle interface, TLS to the production
// server. Opt-in (it spends metered bytes); run on the Pi with:
//
//	TESLCAM_LTE_REAL_URL=https://teslcam.161-35-232-246.sslip.io/healthz \
//	  ./lte.test -test.run TestRealFallback -test.v
//
// To force the fallback (rather than just proving the primary path), block the
// server over Wi-Fi first — from a root shell:
//
//	nft add table inet ltetest
//	nft add chain inet ltetest out '{ type filter hook output priority 0; }'
//	nft add rule inet ltetest out oifname "wlan0" ip daddr 161.35.232.246 drop
//
// and `nft delete table inet ltetest` afterwards.
func TestRealFallback(t *testing.T) {
	url := os.Getenv("TESLCAM_LTE_REAL_URL")
	if url == "" {
		t.Skip("TESLCAM_LTE_REAL_URL not set; skipping metered-hardware test")
	}
	tr, err := NewTransport(FallbackConfig{
		Interface:   "eth1",
		DNS:         "8.8.8.8:53",
		DialTimeout: 8 * time.Second,
		Logf:        t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	t.Logf("GET %s -> %d %q", url, resp.StatusCode, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
