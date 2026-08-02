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

	// Where the time daemon records that it has confirmed the clock, how long
	// to wait for that before probing, and how often to look.
	clockMarker          string
	clockWait, clockPoll time.Duration
}

func newHTTPConnTester(deviceID string) *httpConnTester {
	return &httpConnTester{
		client:      &http.Client{Timeout: 15 * time.Second},
		deviceID:    deviceID,
		clockMarker: timesyncMarker,
		// Long enough to cover a slow first NTP sample (it usually lands within
		// a couple of seconds of joining Wi-Fi), short enough that this wait
		// plus both probe timeouts stays inside the central's budget for the
		// whole test.
		clockWait: 30 * time.Second,
		clockPoll: 500 * time.Millisecond,
	}
}

func (t *httpConnTester) Run(cfg DeviceConfig, report func(step string, ok bool, detail string)) error {
	base := strings.TrimRight(cfg.ServerURL, "/")

	// Before anything over TLS: a unit that has never been online believes it
	// is still the day its image was built, and every certificate then looks
	// "not yet valid". Waiting here turns a hard onboarding failure into a
	// couple of seconds on the progress screen. A clock that never syncs is
	// reported and not treated as fatal on its own — the probes below say
	// precisely what broke, and a plain-HTTP server is unaffected by it.
	if !awaitClock(t.clockMarker, t.clockWait, t.clockPoll) {
		report("clock", false, "the unit's clock is not synchronized yet; certificate checks may fail")
	} else {
		report("clock", true, "")
	}

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
