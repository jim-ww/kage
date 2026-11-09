package call

import (
	"math"
	"time"
)

// ringFadeMillis is how long each "on" segment fades in/out, instead of
// switching amplitude instantly - a hard on/off edge on a sine wave is an
// audible click/pop, and is most of what made the original single-tone
// version sound harsh.
const ringFadeMillis = 15 * time.Millisecond

// RingTone plays a repeating chord (ringback for an outgoing call, ringtone
// for an incoming one) through its own dedicated Speaker, independent of any
// call's media pipeline - it only ever runs during the pre-connect ringing
// states, before startMedia opens the call's real mic/speaker, and is
// always stopped before that happens.
type RingTone struct {
	spk  pcmSink
	stop chan struct{}
	done chan struct{}
}

// pcmSink is the part of Speaker RingTone uses.
type pcmSink interface {
	Write(frame []int16) error
	Close() error
}

// NewRingTone opens a dedicated Speaker on device ("" for the system
// default) and starts looping the given
// pattern: segments alternate on/off starting with "on", each duration a
// multiple of a frame (FrameMillis). During an "on" segment, all of freqsHz
// play together as a soft chord (quieter and fading at each edge, rather
// than a single flat-amplitude tone) - two or three close, consonant
// frequencies read as a more pleasant "modern" tone than one raw sine wave.
func NewRingTone(device string, freqsHz []float64, pattern []time.Duration) (*RingTone, error) {
	spk, err := NewSpeaker(device)
	if err != nil {
		return nil, err
	}
	return startRingTone(spk, freqsHz, pattern), nil
}

func startRingTone(spk pcmSink, freqsHz []float64, pattern []time.Duration) *RingTone {
	r := &RingTone{spk: spk, stop: make(chan struct{}), done: make(chan struct{})}
	go r.run(freqsHz, pattern)
	return r
}

func (r *RingTone) run(freqsHz []float64, pattern []time.Duration) {
	defer close(r.done)

	const frameDuration = FrameMillis * time.Millisecond
	steps := make([]float64, len(freqsHz))
	phases := make([]float64, len(freqsHz))
	for i, f := range freqsHz {
		steps[i] = 2 * math.Pi * f / SampleRate
	}
	// Per-voice amplitude, quieter than the old single-tone version and
	// split across voices so a multi-frequency chord doesn't clip.
	const amplitude = 5000

	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	segIdx := 0
	var elapsed time.Duration
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			segDur := pattern[segIdx]
			on := segIdx%2 == 0

			frame := make([]int16, FrameSamples)
			if on {
				// Fade in/out at the segment's edges so consecutive on/off
				// transitions don't click.
				for i := range frame {
					var sample float64
					for v := range steps {
						sample += math.Sin(phases[v]) * amplitude
						phases[v] += steps[v]
					}

					sampleElapsed := elapsed + time.Duration(i)*time.Second/SampleRate
					remaining := segDur - sampleElapsed
					gain := 1.0
					if sampleElapsed < ringFadeMillis {
						gain = float64(sampleElapsed) / float64(ringFadeMillis)
					} else if remaining < ringFadeMillis {
						gain = float64(remaining) / float64(ringFadeMillis)
					}
					gain = max(0, min(1, gain))
					frame[i] = int16(sample * gain)
				}
			}
			if err := r.spk.Write(frame); err != nil {
				return
			}

			elapsed += frameDuration
			if elapsed >= segDur {
				elapsed = 0
				segIdx = (segIdx + 1) % len(pattern)
			}
		}
	}
}

// Stop halts tone generation and releases the dedicated speaker. The
// speaker is closed before waiting for the generator to exit: a stalled
// output device (nothing draining playback) leaves run blocked inside
// Write, which only a closed speaker unblocks.
func (r *RingTone) Stop() {
	close(r.stop)
	r.spk.Close()
	<-r.done
}
