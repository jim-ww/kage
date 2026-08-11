package xmpp

import (
	"encoding/xml"
	"fmt"
	"io"
	"sync"
	"time"
)

// xmlLog is the writer -debug-xml logs decoded stanzas to (nil when
// disabled, the default). Enabled process-wide via SetXMLLog before any
// account dials — see cmd_daemon.go's runDaemonProcess.
var (
	xmlLogMu sync.Mutex
	xmlLog   io.Writer
)

// SetXMLLog enables (w non-nil) or disables (nil) logging of every
// decoded incoming/outgoing message stanza this package handles, for
// diagnosing interop issues against other XMPP clients — e.g. confirming
// whether a <reply/> element's id/to actually matches what a peer client
// expects. Deliberately logs the decoded Go struct re-marshaled to XML
// rather than tapping the raw TCP bytes, since the bytes below STARTTLS
// are ciphertext anyway.
func SetXMLLog(w io.Writer) {
	xmlLogMu.Lock()
	xmlLog = w
	xmlLogMu.Unlock()
}

// logXML writes v (a decoded or about-to-be-sent stanza struct)
// re-marshaled to XML text, prefixed with direction/account/time. A
// no-op unless SetXMLLog was called.
func logXML(direction, account string, v any) {
	xmlLogMu.Lock()
	defer xmlLogMu.Unlock()
	if xmlLog == nil {
		return
	}
	b, err := xml.Marshal(v)
	if err != nil {
		fmt.Fprintf(xmlLog, "%s [%s %s] marshal error: %v\n", time.Now().Format(time.RFC3339Nano), account, direction, err)
		return
	}
	fmt.Fprintf(xmlLog, "%s [%s %s] %s\n", time.Now().Format(time.RFC3339Nano), account, direction, b)
}
