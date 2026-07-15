// Package health collects best-effort device metrics on the in-car Pi and
// reports them to the server as periodic heartbeats (the agent side of the M6
// device-status contract). Every collector is best-effort: a metric that
// cannot be read is omitted from the heartbeat rather than sent as a fabricated
// default, so the server/app can distinguish "unknown" from a real value.
//
// The sysfs/syscall/exec collectors are Linux-only (build-tagged); the
// heartbeat JSON assembly and the small numeric helpers are pure and tested
// cross-platform.
package health

import (
	"strconv"
	"strings"
)

// Heartbeat is the agent -> server heartbeat body (protocol v1). Optional
// metrics are pointers with omitempty so an unreadable metric is omitted
// entirely; UploadBacklog, RecordingNow and SentryActive come from the
// pipeline and are always present.
type Heartbeat struct {
	V            int    `json:"v"`
	AgentVersion string `json:"agentVersion"`

	UptimeSec         *int64   `json:"uptimeSec,omitempty"`
	StorageFreeBytes  *int64   `json:"storageFreeBytes,omitempty"`
	StorageTotalBytes *int64   `json:"storageTotalBytes,omitempty"`
	CPUTempC          *float64 `json:"cpuTempC,omitempty"`

	ThrottledHex     *string `json:"throttledHex,omitempty"`
	UnderVoltageNow  *bool   `json:"underVoltageNow,omitempty"`
	UnderVoltageEver *bool   `json:"underVoltageEver,omitempty"`

	WifiSsid      *string `json:"wifiSsid,omitempty"`
	WifiRssiDbm   *int    `json:"wifiRssiDbm,omitempty"`
	WifiSignalPct *int    `json:"wifiSignalPct,omitempty"`

	UploadBacklog int  `json:"uploadBacklog"`
	RecordingNow  bool `json:"recordingNow"`
	SentryActive  bool `json:"sentryActive"`
}

// metrics holds the raw best-effort readings from the platform collectors. A
// nil field means the metric was not readable and is omitted from the
// heartbeat.
type metrics struct {
	uptimeSec         *int64
	storageFreeBytes  *int64
	storageTotalBytes *int64
	cpuTempC          *float64
	throttledHex      *string
	underVoltageNow   *bool
	underVoltageEver  *bool
	wifiSsid          *string
	wifiRssiDbm       *int
	wifiSignalPct     *int
}

// buildHeartbeat assembles the wire heartbeat from collected metrics plus the
// pipeline-supplied backlog/recording state. It is pure so it can be tested on
// any OS. sentryActive is a best-effort proxy: there is no cheap Tesla-side
// "sentry armed" signal available to the agent, so we report it equal to
// recordingNow (the car is actively writing sentry clips, which only happens
// while Sentry is engaged). This is documented as an approximation.
func buildHeartbeat(agentVersion string, m metrics, backlog int, recording bool) Heartbeat {
	return Heartbeat{
		V:                 1,
		AgentVersion:      agentVersion,
		UptimeSec:         m.uptimeSec,
		StorageFreeBytes:  m.storageFreeBytes,
		StorageTotalBytes: m.storageTotalBytes,
		CPUTempC:          m.cpuTempC,
		ThrottledHex:      m.throttledHex,
		UnderVoltageNow:   m.underVoltageNow,
		UnderVoltageEver:  m.underVoltageEver,
		WifiSsid:          m.wifiSsid,
		WifiRssiDbm:       m.wifiRssiDbm,
		WifiSignalPct:     m.wifiSignalPct,
		UploadBacklog:     backlog,
		RecordingNow:      recording,
		SentryActive:      recording,
	}
}

// rssiToPct maps a Wi-Fi RSSI in dBm to a rough 0-100 signal percentage.
// -100 dBm and below is 0%, -50 dBm and above is 100%, linear in between
// (pct = clamp(2*(rssi+100), 0, 100)).
func rssiToPct(rssiDbm int) int {
	pct := 2 * (rssiDbm + 100)
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// parseThrottled parses the output of `vcgencmd get_throttled`, which looks
// like "throttled=0x50005" (or just the "0x50005" value). It returns the raw
// bitmask value and ok=false if the line cannot be parsed. Bit 0 = under-voltage
// currently, bit 16 = under-voltage has occurred since boot.
func parseThrottled(out string) (uint64, bool) {
	s := strings.TrimSpace(out)
	if i := strings.IndexByte(s, '='); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return 0, false
	}
	val, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, false
	}
	return val, true
}

const (
	throttleUnderVoltageNowBit  = 0
	throttleUnderVoltageEverBit = 16
)
