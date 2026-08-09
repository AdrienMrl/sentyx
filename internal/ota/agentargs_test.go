package ota_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The systemd unit and the args file are two halves of one contract: the unit
// reads agent.args as a NON-optional EnvironmentFile and word-splits
// $TESLCAM_AGENT_ARGS into ExecStart. If either half drifts, units fail to
// start after an update — the failure this whole design exists to prevent, and
// one that would only surface on real hardware.
const (
	unitPath = "../../scripts/teslcam-agent-pi4.service"
	argsPath = "../../scripts/agent.args"
)

func TestUnitReadsAgentArgsAsRequiredEnvironmentFile(t *testing.T) {
	unit := read(t, unitPath)
	// No leading "-": a missing args file must fail the unit loudly rather
	// than start an agent with no backing image, spool or gadget.
	if !strings.Contains(unit, "\nEnvironmentFile=/opt/teslcam/current/agent.args") {
		t.Fatal("unit does not read /opt/teslcam/current/agent.args as a required EnvironmentFile")
	}
	if strings.Contains(unit, "EnvironmentFile=-/opt/teslcam/current/agent.args") {
		t.Fatal("agent.args is marked optional; a missing file would start a misconfigured agent")
	}
	// Single $, unquoted: systemd only word-splits that form.
	if !strings.Contains(unit, "$TESLCAM_AGENT_ARGS") || strings.Contains(unit, "${TESLCAM_AGENT_ARGS}") {
		t.Fatal("ExecStart must reference $TESLCAM_AGENT_ARGS unbraced so systemd splits it into arguments")
	}
}

func TestAgentArgsIsASingleAssignmentSystemdCanParse(t *testing.T) {
	var assignments int
	for _, line := range strings.Split(read(t, argsPath), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "TESLCAM_AGENT_ARGS=") {
			t.Fatalf("unexpected line in agent.args (systemd has no line continuations): %q", line)
		}
		assignments++
	}
	if assignments != 1 {
		t.Fatalf("agent.args has %d assignments, want exactly 1", assignments)
	}
}

// Unit-specific values must stay in the unit: systemd does not expand ${VAR}
// nested inside an EnvironmentFile value, so a per-unit flag placed in
// agent.args would silently pass the literal string "${POST_TO}" to the agent.
func TestPerUnitIdentityStaysOutOfTheSharedArgsFile(t *testing.T) {
	// Only the assignment itself: the comments above it legitimately discuss
	// the flags that must NOT appear here.
	value := argsValue(t)
	for _, forbidden := range []string{"-post-to", "-device-id", "${"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("agent.args contains per-unit value %q; it belongs in the unit, expanded from /etc/teslcam/agent.env", forbidden)
		}
	}
	unit := read(t, unitPath)
	for _, required := range []string{"-post-to=${POST_TO}", "-device-id=${DEVICE_ID}"} {
		if !strings.Contains(unit, required) {
			t.Fatalf("unit no longer passes %s", required)
		}
	}
}

// Every flag the agent is given must still exist. A release whose args name a
// removed flag would fail at startup on the unit, after the symlink moved.
func TestAgentArgsOnlyNamesFlagsTheAgentDefines(t *testing.T) {
	value := argsValue(t)
	main := read(t, "../../cmd/teslcam-agent/main.go")
	defined := regexp.MustCompile(`flag\.\w+\("([\w-]+)"`)
	known := map[string]bool{}
	for _, m := range defined.FindAllStringSubmatch(main, -1) {
		known[m[1]] = true
	}
	if len(known) == 0 {
		t.Fatal("found no flag definitions in cmd/teslcam-agent/main.go")
	}
	for _, tok := range strings.Fields(value) {
		if !strings.HasPrefix(tok, "-") {
			continue // a value belonging to the previous flag
		}
		name := strings.TrimPrefix(strings.SplitN(tok, "=", 2)[0], "-")
		if !known[name] {
			t.Errorf("agent.args passes -%s, which teslcam-agent does not define", name)
		}
	}
}

// argsValue returns just the TESLCAM_AGENT_ARGS value, the part systemd
// actually passes to the agent.
func argsValue(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(read(t, argsPath), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "TESLCAM_AGENT_ARGS="); ok {
			return v
		}
	}
	t.Fatal("agent.args has no TESLCAM_AGENT_ARGS assignment")
	return ""
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
