// Package lte provides the metered-LTE fallback path. The Pi's LTE dongle
// deliberately has no default route (see hardware/lte-dongle.md): the only way
// a packet leaves over LTE is a socket explicitly bound to the dongle
// interface's address, which makes "only sentry uploads use LTE" structural
// rather than a firewall filter. NewTransport is that binding: it dials over
// the default route (Wi-Fi) first and retries bound to the LTE interface only
// when the default route fails.
package lte

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FallbackConfig configures the fallback transport. All fields are required.
type FallbackConfig struct {
	// Interface is the LTE network interface, e.g. eth1. Its IPv4 address is
	// looked up per dial, so DHCP renewals and dongle resets need no restart.
	Interface string
	// DNS is the resolver dialed over LTE, e.g. 8.8.8.8:53 (the dongle's
	// DHCP-provided resolver). The system resolver rides the default route and
	// is unreachable exactly when the fallback is needed.
	DNS string
	// DialTimeout bounds each attempt separately; a dial that falls back can
	// take up to twice this before failing.
	DialTimeout time.Duration
	Logf        func(format string, v ...any)
}

// NewTransport returns an http.Transport whose dials fall back to the LTE
// interface. Route transitions (Wi-Fi -> LTE and back) are logged once each.
func NewTransport(cfg FallbackConfig) (*http.Transport, error) {
	if cfg.Interface == "" || cfg.DNS == "" || cfg.DialTimeout <= 0 || cfg.Logf == nil {
		return nil, fmt.Errorf("lte: Interface, DNS, DialTimeout and Logf are all required")
	}
	if _, _, err := net.SplitHostPort(cfg.DNS); err != nil {
		return nil, fmt.Errorf("lte: DNS must be host:port: %w", err)
	}
	d := &fallbackDialer{cfg: cfg}
	return &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, nil
}

type fallbackDialer struct {
	cfg FallbackConfig

	mu    sync.Mutex
	onLTE bool
}

func (d *fallbackDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	primary := &net.Dialer{Timeout: d.cfg.DialTimeout}
	conn, primaryErr := primary.DialContext(ctx, network, addr)
	if primaryErr == nil {
		d.noteRoute(false, addr)
		return conn, nil
	}
	local, err := interfaceIPv4(d.cfg.Interface)
	if err != nil {
		return nil, fmt.Errorf("%w (LTE fallback unavailable: %v)", primaryErr, err)
	}
	conn, lteErr := d.dialLTE(ctx, network, addr, local)
	if lteErr != nil {
		return nil, fmt.Errorf("default route: %v; LTE via %s: %w", primaryErr, d.cfg.Interface, lteErr)
	}
	d.noteRoute(true, addr)
	return conn, nil
}

// dialLTE dials addr with the socket bound to the LTE interface's address,
// which is what routes it through the LTE-only policy table. Hostnames are
// resolved through the configured LTE resolver for the same reason.
func (d *fallbackDialer) dialLTE(ctx context.Context, network, addr string, local net.IP) (net.Conn, error) {
	if !strings.HasPrefix(network, "tcp") {
		return nil, fmt.Errorf("network %q not supported over the LTE fallback", network)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: d.cfg.DialTimeout, LocalAddr: &net.TCPAddr{IP: local}}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return nil, fmt.Errorf("IPv6 %s not supported over the LTE fallback", ip)
		}
		return dialer.DialContext(ctx, "tcp4", addr)
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			rd := net.Dialer{Timeout: d.cfg.DialTimeout, LocalAddr: &net.UDPAddr{IP: local}}
			return rd.DialContext(ctx, "udp4", d.cfg.DNS)
		},
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s via %s: %w", host, d.cfg.DNS, err)
	}
	for _, a := range addrs {
		if ip4 := a.IP.To4(); ip4 != nil {
			return dialer.DialContext(ctx, "tcp4", net.JoinHostPort(ip4.String(), port))
		}
	}
	return nil, fmt.Errorf("no IPv4 address for %s via %s", host, d.cfg.DNS)
}

func (d *fallbackDialer) noteRoute(onLTE bool, addr string) {
	d.mu.Lock()
	changed := d.onLTE != onLTE
	d.onLTE = onLTE
	d.mu.Unlock()
	if !changed {
		return
	}
	if onLTE {
		d.cfg.Logf("lte: default route failed, %s now reached over %s (metered)", addr, d.cfg.Interface)
	} else {
		d.cfg.Logf("lte: default route restored, %s reached without %s", addr, d.cfg.Interface)
	}
}

// interfaceIPv4 returns the interface's global IPv4 address, or an error when
// the dongle is absent, link-down, or has no lease yet.
func interfaceIPv4(name string) (net.IP, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() && !ip4.IsLoopback() {
			return ip4, nil
		}
	}
	return nil, fmt.Errorf("interface %s has no IPv4 address", name)
}
