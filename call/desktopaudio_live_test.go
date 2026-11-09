//go:build integration

package call

import (
	"encoding/binary"
	"io"
	"math"
	"os/exec"
	"testing"
	"time"
)

// TestDesktopAudioCapturesOtherApps plays a tone from another process (as
// any application would) and checks DesktopAudio links it in and hands it
// over. Needs a running PipeWire - a session's own, or a bare `pipewire`
// started on a scratch XDG_RUNTIME_DIR (no session manager needed: the tone
// is marked always-process so the server's dummy driver schedules it).
func TestDesktopAudioCapturesOtherApps(t *testing.T) {
	if err := exec.Command("pw-cli", "info", "0").Run(); err != nil {
		t.Skipf("no PipeWire server reachable: %v", err)
	}
	d, err := NewDesktopAudio()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// --target 0: there may be no sink to play to (a bare test server),
	// and the session manager would fail the stream for lack of one.
	play := exec.Command("pw-play", "--raw", "--format", "s16", "--rate", "48000", "--channels", "1",
		"--target", "0", "-P", `{ "node.always-process": true }`, "-")
	stdin, err := play.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := play.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		play.Process.Kill()
		play.Wait()
	}()
	go func() {
		buf := make([]byte, FrameSamples*2)
		phase := 0.0
		for {
			for i := range FrameSamples {
				binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(math.Sin(phase)*10000)))
				phase += 2 * math.Pi * 440 / SampleRate
			}
			if _, err := stdin.Write(buf); err != nil {
				return
			}
		}
	}()
	defer stdin.(io.Closer).Close()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case frame, ok := <-d.Frames():
			if !ok {
				t.Fatal("capture stopped")
			}
			peak := 0
			for _, s := range frame {
				peak = max(peak, int(math.Abs(float64(s))))
			}
			if peak > 5000 {
				return
			}
		case <-deadline:
			t.Fatal("the other app's tone never reached the desktop audio capture")
		}
	}
}
