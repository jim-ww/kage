package ui

// Autofocus is whose new messages in the open chat get selected and
// scrolled to as they arrive: none (the default - the chat only follows
// along if it's already at the bottom), your own, everyone else's, or all.
type Autofocus string

const (
	AutofocusOff    Autofocus = ""
	AutofocusOwn    Autofocus = "own"
	AutofocusOthers Autofocus = "others"
	AutofocusAll    Autofocus = "all"
)

// Valid reports whether a is one of the Autofocus values.
func (a Autofocus) Valid() bool {
	switch a {
	case AutofocusOff, AutofocusOwn, AutofocusOthers, AutofocusAll:
		return true
	}
	return false
}

// covers reports whether a focuses a new message sent by me (isMe) or by
// someone else.
func (a Autofocus) covers(isMe bool) bool {
	switch a {
	case AutofocusAll:
		return true
	case AutofocusOwn:
		return isMe
	case AutofocusOthers:
		return !isMe
	}
	return false
}

// followNew is followTail for a message just appended to the open chat:
// when autofocus covers whoever sent it, it's selected and scrolled to
// wherever the chat was; otherwise the chat follows as followTail does.
func (m *Model) followNew(isMe bool) {
	if !m.autofocus.covers(isMe) {
		m.followTail()
		return
	}
	m.selectedMsg = len(m.currentMessages()) - 1
	m.refreshViewport()
	m.viewport.GotoBottom()
}
