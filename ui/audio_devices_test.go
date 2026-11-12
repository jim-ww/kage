package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakeAudioDevices struct {
	stubDaemon
	list AudioDevicesMsg
	set  []string
}

func (f *fakeAudioDevices) ListAudioDevices() tea.Msg { return f.list }
func (f *fakeAudioDevices) SetAudioDevice(output bool, name string) tea.Msg {
	f.set = append(f.set, fmt.Sprintf("output=%v %q", output, name))
	return nil
}

// pressAndDeliver is press, but feeds the command's resulting message back
// through Update - for flows that round-trip through the daemon.
func pressAndDeliver(t *testing.T, m Model, key tea.KeyPressMsg) Model {
	t.Helper()
	next, cmd := m.Update(key)
	m = next.(Model)
	if msg := nonIdleCmd(cmd); msg != nil {
		next, _ = m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestAudioDevicePicker(t *testing.T) {
	devs := &fakeAudioDevices{list: AudioDevicesMsg{
		Inputs:  []AudioDevice{{Name: "Built-in Mic", Default: true}, {Name: "USB Mic"}},
		Outputs: []AudioDevice{{Name: "Speakers", Default: true}, {Name: "Headphones"}},
		Output:  "Headphones",
	}}
	m := callTestModel(&fakeCallController{})
	m.daemon = devs

	m = pressAndDeliver(t, m, ctrlG)
	m = pressAndDeliver(t, m, letter('d'))
	if menuOf(m) == nil || menuTitle(m) != "Audio devices" {
		t.Fatalf("d didn't open the audio devices menu: %+v", menuOf(m))
	}
	labels := strings.Join(callMenuLabels(m), "|")
	if labels != "Microphone: System default|Speaker: Headphones" {
		t.Fatalf("audio devices menu: %s", labels)
	}

	m = pressAndDeliver(t, m, letter('s'))
	labels = strings.Join(callMenuLabels(m), "|")
	if labels != "System default|Speakers|✓ Headphones" {
		t.Fatalf("speaker picker: %s", labels)
	}
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(devs.set) != 1 || devs.set[0] != `output=true "Speakers"` {
		t.Fatalf("picking Speakers sent %v", devs.set)
	}
}

// TestAudioDevicePickerKeepsMissingChoice checks a picked device that's
// unplugged still shows (checked), so it's clear why the default is in use.
func TestAudioDevicePickerKeepsMissingChoice(t *testing.T) {
	m := callTestModel(&fakeCallController{})
	m.daemon = &fakeAudioDevices{}
	m.openAudioDevicePicker("Microphone", false, []AudioDevice{{Name: "Built-in Mic"}}, "USB Mic")
	if got := strings.Join(callMenuLabels(m), "|"); got != "System default|Built-in Mic|✓ USB Mic (not connected)" {
		t.Fatalf("picker: %s", got)
	}
}

// TestAudioDevicesUnaskedListingIgnored checks a listing nobody asked for
// (or one arriving after the user moved on) doesn't pop a menu up.
func TestAudioDevicesUnaskedListingIgnored(t *testing.T) {
	m := callTestModel(&fakeCallController{})
	m.daemon = &fakeAudioDevices{}
	next, _ := m.Update(AudioDevicesMsg{})
	if menuOf(next.(Model)) != nil {
		t.Fatal("unrequested device listing opened a menu")
	}
}

// TestCallMenuDevicesWithoutChat checks device settings are reachable with
// no chat to call.
func TestCallMenuDevicesWithoutChat(t *testing.T) {
	m := newTestModel(nil)
	m.daemon = &fakeCallController{}
	m.daemon = &fakeAudioDevices{}
	m = press(t, m, ctrlG)
	if got := strings.Join(callMenuLabels(m), "|"); got != "Audio devices" {
		t.Fatalf("call menu without a chat: %q", got)
	}
}

// TestAudioDeviceLabelFlattensWhitespace checks multi-line/tabbed backend
// names (ALSA's) display on one line, while the raw name is what's sent.
func TestAudioDeviceLabelFlattensWhitespace(t *testing.T) {
	devs := &fakeAudioDevices{}
	m := callTestModel(&fakeCallController{})
	m.daemon = devs
	raw := "HDA Intel PCH, ALC257\n\tFront output"
	m.openAudioDevicePicker("Speaker", true, []AudioDevice{{Name: raw}}, "")
	if got := callMenuLabels(m)[1]; got != "HDA Intel PCH, ALC257 Front output" {
		t.Fatalf("label %q", got)
	}
	m = pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	pressAndDeliver(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(devs.set) != 1 || devs.set[0] != fmt.Sprintf("output=true %q", raw) {
		t.Fatalf("sent %v, want the raw name", devs.set)
	}
}
