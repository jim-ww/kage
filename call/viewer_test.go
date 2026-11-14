package call

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestLatestPictureKeepsNewest checks a picture put before the last was
// taken replaces it, and its buffer is reused.
func TestLatestPictureKeepsNewest(t *testing.T) {
	l := newLatestPicture()
	for i := range 3 {
		pic := l.buffer(4)
		pic[0] = byte(i)
		l.Put(pic)
	}
	pic, ok := l.Take()
	if !ok || pic[0] != 2 || l.dropped != 2 {
		t.Fatalf("took picture %d (ok %v), %d dropped; want picture 2, 2 dropped", pic[0], ok, l.dropped)
	}
	l.Release(pic)
	if n := len(l.free); n != 2 {
		t.Errorf("%d buffers free, want both back: a dropped picture's is reused", n)
	}
	l.Close()
	if _, ok := l.Take(); ok {
		t.Error("Take after Close returned a picture")
	}
}

func TestY4MPictureSize(t *testing.T) {
	for _, tc := range []struct {
		header string
		size   int
		ok     bool
	}{
		{"YUV4MPEG2 W640 H360 F30:1 Ip A1:1 C420mpeg2 XYSCSS=420MPEG2\n", 640*360 + 2*320*180, true},
		{"YUV4MPEG2 W5 H3 F30:1 C420jpeg\n", 15 + 2*3*2, true},
		{"YUV4MPEG2 W640 H360 C444\n", 0, false},
		{"YUV4MPEG2 F30:1\n", 0, false},
		{"garbage\n", 0, false},
	} {
		size, err := y4mPictureSize(tc.header)
		if (err == nil) != tc.ok || size != tc.size {
			t.Errorf("y4mPictureSize(%q) = %d, %v; want %d, ok %v", tc.header, size, err, tc.size, tc.ok)
		}
	}
}

// h264Frames encodes n frames of a test pattern, one access unit each.
func h264Frames(t *testing.T, n int) [][]byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("no ffmpeg: %v", err)
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=30", "-frames:v", strconv.Itoa(n),
		"-pix_fmt", "yuv420p", "-c:v", "libx264", "-profile:v", "baseline", "-tune", "zerolatency", "-g", "30",
		"-threads", "1", // a slice per frame
		"-f", "h264", "-").Output()
	if err != nil {
		t.Fatalf("encoding test video: %v", err)
	}
	var frames [][]byte
	var frame []byte
	buf := append(out, 0, 0, 0, 1) // so the last NAL ends
	for {
		nal, rest, ok := splitNextNAL(buf)
		if !ok {
			break
		}
		buf = rest
		frame = append(frame, nal...)
		if isVCLNAL(nal) {
			frames = append(frames, frame)
			frame = nil
		}
	}
	if len(frames) != n {
		t.Fatalf("split %d frames, want %d", len(frames), n)
	}
	return frames
}

const testPictureSize = 320*180 + 2*160*90

// vp8Frames encodes n frames of a test pattern.
func vp8Frames(t *testing.T, n int) [][]byte {
	t.Helper()
	if err := exec.Command("ffmpeg", "-hide_banner", "-h", "encoder=libvpx").Run(); err != nil {
		t.Skipf("no ffmpeg with libvpx: %v", err)
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=30", "-frames:v", strconv.Itoa(n),
		"-c:v", "libvpx", "-deadline", "realtime", "-lag-in-frames", "0",
		"-f", "ivf", "-").Output()
	if err != nil {
		t.Fatalf("encoding test video: %v", err)
	}
	var frames [][]byte
	for p := 32; p+12 <= len(out); {
		size := int(binary.LittleEndian.Uint32(out[p:]))
		frames = append(frames, out[p+12:p+12+size])
		p += 12 + size
	}
	if len(frames) != n {
		t.Fatalf("split %d frames, want %d", len(frames), n)
	}
	return frames
}

// TestScreenViewerDecodes checks frames written come out the display end
// as whole pictures, each as soon as its frame is in: none is held back
// for the next.
func TestScreenViewerDecodes(t *testing.T) {
	t.Run("H.264", func(t *testing.T) { testScreenViewerDecodes(t, VideoCodecH264, h264Frames(t, 30)) })
	t.Run("VP8", func(t *testing.T) { testScreenViewerDecodes(t, VideoCodecVP8, vp8Frames(t, 30)) })
}

func testScreenViewerDecodes(t *testing.T, codec VideoCodec, frames [][]byte) {
	shown := filepath.Join(t.TempDir(), "shown.y4m")
	v, err := startScreenViewer(codec, videoDecoderCmd(codec), exec.Command("sh", "-c", "cat > "+shown), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if err := v.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
		time.Sleep(videoFrameGap)
	}
	// A picture the display was still busy with the one before for is
	// skipped, so it's shown and skipped ones that add up to every frame.
	shownAndSkipped := func() (got []byte, pictures, skipped int) {
		got, _ = os.ReadFile(shown)
		v.latest.mu.Lock()
		skipped = v.latest.dropped
		v.latest.mu.Unlock()
		return got, bytes.Count(got, []byte("FRAME\n")), skipped
	}
	deadline := time.Now().Add(5 * time.Second)
	got, pictures, skipped := shownAndSkipped()
	for pictures+skipped < len(frames) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		got, pictures, skipped = shownAndSkipped()
	}
	v.Close()
	if !bytes.HasPrefix(got, []byte("YUV4MPEG2 W320 H180 ")) {
		t.Fatalf("display got %q..., want a 320x180 YUV4MPEG stream", got[:min(len(got), 40)])
	}
	if pictures+skipped != len(frames) {
		t.Errorf("%d pictures shown and %d skipped before input ended, want all %d frames decoded", pictures, skipped, len(frames))
	}
	header := bytes.IndexByte(got, '\n') + 1
	if want := header + pictures*(len("FRAME\n")+testPictureSize); len(got) != want {
		t.Errorf("display got %d bytes in %d pictures, want %d: whole pictures", len(got), pictures, want)
	}
}

// TestScreenViewerSkipsPicturesForSlowDisplay checks a display slower
// than the video gets fewer pictures rather than falling behind, without
// any frame dropped before decoding.
func TestScreenViewerSkipsPicturesForSlowDisplay(t *testing.T) {
	frames := h264Frames(t, 60)
	// Takes a picture every 200ms, a sixth of the video's rate.
	slow := exec.Command("sh", "-c", "while dd bs="+strconv.Itoa(testPictureSize)+" count=1 iflag=fullblock of=/dev/null 2>/dev/null; do sleep 0.2; done")
	var asked atomic.Bool
	v, err := startScreenViewer(VideoCodecH264, videoDecoderCmd(VideoCodecH264), slow, func() { asked.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	start := time.Now()
	for _, f := range frames {
		if err := v.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
		time.Sleep(videoFrameGap)
	}
	if took, want := time.Since(start), time.Duration(len(frames))*videoFrameGap*3/2; took > want {
		t.Errorf("writing %d frames took %v, want under %v: never waiting on the display", len(frames), took, want)
	}
	time.Sleep(200 * time.Millisecond)
	v.latest.mu.Lock()
	dropped := v.latest.dropped
	v.latest.mu.Unlock()
	if dropped < len(frames)/2 {
		t.Errorf("%d pictures skipped, want most of %d", dropped, len(frames))
	}
	if asked.Load() {
		t.Error("frames were dropped before decoding, want only pictures skipped")
	}
}
