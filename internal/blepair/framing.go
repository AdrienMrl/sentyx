package blepair

import "fmt"

// Config writes are chunked because the payload (~250-400 B) exceeds the
// worst-case usable ATT payload (iOS commonly negotiates MTU 185). Each BLE
// write carries a 1-byte header followed by payload bytes. The same grammar
// is implemented in the app (SentyxGatt.kt); test vectors are shared.
const (
	frameCont   = 0x00 // continuation
	frameFirst  = 0x01 // first of several
	frameLast   = 0x02 // last of several
	frameSingle = 0x03 // first and last (single-frame message)
)

// reassembler accumulates Config frames into one payload. Not safe for
// concurrent use; the GATT layer serializes writes per characteristic.
type reassembler struct {
	buf    []byte
	active bool
}

// push consumes one frame. It returns the complete payload once the last
// frame arrives, nil while the message is still in flight.
func (r *reassembler) push(frame []byte) ([]byte, error) {
	if len(frame) < 1 {
		return nil, fmt.Errorf("framing: empty frame")
	}
	header, payload := frame[0], frame[1:]
	switch header {
	case frameSingle:
		r.reset()
		return cloneNonNil(payload), nil
	case frameFirst:
		r.buf = append(r.buf[:0], payload...)
		r.active = true
		return nil, nil
	case frameCont, frameLast:
		if !r.active {
			r.reset()
			return nil, fmt.Errorf("framing: continuation without a first frame")
		}
		r.buf = append(r.buf, payload...)
		if header == frameLast {
			out := cloneNonNil(r.buf)
			r.reset()
			return out, nil
		}
		return nil, nil
	default:
		r.reset()
		return nil, fmt.Errorf("framing: unknown frame header 0x%02x", header)
	}
}

func (r *reassembler) reset() {
	r.buf = r.buf[:0]
	r.active = false
}

// cloneNonNil copies b into a fresh slice that is non-nil even for empty
// input, so "message complete" is distinguishable from "mid-message".
func cloneNonNil(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// chunk splits a payload into frames of at most maxFrame bytes (header
// included). Exported logic mirror of the app-side chunker, used in tests.
func chunk(payload []byte, maxFrame int) ([][]byte, error) {
	if maxFrame < 2 {
		return nil, fmt.Errorf("framing: maxFrame %d too small", maxFrame)
	}
	per := maxFrame - 1
	if len(payload) <= per {
		return [][]byte{append([]byte{frameSingle}, payload...)}, nil
	}
	var frames [][]byte
	for off := 0; off < len(payload); off += per {
		end := min(off+per, len(payload))
		header := byte(frameCont)
		switch {
		case off == 0:
			header = frameFirst
		case end == len(payload):
			header = frameLast
		}
		frames = append(frames, append([]byte{header}, payload[off:end]...))
	}
	return frames, nil
}
