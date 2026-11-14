package call

import (
	"log/slog"
	"slices"
	"sync"
	"time"
)

// maxVideoLag is how long a received frame may wait for the viewer before
// the viewer counts as having fallen behind.
const maxVideoLag = 250 * time.Millisecond

// frameQueue hands video frames to a viewer from a goroutine of its own,
// so whoever receives them never waits on the viewer. If the viewer can't
// keep up (a slow decoder, or a hidden window its compositor throttles),
// it doesn't fall ever further behind: once a frame has waited past
// maxVideoLag, the queue skips ahead to its newest keyframe or, holding
// none, drops frames until the next one comes - unlike audio, video can't
// be played faster, and a decoder can only pick up again at a keyframe.
type frameQueue struct {
	write        func([]byte) error
	isKeyframe   func([]byte) bool
	needKeyframe func() // asked for one when frames are dropped waiting for one
	now          func() time.Time

	mu           sync.Mutex
	frames       []queuedFrame
	waitKeyframe bool
	err          error // the write that failed, ending the queue
	closed       bool

	wake   chan struct{}
	done   chan struct{}
	exited chan struct{}
	once   sync.Once
}

type queuedFrame struct {
	data []byte
	at   time.Time
	key  bool
}

func newFrameQueue(write func([]byte) error, isKeyframe func([]byte) bool, needKeyframe func()) *frameQueue {
	q := &frameQueue{
		write:        write,
		isKeyframe:   isKeyframe,
		needKeyframe: needKeyframe,
		now:          time.Now,
		wake:         make(chan struct{}, 1),
		done:         make(chan struct{}),
		exited:       make(chan struct{}),
	}
	go q.run()
	return q
}

// Push queues frame for the viewer without waiting for it. It returns the
// error a write to the viewer failed with, after which nothing more is
// written.
func (q *frameQueue) Push(frame []byte) error {
	q.mu.Lock()
	if q.err != nil || q.closed {
		err := q.err
		q.mu.Unlock()
		return err
	}
	key := q.isKeyframe(frame)
	if q.waitKeyframe && !key {
		q.mu.Unlock()
		return nil
	}
	q.waitKeyframe = false
	now := q.now()
	q.frames = append(q.frames, queuedFrame{frame, now, key})
	ask := false
	if lag := now.Sub(q.frames[0].at); lag > maxVideoLag {
		k := len(q.frames) - 1
		for k >= 0 && !q.frames[k].key {
			k--
		}
		dropped := len(q.frames)
		if k > 0 {
			q.frames = slices.Clone(q.frames[k:])
			dropped = k
		} else {
			q.frames, q.waitKeyframe, ask = nil, true, true
		}
		slog.Debug("video viewer fell behind, skipping ahead", "lag", lag, "dropped", dropped, "waiting_for_keyframe", ask)
	}
	q.mu.Unlock()

	select {
	case q.wake <- struct{}{}:
	default:
	}
	if ask && q.needKeyframe != nil {
		q.needKeyframe()
	}
	return nil
}

func (q *frameQueue) run() {
	defer close(q.exited)
	for {
		select {
		case <-q.wake:
		case <-q.done:
			return
		}
		for {
			q.mu.Lock()
			if q.closed || len(q.frames) == 0 {
				q.mu.Unlock()
				break
			}
			f := q.frames[0]
			q.frames[0] = queuedFrame{}
			q.frames = q.frames[1:]
			q.mu.Unlock()

			if err := q.write(f.data); err != nil {
				q.mu.Lock()
				q.err, q.frames = err, nil
				q.mu.Unlock()
				return
			}
		}
	}
}

// Close stops writing. A write in progress is left to whatever closes the
// viewer to unblock; Wait waits for it.
func (q *frameQueue) Close() {
	q.once.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		close(q.done)
	})
}

// Wait waits for the writing goroutine to exit, after Close or a failed
// write.
func (q *frameQueue) Wait() {
	<-q.exited
}
