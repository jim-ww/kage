//go:build cgo && linux

package call

// miniaudio (malgo) dlopens its audio backends at runtime rather than
// linking them, and on NixOS dlopen only finds a library the binary's own
// RUNPATH (or LD_LIBRARY_PATH) points at - there's no /usr/lib. Linking
// libasound here keeps the ALSA backend reachable everywhere, PipeWire/
// PulseAudio included through their ALSA plugins; libpulse, which gives
// proper device names, is put on the library path by the nix wrapper/dev
// shell instead (see flake.nix), since linking it would make it a hard
// dependency.

// #cgo pkg-config: alsa
// #include <alsa/asoundlib.h>
import "C"

func init() {
	// Referenced so the linker can't drop libasound as unused.
	_ = C.snd_asoundlib_version()
}
