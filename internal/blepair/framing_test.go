package blepair

import (
	"bytes"
	"testing"
)

// Shared test vectors: the Kotlin chunker in SentyxGatt.kt must produce these
// exact frames for the same inputs.
func TestFramingVectors(t *testing.T) {
	cases := []struct {
		name     string
		payload  []byte
		maxFrame int
		want     [][]byte
	}{
		{"single", []byte("abc"), 5, [][]byte{{0x03, 'a', 'b', 'c'}}},
		{"exact fit", []byte("abcd"), 5, [][]byte{{0x03, 'a', 'b', 'c', 'd'}}},
		{"two frames", []byte("abcde"), 5, [][]byte{
			{0x01, 'a', 'b', 'c', 'd'}, {0x02, 'e'},
		}},
		{"three frames", []byte("abcdefghij"), 5, [][]byte{
			{0x01, 'a', 'b', 'c', 'd'}, {0x00, 'e', 'f', 'g', 'h'}, {0x02, 'i', 'j'},
		}},
		{"empty", nil, 5, [][]byte{{0x03}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames, err := chunk(tc.payload, tc.maxFrame)
			if err != nil {
				t.Fatal(err)
			}
			if len(frames) != len(tc.want) {
				t.Fatalf("frames = %v, want %v", frames, tc.want)
			}
			for i := range frames {
				if !bytes.Equal(frames[i], tc.want[i]) {
					t.Fatalf("frame %d = %v, want %v", i, frames[i], tc.want[i])
				}
			}
			// Round-trip through the reassembler.
			var r reassembler
			var got []byte
			for i, f := range frames {
				out, err := r.push(f)
				if err != nil {
					t.Fatal(err)
				}
				if i < len(frames)-1 && out != nil {
					t.Fatal("payload completed early")
				}
				if i == len(frames)-1 {
					got = out
				}
			}
			if got == nil || !bytes.Equal(got, tc.payload) {
				t.Fatalf("round-trip = %q, want %q", got, tc.payload)
			}
		})
	}
}

func TestFramingErrors(t *testing.T) {
	var r reassembler
	if _, err := r.push(nil); err == nil {
		t.Fatal("empty frame should error")
	}
	if _, err := r.push([]byte{0x00, 'x'}); err == nil {
		t.Fatal("continuation without first should error")
	}
	if _, err := r.push([]byte{0x02, 'x'}); err == nil {
		t.Fatal("last without first should error")
	}
	if _, err := r.push([]byte{0x7f, 'x'}); err == nil {
		t.Fatal("unknown header should error")
	}
	// A first frame abandoned mid-message is discarded by a later single.
	if _, err := r.push([]byte{0x01, 'a'}); err != nil {
		t.Fatal(err)
	}
	out, err := r.push([]byte{0x03, 'b'})
	if err != nil || !bytes.Equal(out, []byte{'b'}) {
		t.Fatalf("single after abandoned first = %q, %v", out, err)
	}
}
