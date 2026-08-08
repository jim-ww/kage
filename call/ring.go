package call

import (
	"math"
	"time"
)

// RingTone plays a repeating sine-wave tone pattern (ringback for an
// outgoing call, ringtone for an incoming one) through its own dedicated
// Speaker, independent of any call's media pipeline - it only ever runs
// during the pre-connect ringing states, before startMedia opens the call's
// real mic/speaker, and is always stopped before that happens.
type RingTone struct {
	spk  *Speaker
	stop chan struct{}
	done chan struct{}
}

// NewRingTone opens a dedicated Speaker and starts generating a tone at
// freqHz, alternating onDur playing / offDur silent, until Stop is called.
func NewRingTone(freqHz float64, onDur, offDur time.Duration) (*RingTone, error) {
	spk, err := NewSpeaker()
	if err != nil {
		return nil, err
	}
	r := &RingTone{spk: spk, stop: make(chan struct{}), done: make(chan struct{})}
	go r.run(freqHz, onDur, offDur)
	return r, nil
}

func (r *RingTone) run(freqHz float64, onDur, offDur time.Duration) {
	defer close(r.done)

	const frameDuration = FrameMillis * time.Millisecond
	step := 2 * math.Pi * freqHz / SampleRate

	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	var phase float64
	var elapsed time.Duration
	on := true
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			frame := make([]int16, FrameSamples)
			if on {
				for i := range frame {
					frame[i] = int16(math.Sin(phase) * 8000)
					phase += step
				}
			}
			if err := r.spk.Write(frame); err != nil {
				return
			}

			elapsed += frameDuration
			if on && elapsed >= onDur {
				on, elapsed = false, 0
			} else if !on && elapsed >= offDur {
				on, elapsed = true, 0
			}
		}
	}
}

// Stop halts tone generation and releases the dedicated speaker.
func (r *RingTone) Stop() {
	close(r.stop)
	<-r.done
	r.spk.Close()
}
