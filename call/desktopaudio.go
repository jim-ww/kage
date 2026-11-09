package call

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// desktopAudioPoll is how often DesktopAudio looks for newly started
// playback streams to link in - a stream that starts mid-share is heard
// within about this long.
const desktopAudioPoll = time.Second

// DesktopAudio captures what other applications are playing, for mixing into
// a call's outgoing audio while sharing the screen. It's a PipeWire capture
// stream (pw-record) that isn't connected to any device; instead every
// application playback stream except this process's own is linked into it
// (pw-link), and kept so as streams come and go (pw-dump, polled). Leaving
// this process's streams out is the point: a plain monitor of the speakers
// would also capture the peer's own voice coming out of them and send it
// straight back as an echo. PipeWire mixes the links on the capture port,
// and drops them by itself once pw-record exits - nothing is left behind in
// the user's audio graph.
type DesktopAudio struct {
	cmd    *exec.Cmd
	frames chan []int16
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// NewDesktopAudio starts capturing. It needs PipeWire's pw-record, pw-dump
// and pw-link on PATH.
func NewDesktopAudio() (*DesktopAudio, error) {
	for _, tool := range []string{"pw-record", "pw-dump", "pw-link"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("desktop audio needs PipeWire's %s: %w", tool, err)
		}
	}
	nodeName := fmt.Sprintf("kage-desktop-audio-%d-%d", os.Getpid(), time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "pw-record",
		"--raw", "--format", "s16", "--rate", strconv.Itoa(SampleRate), "--channels", strconv.Itoa(Channels),
		"--target", "0", // don't autoconnect: linkDesktopStreams picks the sources
		"--latency", "20ms",
		"-P", fmt.Sprintf(`{ "node.name": %q, "node.description": "kage desktop audio" }`, nodeName),
		"-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting pw-record: %w", err)
	}
	d := &DesktopAudio{cmd: cmd, frames: make(chan []int16, 10), cancel: cancel, done: make(chan struct{})}

	go func() {
		defer close(d.done)
		readPCMFrames(stdout, d.frames)
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			slog.Warn("desktop audio: pw-record exited", "err", err, "stderr", lastLine(stderr.String()))
		}
	}()
	go d.linkLoop(ctx, nodeName)
	return d, nil
}

// Frames returns captured FrameSamples-sized mono frames. Closed once
// capture stops. A frame not taken in time is dropped rather than queued, so
// the desktop audio never drifts further behind than the channel holds.
func (d *DesktopAudio) Frames() <-chan []int16 {
	return d.frames
}

// Close stops capturing.
func (d *DesktopAudio) Close() {
	d.once.Do(func() {
		d.cancel()
		<-d.done
	})
}

// readPCMFrames splits a raw s16le mono stream into FrameSamples frames on
// out until r ends, then closes out. A full out drops the frame.
func readPCMFrames(r io.Reader, out chan<- []int16) {
	defer close(out)
	buf := make([]byte, FrameSamples*2)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		frame := make([]int16, FrameSamples)
		for i := range frame {
			frame[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
		}
		select {
		case out <- frame:
		default:
		}
	}
}

// linkLoop keeps every other application's playback stream linked into the
// capture node named nodeName until ctx ends.
func (d *DesktopAudio) linkLoop(ctx context.Context, nodeName string) {
	ticker := time.NewTicker(desktopAudioPoll)
	defer ticker.Stop()
	pid := os.Getpid()
	for {
		dump, err := exec.CommandContext(ctx, "pw-dump").Output()
		if err == nil {
			links, err := desktopAudioLinks(dump, nodeName, pid)
			if err != nil {
				slog.Debug("desktop audio: reading pw-dump", "err", err)
			}
			for _, l := range links {
				if out, err := exec.CommandContext(ctx, "pw-link", strconv.Itoa(l.out), strconv.Itoa(l.in)).CombinedOutput(); err != nil && ctx.Err() == nil {
					slog.Debug("desktop audio: linking", "out", l.out, "in", l.in, "err", err, "output", string(out))
				}
			}
		} else if ctx.Err() == nil {
			slog.Debug("desktop audio: pw-dump", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// portLink is one output port -> input port link to create, by PipeWire
// object id.
type portLink struct{ out, in int }

// pwObject is the part of a pw-dump entry desktopAudioLinks reads.
type pwObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info struct {
		Direction    string         `json:"direction"`
		Props        map[string]any `json:"props"`
		OutputPortID int            `json:"output-port-id"`
		InputPortID  int            `json:"input-port-id"`
	} `json:"info"`
}

// desktopAudioLinks works out, from a pw-dump snapshot, which links are
// still missing between other processes' playback streams and the capture
// node named nodeName: every output port of every "Stream/Output/Audio"
// node not owned by process pid, to every input port of the capture node.
// Mono capture means a stereo stream's FL and FR both land on the one input
// port, where PipeWire sums them.
//
// Who owns a node is recorded in two places: a PulseAudio client's stream
// (kage's own playback - miniaudio talks PulseAudio) carries
// application.process.id on the node itself, while a native PipeWire
// stream only names its client.id, whose Client object carries the pid.
// Both are checked.
func desktopAudioLinks(dump []byte, nodeName string, pid int) ([]portLink, error) {
	var objects []pwObject
	if err := json.Unmarshal(dump, &objects); err != nil {
		return nil, err
	}
	self := strconv.Itoa(pid)
	ownClients := map[string]bool{}
	for _, o := range objects {
		if o.Type == "PipeWire:Interface:Client" && (propString(o.Info.Props, "application.process.id") == self || propString(o.Info.Props, "pipewire.sec.pid") == self) {
			ownClients[strconv.Itoa(o.ID)] = true
		}
	}
	capture := -1
	sources := map[int]bool{}
	for _, o := range objects {
		if o.Type != "PipeWire:Interface:Node" {
			continue
		}
		props := o.Info.Props
		if propString(props, "node.name") == nodeName {
			capture = o.ID
			continue
		}
		own := propString(props, "application.process.id") == self || ownClients[propString(props, "client.id")]
		if propString(props, "media.class") == "Stream/Output/Audio" && !own {
			sources[o.ID] = true
		}
	}
	if capture < 0 {
		return nil, errors.New("capture node not found yet")
	}

	var outs, ins []int
	linked := map[portLink]bool{}
	for _, o := range objects {
		switch o.Type {
		case "PipeWire:Interface:Port":
			node, _ := strconv.Atoi(propString(o.Info.Props, "node.id"))
			if o.Info.Direction == "output" && sources[node] && propString(o.Info.Props, "port.monitor") != "true" {
				outs = append(outs, o.ID)
			}
			if o.Info.Direction == "input" && node == capture {
				ins = append(ins, o.ID)
			}
		case "PipeWire:Interface:Link":
			linked[portLink{o.Info.OutputPortID, o.Info.InputPortID}] = true
		}
	}
	var missing []portLink
	for _, out := range outs {
		for _, in := range ins {
			if l := (portLink{out, in}); !linked[l] {
				missing = append(missing, l)
			}
		}
	}
	return missing, nil
}

// propString reads a pw-dump property as a string whatever JSON type it was
// dumped as - ids and pids show up as numbers or strings depending on who
// set them.
func propString(props map[string]any, key string) string {
	switch v := props[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

// MixFrame adds src into dst sample by sample, clipping at int16's range.
func MixFrame(dst, src []int16) {
	for i := range min(len(dst), len(src)) {
		dst[i] = int16(max(-32768, min(32767, int32(dst[i])+int32(src[i]))))
	}
}
