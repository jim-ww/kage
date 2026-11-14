package call

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeViewer is a frameQueue's viewer that takes frames only when let.
type fakeViewer struct {
	mu      sync.Mutex
	got     []byte // each frame's first byte, its number
	writing chan struct{}
	gate    chan error
}

func newFakeViewer() *fakeViewer {
	return &fakeViewer{writing: make(chan struct{}, 100), gate: make(chan error)}
}

func (v *fakeViewer) write(frame []byte) error {
	v.writing <- struct{}{}
	if err := <-v.gate; err != nil {
		return err
	}
	v.mu.Lock()
	v.got = append(v.got, frame[0])
	v.mu.Unlock()
	return nil
}

func (v *fakeViewer) frames() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.got)
}

// videoFrame is frame number n, its second byte marking it a keyframe
// for isTestKeyframe.
func videoFrame(n int, key bool) []byte {
	if key {
		return []byte{byte(n), 1}
	}
	return []byte{byte(n), 0}
}

func isTestKeyframe(frame []byte) bool { return frame[1] == 1 }

// testQueue returns a frameQueue writing to v on a fake clock, and a
// function advancing the clock.
func testQueue(v *fakeViewer, needKeyframe func()) (*frameQueue, func(time.Duration)) {
	q := newFrameQueue(v.write, isTestKeyframe, needKeyframe)
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	q.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	return q, func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
}

// drain lets the viewer take everything queued.
func drain(t *testing.T, q *frameQueue, v *fakeViewer) {
	t.Helper()
	for {
		select {
		case <-v.writing:
			v.gate <- nil
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

const videoFrameGap = 33 * time.Millisecond

// TestFrameQueueKeepsUp checks a viewer that keeps up gets every frame in
// order.
func TestFrameQueueKeepsUp(t *testing.T) {
	v := newFakeViewer()
	q, tick := testQueue(v, nil)
	defer q.Close()
	for i := range 60 {
		if err := q.Push(videoFrame(i, i%30 == 0)); err != nil {
			t.Fatal(err)
		}
		<-v.writing
		v.gate <- nil
		tick(videoFrameGap)
	}
	drain(t, q, v)
	if got := v.frames(); len(got) != 60 || !slices.IsSorted(got) {
		t.Fatalf("viewer got %v, want all 60 frames in order", got)
	}
}

// TestFrameQueueSkipsToKeyframe checks a viewer that's stuck past
// maxVideoLag resumes at the newest keyframe rather than at the oldest
// frame waiting.
func TestFrameQueueSkipsToKeyframe(t *testing.T) {
	v := newFakeViewer()
	q, tick := testQueue(v, nil)
	defer q.Close()
	q.Push(videoFrame(0, true))
	<-v.writing // stuck writing frame 0
	for i := 1; i < 12; i++ {
		tick(videoFrameGap)
		q.Push(videoFrame(i, i == 5))
	}
	v.gate <- nil
	drain(t, q, v)
	if got, want := v.frames(), []byte{0, 5, 6, 7, 8, 9, 10, 11}; !slices.Equal(got, want) {
		t.Fatalf("viewer got %v, want %v", got, want)
	}
}

// TestFrameQueueWaitsForKeyframe checks a viewer that's stuck past
// maxVideoLag with no keyframe queued to resume at gets nothing until the
// next keyframe, which is asked for.
func TestFrameQueueWaitsForKeyframe(t *testing.T) {
	v := newFakeViewer()
	asked := 0
	q, tick := testQueue(v, func() { asked++ })
	defer q.Close()
	q.Push(videoFrame(0, true))
	<-v.writing
	for i := 1; i < 20; i++ {
		tick(videoFrameGap)
		q.Push(videoFrame(i, i == 15))
	}
	v.gate <- nil
	drain(t, q, v)
	if got, want := v.frames(), []byte{0, 15, 16, 17, 18, 19}; !slices.Equal(got, want) {
		t.Fatalf("viewer got %v, want %v", got, want)
	}
	if asked != 1 {
		t.Errorf("keyframe asked for %d times, want once", asked)
	}
}

// TestFrameQueueReportsWriteError checks a failed write (the window was
// closed) is returned from the next Push.
func TestFrameQueueReportsWriteError(t *testing.T) {
	v := newFakeViewer()
	q, _ := testQueue(v, nil)
	defer q.Close()
	q.Push(videoFrame(0, true))
	<-v.writing
	closed := errors.New("window closed")
	v.gate <- closed
	q.Wait()
	if err := q.Push(videoFrame(1, false)); !errors.Is(err, closed) {
		t.Fatalf("Push returned %v, want %v", err, closed)
	}
}
