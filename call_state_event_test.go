package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
)

// TestCallStateEventVideoSource checks the outgoing-video fields the UI
// renders from: the source is reported from the moment it's requested
// (pendingShare), not only once capture is running.
func TestCallStateEventVideoSource(t *testing.T) {
	cases := []struct {
		name                     string
		sharing, pending, camera bool
		wantVideo                string
	}{
		{"idle", false, false, true, ""},
		{"pending camera", false, true, true, videoSourceCamera},
		{"sharing screen", true, false, false, videoSourceScreen},
		{"sharing camera", true, false, true, videoSourceCamera},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &callSession{sharing: tc.sharing, pendingShare: tc.pending, videoUseCamera: tc.camera}
			ev := c.stateEventLocked(callConnected, "")
			if ev.Video != tc.wantVideo || ev.VideoPending != tc.pending || ev.Sharing != tc.sharing {
				t.Fatalf("got Video=%q VideoPending=%v Sharing=%v", ev.Video, ev.VideoPending, ev.Sharing)
			}
		})
	}
}

// TestStartVideoShareWhilePendingSwitchesSource checks that asking for the
// other source before the peer accepted the first request just retargets it,
// rather than being dropped as "already sharing".
func TestStartVideoShareWhilePendingSwitchesSource(t *testing.T) {
	c := &callSession{state: callConnected, pendingShare: true, videoUseCamera: false}
	if err := c.startVideoShare(true); err != nil {
		t.Fatal(err)
	}
	if !c.videoUseCamera || !c.pendingShare {
		t.Fatalf("videoUseCamera=%v pendingShare=%v, want true/true", c.videoUseCamera, c.pendingShare)
	}
}

// TestGetCallStateResultWireShape checks the embedded callStateEvent's
// fields are promoted on the wire, where ipcClient.getCallState reads them
// back by name.
func TestGetCallStateResultWireShape(t *testing.T) {
	in := getCallStateResult{Active: true, callStateEvent: callStateEvent{Peer: "bob@localhost", Video: videoSourceScreen, RemoteVideo: true}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["Peer"] != "bob@localhost" || out["Video"] != videoSourceScreen || out["RemoteVideo"] != true || out["Active"] != true {
		t.Fatalf("unexpected wire shape: %s", b)
	}
}

// TestNextDesktopFrameSkipsBacklog checks desktop audio frames piling up
// past the backlog are skipped so playback stays near real time, and that
// an empty or closed source yields nothing without blocking.
func TestNextDesktopFrameSkipsBacklog(t *testing.T) {
	ch := make(chan []int16, 10)
	for i := range 6 {
		ch <- []int16{int16(i)}
	}
	if f, ended := nextDesktopFrame(ch); f[0] != 2 || len(ch) != 3 || ended {
		t.Fatalf("got frame %v with %d left (ended %v), want frame 2 with 3 left", f, len(ch), ended)
	}
	if f, _ := nextDesktopFrame(ch); f[0] != 3 {
		t.Fatalf("got %v, want the next frame in order", f)
	}
	for len(ch) > 0 {
		<-ch
	}
	if f, ended := nextDesktopFrame(ch); f != nil || ended {
		t.Fatalf("empty source gave %v, ended %v", f, ended)
	}
	close(ch)
	if f, ended := nextDesktopFrame(ch); f != nil || !ended {
		t.Fatalf("closed source gave %v, ended %v - want nothing, and ended", f, ended)
	}
}

// TestPeerWentOfflineEndsCall checks the peer device a call is with going
// unavailable ends the call, while another of the contact's devices going
// away doesn't.
func TestPeerWentOfflineEndsCall(t *testing.T) {
	_, db, err := storage.Open(filepath.Join(t.TempDir(), "calls.db"))
	if err != nil {
		t.Fatal(err)
	}
	sess := &accountSession{account: config.Account{JID: "alice@example.test"}, db: db}
	c := &callSession{sess: sess, peer: "bob@example.test", remoteJID: "bob@example.test/phone", state: callConnected, done: make(chan struct{})}
	sess.call = c

	sess.peerWentOffline(context.Background(), "bob@example.test/laptop")
	if sess.currentCall() == nil {
		t.Fatal("another of the peer's devices going offline ended the call")
	}
	sess.peerWentOffline(context.Background(), "bob@example.test/phone")
	if sess.currentCall() != nil || c.currentState() != callEnded {
		t.Fatal("the call's peer device going offline didn't end the call")
	}
}

func TestOmemoFingerprintGroups(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	want := "00010203 04050607 08090a0b 0c0d0e0f 10111213 14151617 18191a1b 1c1d1e1f"
	if got := omemoFingerprint(key); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClampDesktopAudioVolume(t *testing.T) {
	for in, want := range map[int]int{0: 100, 5: 10, 70: 70, 250: 200} {
		if got := clampDesktopAudioVolume(in); got != want {
			t.Errorf("clamp(%d) = %d, want %d", in, got, want)
		}
	}
}
