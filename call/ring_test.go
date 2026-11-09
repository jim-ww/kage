package call

import (
	"errors"
	"testing"
	"time"
)

// stalledSink is an output device nothing drains: Write blocks until Close.
type stalledSink struct{ closed chan struct{} }

func (s *stalledSink) Write([]int16) error {
	<-s.closed
	return errors.New("closed")
}

func (s *stalledSink) Close() error {
	close(s.closed)
	return nil
}

// TestRingToneStopWithStalledOutput guards against Stop hanging forever when
// the output device has stopped consuming audio - Stop runs on the path
// that answers a call, so a hang there leaves the call unanswerable.
func TestRingToneStopWithStalledOutput(t *testing.T) {
	r := startRingTone(&stalledSink{closed: make(chan struct{})}, []float64{440}, []time.Duration{time.Second, time.Second})
	time.Sleep(3 * FrameMillis * time.Millisecond) // let run block in Write

	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung on a stalled output device")
	}
}
