package blepair

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpConnTester verifies the received config against the server: first the
// open /healthz, then an authenticated endpoint with the new device token.
// Only a passing auth step lets the config persist.
type httpConnTester struct {
	client *http.Client
}

func newHTTPConnTester() *httpConnTester {
	return &httpConnTester{client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *httpConnTester) Run(cfg DeviceConfig, report func(step string, ok bool, detail string)) error {
	base := strings.TrimRight(cfg.ServerURL, "/")

	if err := t.get(base+"/healthz", ""); err != nil {
		report("healthz", false, err.Error())
		return fmt.Errorf("healthz: %w", err)
	}
	report("healthz", true, "")

	if err := t.get(base+"/usage", cfg.Token); err != nil {
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
