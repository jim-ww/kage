package main

import (
	"testing"

	"github.com/jim-ww/kage/call"
	"github.com/jim-ww/kage/xmpp"
	"github.com/pion/webrtc/v4"
)

// TestSettleRejectedContent covers a video content-add the peer rejects:
// our pending offer gets settled, so sharing can be offered again.
func TestSettleRejectedContent(t *testing.T) {
	caller, err := call.NewPeerConnection()
	if err != nil {
		t.Skipf("no usable pion peer connection here: %v", err)
	}
	defer caller.Close()
	callee, err := call.NewPeerConnection()
	if err != nil {
		t.Fatalf("creating callee: %v", err)
	}
	defer callee.Close()

	offer, err := caller.CreateOffer()
	if err != nil {
		t.Skipf("no usable pion peer connection here: %v", err)
	}
	offerContents, err := jingleContentsFromSDP(offer, true)
	if err != nil {
		t.Fatal(err)
	}
	remoteOffer, err := sdpFromJingleContents(offerContents, webrtc.SDPTypeOffer, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := callee.SetRemoteDescription(remoteOffer); err != nil {
		t.Fatalf("callee applying offer: %v", err)
	}
	answer, err := callee.CreateAnswer()
	if err != nil {
		t.Fatal(err)
	}
	answerContents, err := jingleContentsFromSDP(answer, false)
	if err != nil {
		t.Fatal(err)
	}
	remoteAnswer, err := sdpFromJingleContents(answerContents, webrtc.SDPTypeAnswer, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := caller.SetRemoteDescription(remoteAnswer); err != nil {
		t.Fatalf("caller applying answer: %v", err)
	}

	if err := caller.AddVideoTrack(); err != nil {
		t.Fatal(err)
	}
	addOffer, err := caller.CreateOffer()
	if err != nil {
		t.Fatalf("content-add offer: %v", err)
	}
	addContents, err := jingleContentsFromSDP(addOffer, true)
	if err != nil {
		t.Fatal(err)
	}
	var video xmpp.JingleContent
	for _, ct := range addContents {
		if ct.Name == caller.VideoMid() {
			video = ct
		}
	}
	if video.Description == nil {
		t.Fatalf("no content for our video mid %q in %+v", caller.VideoMid(), addContents)
	}

	if err := settleRejectedContent(caller, answerContents, video, false); err != nil {
		t.Fatalf("settleRejectedContent: %v", err)
	}
	if _, err := caller.CreateOffer(); err != nil {
		t.Errorf("offering again after the reject: %v", err)
	}
}
