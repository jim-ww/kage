package call

// AudioDevice is one capture or playback device, by the name NewMic/
// NewSpeaker accept.
type AudioDevice struct {
	Name    string
	Default bool // the system's current default device
}
