package main

import (
	"encoding/json"
	"testing"
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
