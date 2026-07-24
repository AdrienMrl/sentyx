package lte

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDongle mimics the ASR firmware's /api.cgi: a nonce from get_rand,
// login accepting md5(nonce+password), a CGISID cookie, and status calls that
// fail with system_err until authenticated.
type fakeDongle struct {
	password string
	rand     string
	session  string
	logins   int
}

func (f *fakeDongle) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "mobile.router" {
			http.Error(w, "vhost required", http.StatusMovedPermanently)
			return
		}
		path, method := r.URL.Query().Get("path"), r.URL.Query().Get("method")
		var body map[string]string
		if r.Body != nil {
			json.NewDecoder(r.Body).Decode(&body)
		}
		switch path + "." + method {
		case "account.get_rand":
			json.NewEncoder(w).Encode(map[string]any{"result": 0, "rand": f.rand})
		case "account.login":
			sum := md5.Sum([]byte(f.rand + f.password))
			if body["password"] != hex.EncodeToString(sum[:]) {
				json.NewEncoder(w).Encode(map[string]any{"result": 0})
				return
			}
			f.logins++
			f.session = "sess-" + f.rand
			http.SetCookie(w, &http.Cookie{Name: "CGISID", Value: f.session, Domain: "mobile.router"})
			json.NewEncoder(w).Encode(map[string]any{"result": 3})
		case "cm.get_link_context":
			c, err := r.Cookie("CGISID")
			if err != nil || c.Value != f.session {
				json.NewEncoder(w).Encode(map[string]string{"system_err": "session no exist"})
				return
			}
			w.Write([]byte(`{
				"celluar_basic_info": {"rssi": 42, "network_name": "Hologram", "roaming_network_name": "T-Mobile", "roaming": 1},
				"signal_info": {"rat": "4g", "level": 4},
				"contextlist": [{"connection_status": 1, "ipv4_ip": "10.239.229.252", "apn": "hologram"}]
			}`))
		default:
			t.Errorf("unexpected call %s.%s", path, method)
		}
	}
}

func newTestDongle(t *testing.T, f *fakeDongle) *Dongle {
	ts := httptest.NewServer(f.handler(t))
	t.Cleanup(ts.Close)
	d, err := NewDongle(DongleConfig{BaseURL: ts.URL, Password: "ADMIN", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDongleLinkContext(t *testing.T) {
	f := &fakeDongle{password: "admin", rand: "B4KcZGA3"} // client must lowercase the password
	d := newTestDongle(t, f)

	lc, err := d.LinkContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if lc.RSSI != 42 || lc.SignalLevel != 4 || lc.RAT != "4g" || !lc.Roaming || !lc.Connected {
		t.Errorf("unexpected link context: %+v", lc)
	}
	if lc.NetworkName != "Hologram" || lc.RoamingNetworkName != "T-Mobile" || lc.IPv4 != "10.239.229.252" {
		t.Errorf("unexpected identity fields: %+v", lc)
	}
	if f.logins != 1 {
		t.Errorf("logins = %d, want 1", f.logins)
	}
}

func TestDongleReloginOnExpiredSession(t *testing.T) {
	f := &fakeDongle{password: "admin", rand: "nonce111"}
	d := newTestDongle(t, f)
	if _, err := d.LinkContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.session = "expired-server-side"
	if _, err := d.LinkContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.logins != 2 {
		t.Errorf("logins = %d, want 2 (one initial, one after expiry)", f.logins)
	}
}

func TestDongleBadPassword(t *testing.T) {
	f := &fakeDongle{password: "other", rand: "nonce222"}
	d := newTestDongle(t, f)
	_, err := d.LinkContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Fatalf("err = %v, want login failure", err)
	}
}

func TestDongleConfigValidation(t *testing.T) {
	if _, err := NewDongle(DongleConfig{BaseURL: "http://x", Logf: t.Logf}); err == nil {
		t.Error("missing Password accepted")
	}
	if _, err := NewDongle(DongleConfig{Password: "p", Logf: t.Logf}); err == nil {
		t.Error("missing BaseURL accepted")
	}
}
