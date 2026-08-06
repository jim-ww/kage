package xmpp

import (
	"io"
	"strings"
	"testing"
)

func TestProgressReaderReportsCumulativeBytes(t *testing.T) {
	data := strings.Repeat("x", 100)
	var calls [][2]int64
	r := &progressReader{
		Reader: strings.NewReader(data),
		total:  int64(len(data)),
		onProgress: func(sent, total int64) {
			calls = append(calls, [2]int64{sent, total})
		},
	}

	buf := make([]byte, 30)
	var gotTotal int
	for {
		n, err := r.Read(buf)
		gotTotal += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	if gotTotal != len(data) {
		t.Fatalf("read %d bytes, want %d", gotTotal, len(data))
	}
	if len(calls) == 0 {
		t.Fatal("onProgress was never called")
	}
	last := calls[len(calls)-1]
	if last[0] != int64(len(data)) || last[1] != int64(len(data)) {
		t.Fatalf("final progress call = %v, want sent=total=%d", last, len(data))
	}
	// sent must be non-decreasing across calls.
	for i := 1; i < len(calls); i++ {
		if calls[i][0] < calls[i-1][0] {
			t.Fatalf("sent went backwards: %v then %v", calls[i-1], calls[i])
		}
	}
}
