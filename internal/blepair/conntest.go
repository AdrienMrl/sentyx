package blepair

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpConnTester verifies the received config against the server: first the
// open /healthz, then an authenticated endpoint with the new device token.
// Only a passing auth step lets the config persist.
type httpConnTester struct {
	client   *http.Client
	deviceID string
}

func newHTTPConnTester(deviceID string) *httpConnTester {
	return &httpConnTester{
		client:   &http.Client{Timeout: 15 * time.Second},
		deviceID: deviceID,
	}
}

func (t *httpConnTester) Run(cfg DeviceConfig, report func(step string, ok bool, detail string)) error {
	base := strings.TrimRight(cfg.ServerURL, "/")

	if err := t.get(base+"/healthz", ""); err != nil {
		report("healthz", false, err.Error())
		return fmt.Errorf("healthz: %w", err)
	}
	report("healthz", true, "")

	// The device's own status endpoint, NOT /usage: a per-device token is
	// authorized for the device it was minted for and nothing else, while
	// /usage is operator-only (it aggregates every user's analysis spend). The
	// probe therefore proves exactly what onboarding needs — the token
	// authenticates and is bound to this device, which is registered — instead
	// of failing 403 on a token that is perfectly valid.
	if err := t.get(base+"/v1/devices/"+url.PathEscape(t.deviceID), cfg.Token); err != nil {
		report("auth", false, err.Error())
		return fmt.Errorf("auth: %w", err)
	}
	report("auth", true, "")
	return nil
}

func (t *httpConnTester) get(url, token string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return nil
}
