package call

import "math"

const (
	// minPitch and maxPitch bound the pitch periods accelerate looks for:
	// 500Hz down to 100Hz, covering speaking voices.
	minPitch = SampleRate / 500
	maxPitch = SampleRate / 100

	// voicedCorrelation is how alike two consecutive pitch periods must be
	// for one to be cut out unnoticed.
	voicedCorrelation = 0.9
)

// accelerate shortens a frame so a backlog plays out faster without
// skipping any of it or changing its pitch (as WebRTC's NetEq does): in
// voiced audio, where the waveform repeats every pitch period, one period
// of at most maxCut samples is crossfaded into the next; a quiet frame is
// crossfaded down to half, whatever maxCut. Anything else is left as it is
// unless force says to cut it anyway, crossfaded over as much as maxCut
// allows. It returns how many samples it cut.
func accelerate(frame []int16, force bool, maxCut int) ([]int16, int) {
	var period int
	if quiet(frame) {
		period = len(frame) / 2
	} else {
		var corr float64
		period, corr = pitchPeriod(frame)
		switch {
		case corr >= voicedCorrelation && period <= maxCut:
		case force:
			period = min(maxCut, len(frame)/2)
		default:
			return frame, 0
		}
	}
	if period == 0 {
		return frame, 0
	}
	out := make([]int16, len(frame)-period)
	for i := range period {
		w := float64(i) / float64(period)
		out[i] = int16(float64(frame[i])*(1-w) + float64(frame[i+period])*w)
	}
	copy(out[period:], frame[2*period:])
	return out, period
}

// pitchPeriod finds the period at which frame's start repeats, and how
// well (normalized correlation, 1 = identical). Whole multiples of the
// period repeat about as well, so it's the shortest one that comes close
// to the best.
func pitchPeriod(frame []int16) (period int, corr float64) {
	maxT := min(maxPitch, len(frame)/2)
	if maxT < minPitch {
		return 0, 0
	}
	corrs := make([]float64, maxT+1)
	best := -1.0
	for t := minPitch; t <= maxT; t++ {
		corrs[t] = correlation(frame[:t], frame[t:2*t])
		best = max(best, corrs[t])
	}
	for t := minPitch; t <= maxT; t++ {
		peak := (t == maxT || corrs[t] >= corrs[t+1]) && (t == minPitch || corrs[t] >= corrs[t-1])
		if peak && corrs[t] >= best-0.03 {
			return t, corrs[t]
		}
	}
	return 0, 0
}

func correlation(a, b []int16) float64 {
	var ab, aa, bb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		ab += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return ab / math.Sqrt(aa*bb)
}
