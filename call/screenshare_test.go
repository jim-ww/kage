package call

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// TestScreenShareRunGroupsNALsIntoFrames verifies that Run buffers SPS/PPS/
// slice NALs belonging to the same picture into a single write() call,
// rather than invoking write once per NAL - see the comment on Run for why
// that distinction matters for how pion packetizes and timestamps the RTP
// stream.
func TestScreenShareRunGroupsNALsIntoFrames(t *testing.T) {
	// splitNextNAL's search for the *next* start code begins 3 bytes into
	// the current NAL's payload (to avoid matching the current NAL's own
	// header as a false start code), so payloads here need to be longer
	// than that to be found correctly.
	sps := []byte{0, 0, 0, 1, 0x67, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa}
	pps := []byte{0, 0, 0, 1, 0x68, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb}
	slice1 := []byte{0, 0, 0, 1, 0x65, 0xcc, 0xcc, 0xcc, 0xcc, 0xcc} // IDR slice, type 5
	slice2 := []byte{0, 0, 0, 1, 0x41, 0xdd, 0xdd, 0xdd, 0xdd, 0xdd} // non-IDR slice, type 1
	trailing := []byte{0, 0, 0, 1, 0x41, 0xee, 0xee, 0xee, 0xee, 0xee}

	var stream []byte
	stream = append(stream, sps...)
	stream = append(stream, pps...)
	stream = append(stream, slice1...)
	stream = append(stream, slice2...)
	// splitNextNAL only emits a NAL once the *next* one's start code has
	// arrived, so a trailing NAL is needed to flush slice2 out of buf.
	stream = append(stream, trailing...)

	pr, pw := io.Pipe()
	go func() {
		pw.Write(stream)
		pw.Close()
	}()

	s := &ScreenShare{&annexBSource{label: "screen share", stdout: pr, stderr: &syncBuffer{}, done: make(chan struct{})}}

	var frames [][]byte
	err := s.Run(func(frame []byte, _ time.Duration) error {
		cp := make([]byte, len(frame))
		copy(cp, frame)
		frames = append(frames, cp)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2: %v", len(frames), frames)
	}

	wantFrame1 := append(append([]byte{}, sps...), append(pps, slice1...)...)
	if !bytes.Equal(frames[0], wantFrame1) {
		t.Errorf("frame 0 = %x, want %x (SPS+PPS+slice grouped together)", frames[0], wantFrame1)
	}
	if !bytes.Equal(frames[1], slice2) {
		t.Errorf("frame 1 = %x, want %x", frames[1], slice2)
	}
}

// TestSplitNextNALDoesNotDropMiddleNALs guards against a regression where a
// NAL sandwiched between two others (e.g. PPS between SPS and the first
// slice) was silently discarded: splitNextNAL returned each NAL's own
// leading start code stripped but the *following* NAL's start code
// appended, so the next call's search started past that following NAL's
// header and skipped straight to the one after it.
func TestSplitNextNALDoesNotDropMiddleNALs(t *testing.T) {
	sps := []byte{0, 0, 0, 1, 0x67, 0xaa, 0xaa, 0xaa, 0xaa, 0xaa}
	pps := []byte{0, 0, 0, 1, 0x68, 0xbb, 0xbb, 0xbb, 0xbb, 0xbb}
	slice1 := []byte{0, 0, 0, 1, 0x65, 0xcc, 0xcc, 0xcc, 0xcc, 0xcc}
	trailing := []byte{0, 0, 0, 1, 0x41, 0xdd, 0xdd, 0xdd, 0xdd, 0xdd}

	var buf []byte
	buf = append(buf, sps...)
	buf = append(buf, pps...)
	buf = append(buf, slice1...)
	buf = append(buf, trailing...)

	var got [][]byte
	for {
		nal, rest, ok := splitNextNAL(buf)
		if !ok {
			break
		}
		cp := make([]byte, len(nal))
		copy(cp, nal)
		got = append(got, cp)
		buf = rest
	}

	want := [][]byte{sps, pps, slice1}
	if len(got) != len(want) {
		t.Fatalf("got %d NALs, want %d: %x", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("NAL %d = %x, want %x", i, got[i], want[i])
		}
	}
}

func TestIsVCLNAL(t *testing.T) {
	cases := []struct {
		name string
		nal  []byte
		want bool
	}{
		{"sps 4-byte start code", []byte{0, 0, 0, 1, 0x67, 0xaa}, false},
		{"pps 3-byte start code", []byte{0, 0, 1, 0x68, 0xbb}, false},
		{"idr slice", []byte{0, 0, 0, 1, 0x65, 0xcc}, true},
		{"non-idr slice", []byte{0, 0, 1, 0x41, 0xdd}, true},
		{"sei", []byte{0, 0, 0, 1, 0x06, 0xee}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isVCLNAL(tc.nal); got != tc.want {
				t.Errorf("isVCLNAL(%x) = %v, want %v", tc.nal, got, tc.want)
			}
		})
	}
}
