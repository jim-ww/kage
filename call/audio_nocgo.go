//go:build !cgo

// Stub audio devices for CGO_ENABLED=0 builds (goreleaser's cross-compiled
// Windows/macOS releases, which have no cgo cross toolchain wired up — see
// audio_cgo.go). Voice calls degrade gracefully: startMedia in
// callsession.go already treats a Mic error as "proceed without mic" and
// surfaces a Speaker error up as a call-setup failure.

package call

import "fmt"

// Mic is a stub on non-cgo builds; NewMic always errors.
type Mic struct{}

func NewMic() (*Mic, error) {
	return nil, fmt.Errorf("microphone capture not supported in this build")
}

func (m *Mic) Frames() <-chan []int16 { return nil }
func (m *Mic) Close() error           { return nil }

// Speaker is a stub on non-cgo builds; NewSpeaker always errors.
type Speaker struct{}

func NewSpeaker() (*Speaker, error) {
	return nil, fmt.Errorf("audio playback not supported in this build")
}

func (s *Speaker) Write(frame []int16) error { return fmt.Errorf("audio playback not supported in this build") }
func (s *Speaker) Close() error              { return nil }
