package call

import (
	"math"
	"math/rand/v2"
	"testing"
)

func tone(n int, hz, amplitude float64) []int16 {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(amplitude * math.Sin(2*math.Pi*hz*float64(i)/SampleRate))
	}
	return pcm
}

func noise(n int, amplitude float64, r *rand.Rand) []int16 {
	pcm := make([]int16, n)
	for i := range pcm {
		pcm[i] = int16(amplitude * (2*r.Float64() - 1))
	}
	return pcm
}

// maxStep is the biggest jump between neighbouring samples - a cut that
// isn't seamless shows up as one far bigger than the waveform's own.
func maxStep(pcm []int16) int {
	step := 0
	for i := 1; i < len(pcm); i++ {
		step = max(step, abs(int(pcm[i])-int(pcm[i-1])))
	}
	return step
}

func abs(x int) int { return max(x, -x) }

// TestAccelerateVoiced checks a voiced frame loses whole pitch periods
// seamlessly.
func TestAccelerateVoiced(t *testing.T) {
	in := tone(FrameSamples, 200, 8000) // a 240-sample period
	out, cut := accelerate(in, false, maxPitch)
	if cut != 240 || len(out) != len(in)-cut {
		t.Fatalf("cut %d samples to %d, want one 240-sample pitch period", cut, len(out))
	}
	if step, want := maxStep(out), maxStep(in)*11/10; step > want {
		t.Errorf("largest step %d after the cut, want at most %d (no click)", step, want)
	}
}

// TestAccelerateQuiet checks a quiet frame is halved.
func TestAccelerateQuiet(t *testing.T) {
	out, _ := accelerate(make([]int16, FrameSamples), false, 0)
	if len(out) != FrameSamples/2 {
		t.Fatalf("got %d samples, want %d", len(out), FrameSamples/2)
	}
}

// TestAccelerateWithinMaxCut checks a pitch period longer than maxCut
// isn't cut.
func TestAccelerateWithinMaxCut(t *testing.T) {
	in := tone(FrameSamples, 200, 8000)
	if _, cut := accelerate(in, false, 239); cut != 0 {
		t.Fatalf("cut %d samples, want none past maxCut", cut)
	}
}

// TestAccelerateLeavesNoiseUnlessForced checks a frame with no pitch to
// cut is only shortened when forced.
func TestAccelerateLeavesNoiseUnlessForced(t *testing.T) {
	in := noise(FrameSamples, 8000, rand.New(rand.NewPCG(1, 2)))
	if _, cut := accelerate(in, false, maxPitch); cut != 0 {
		t.Fatalf("noise cut by %d samples unforced", cut)
	}
	if _, cut := accelerate(in, true, maxPitch); cut == 0 {
		t.Fatal("noise not shortened when forced")
	}
}
