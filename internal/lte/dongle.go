package lte

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Dongle is a client for the ASR Microelectronics LTE dongle's local status
// API (lighttpd + a ubus-over-HTTP bridge at /api.cgi; protocol reverse-noted
// in hardware/lte-dongle.md). It is used to read signal quality and connection
// state for heartbeats and field debugging — never for data-path traffic.
type Dongle struct {
	cfg DongleConfig

	mu      sync.Mutex
	session string // CGISID cookie value; empty until Login
}

// DongleConfig configures the dongle client. BaseURL, Password and Logf are
// required; HTTPClient is optional (a bounded client is used when nil).
type DongleConfig struct {
	BaseURL    string // e.g. http://192.168.8.1
	Password   string // web UI admin password
	HTTPClient *http.Client
	Logf       func(format string, v ...any)
}

// LinkContext is the subset of cm.get_link_context the agent cares about.
type LinkContext struct {
	RSSI               int    `json:"rssi"`
	SignalLevel        int    `json:"signalLevel"` // bars, 0-5
	RAT                string `json:"rat"`         // e.g. "4g"
	NetworkName        string `json:"networkName"`
	RoamingNetworkName string `json:"roamingNetworkName,omitempty"`
	Roaming            bool   `json:"roaming"`
	Connected          bool   `json:"connected"`
	IPv4               string `json:"ipv4,omitempty"`
	APN                string `json:"apn,omitempty"`
}

func NewDongle(cfg DongleConfig) (*Dongle, error) {
	if cfg.BaseURL == "" || cfg.Password == "" || cfg.Logf == nil {
		return nil, fmt.Errorf("lte: BaseURL, Password and Logf are required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	return &Dongle{cfg: cfg}, nil
}

// LinkContext fetches signal and connection state, transparently
// (re-)authenticating when the session is missing or expired.
func (d *Dongle) LinkContext(ctx context.Context) (*LinkContext, error) {
	raw, err := d.callAuthed(ctx, "cm", "get_link_context", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Basic struct {
			RSSI               int    `json:"rssi"`
			NetworkName        string `json:"network_name"`
			RoamingNetworkName string `json:"roaming_network_name"`
			Roaming            int    `json:"roaming"`
		} `json:"celluar_basic_info"` // "celluar" typo is the firmware's
		Signal struct {
			RAT   string `json:"rat"`
			Level int    `json:"level"`
		} `json:"signal_info"`
		Contexts []struct {
			ConnectionStatus int    `json:"connection_status"`
			IPv4             string `json:"ipv4_ip"`
			APN              string `json:"apn"`
		} `json:"contextlist"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("lte: parsing get_link_context: %w", err)
	}
	lc := &LinkContext{
		RSSI:               resp.Basic.RSSI,
		SignalLevel:        resp.Signal.Level,
		RAT:                resp.Signal.RAT,
		NetworkName:        resp.Basic.NetworkName,
		RoamingNetworkName: resp.Basic.RoamingNetworkName,
		Roaming:            resp.Basic.Roaming != 0,
	}
	for _, c := range resp.Contexts {
		if c.ConnectionStatus == 1 {
			lc.Connected = true
			lc.IPv4 = c.IPv4
			lc.APN = c.APN
			break
		}
	}
	return lc, nil
}

// callAuthed performs one ubus call, logging in first when there is no
// session and retrying once when the dongle reports the session expired.
func (d *Dongle) callAuthed(ctx context.Context, path, method string, body any) (json.RawMessage, error) {
	d.mu.Lock()
	haveSession := d.session != ""
	d.mu.Unlock()
	if !haveSession {
		if err := d.login(ctx); err != nil {
			return nil, err
		}
	}
	raw, err := d.call(ctx, path, method, body)
	if err == nil && sessionExpired(raw) {
		if err := d.login(ctx); err != nil {
			return nil, err
		}
		raw, err = d.call(ctx, path, method, body)
	}
	if err != nil {
		return nil, err
	}
	if sessionExpired(raw) {
		return nil, fmt.Errorf("lte: dongle session rejected after re-login")
	}
	return raw, nil
}

func sessionExpired(raw json.RawMessage) bool {
	var e struct {
		SystemErr string `json:"system_err"`
	}
	return json.Unmarshal(raw, &e) == nil && e.SystemErr != ""
}

// login runs the dongle's challenge-response: account.get_rand issues a nonce,
// account.login expects md5(nonce + lowercase(password)) and result 3 (OK).
func (d *Dongle) login(ctx context.Context) error {
	uidBytes := make([]byte, 4)
	if _, err := rand.Read(uidBytes); err != nil {
		return err
	}
	uid := hex.EncodeToString(uidBytes)

	raw, err := d.call(ctx, "account", "get_rand", map[string]string{"type": "admin", "user_id": uid})
	if err != nil {
		return err
	}
	var randResp struct {
		Result int    `json:"result"`
		Rand   string `json:"rand"`
	}
	if err := json.Unmarshal(raw, &randResp); err != nil || randResp.Rand == "" {
		return fmt.Errorf("lte: get_rand returned %s", raw)
	}

	sum := md5.Sum([]byte(randResp.Rand + strings.ToLower(d.cfg.Password)))
	raw, err = d.call(ctx, "account", "login", map[string]string{
		"type": "admin", "username": "admin",
		"password": hex.EncodeToString(sum[:]), "user_id": uid,
	})
	if err != nil {
		return err
	}
	var loginResp struct {
		Result int `json:"result"`
	}
	if err := json.Unmarshal(raw, &loginResp); err != nil {
		return fmt.Errorf("lte: login returned %s", raw)
	}
	if loginResp.Result != 3 { // LOGIN_RESULT.OK
		return fmt.Errorf("lte: dongle login failed (result %d; 6 = locked out after too many tries)", loginResp.Result)
	}
	return nil
}

// call performs one raw /api.cgi ubus call. The CGISID session cookie is
// managed by hand: the dongle scopes it to its vanity hostname mobile.router,
// which a standard cookie jar keyed on the IP URL would refuse to store.
func (d *Dongle) call(ctx context.Context, path, method string, body any) (json.RawMessage, error) {
	url := fmt.Sprintf("%s/api.cgi?path=%s&method=%s&timeout=10", d.cfg.BaseURL, path, method)
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, methodFor(body), url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Host = "mobile.router" // lighttpd vhost; direct-IP requests 301 without it
	req.Header.Set("Content-Type", "application/json")
	d.mu.Lock()
	if d.session != "" {
		req.Header.Set("Cookie", "CGISID="+d.session)
	}
	d.mu.Unlock()

	resp, err := d.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "CGISID" && c.Value != "" {
			d.mu.Lock()
			d.session = c.Value
			d.mu.Unlock()
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lte: dongle %s.%s: HTTP %d: %.200s", path, method, resp.StatusCode, raw)
	}
	return raw, nil
}

func methodFor(body any) string {
	if body == nil {
		return http.MethodGet
	}
	return http.MethodPost
}
