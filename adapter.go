package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/crypto/aesgcm"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
)

// adapter implements ui.MessageSender and ui.AccountAdder, encrypting
// outgoing bodies when a peer key is configured and persisting sent messages
// to storage. sessions is only ever appended to (by AddAccount, from the
// Bubble Tea event loop's own goroutine via a tea.Cmd) after startup, so
// existing indices stay stable and Send doesn't need to hold mu itself —
// mu only guards the append plus the read of len(sessions) it races with.
type adapter struct {
	mu       sync.Mutex
	sessions []*accountSession
	cfgPath  string
	program  *tea.Program
	queries  *storage.Queries
	localKey []byte
}

// AddAccount implements ui.AccountAdder: resolves and stores the password in
// the OS keyring, persists the account to config.toml, connects it live, and
// starts its supervisor goroutine — mirroring what main does for accounts
// configured at startup, just for one account added mid-session.
func (a *adapter) AddAccount(jid, password, gpgKeyID string) tea.Msg {
	acct := config.Account{JID: jid, GPGKeyID: gpgKeyID}
	if password != "" {
		// Prefer the OS keyring; if that's unavailable (no Secret Service
		// running, headless box, etc.) fall back to storing the password in
		// plaintext in config.toml rather than failing the add outright.
		if err := config.SetKeyringPassword(jid, password); err != nil {
			acct.Password = password
			debugf("warning: storing password in keyring for %s: %v; falling back to plaintext in config\n", jid, err)
		}
	}
	if err := config.WriteAccount(a.cfgPath, acct); err != nil {
		return ui.AccountAddErrorMsg{Err: fmt.Errorf("saving account to config: %w", err)}
	}

	ctx := context.Background()
	sess, uiAcct, err := connectAccount(ctx, acct, a.queries, a.localKey)
	if err != nil {
		return ui.AccountAddErrorMsg{Err: err}
	}

	a.mu.Lock()
	accountIdx := len(a.sessions)
	a.sessions = append(a.sessions, sess)
	a.mu.Unlock()

	go superviseAccount(ctx, a.program, accountIdx, sess)

	return ui.AccountAddedMsg{Account: uiAcct}
}

// session returns the accountSession at accountIdx, guarded by mu since
// AddAccount appends to a.sessions concurrently with reads from here.
func (a *adapter) session(accountIdx int) (*accountSession, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if accountIdx < 0 || accountIdx >= len(a.sessions) || a.sessions[accountIdx] == nil {
		return nil, false
	}
	return a.sessions[accountIdx], true
}

// LoadOlderHistory implements ui.HistoryLoader: fetches the next older page
// of to's persisted history (see loadHistoryPage) as a tea.Cmd, off the main
// goroutine, since it's a disk read plus decrypt of up to a page's worth of
// messages.
func (a *adapter) LoadOlderHistory(accountIdx int, to string) tea.Cmd {
	sess, ok := a.session(accountIdx)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		msgs, hasMore := loadHistoryPage(context.Background(), sess, to, sess.rosterName(to))
		return ui.OlderHistoryMsg{AccountIdx: accountIdx, From: to, Messages: msgs, HasMore: hasMore}
	}
}

// SetDefaultAccount implements ui.DefaultAccountSetter: persists jid as the
// account selected on startup.
func (a *adapter) SetDefaultAccount(jid string) error {
	return config.SetDefaultAccount(a.cfgPath, jid)
}

// SetSidebarWidth implements ui.SidebarWidthSetter: persists the
// user-dragged sidebar width so it's restored on the next launch.
func (a *adapter) SetSidebarWidth(width int) error {
	return config.SetSidebarWidth(a.cfgPath, width)
}

// SetSidebarHidden implements ui.SidebarHiddenSetter: persists the chat
// list's visibility so it's restored on the next launch.
func (a *adapter) SetSidebarHidden(hidden bool) error {
	return config.SetSidebarHidden(a.cfgPath, hidden)
}

// SetInputHeight implements ui.InputHeightSetter: persists the user-dragged
// compose box height so it's restored on the next launch.
func (a *adapter) SetInputHeight(height int) error {
	return config.SetInputHeight(a.cfgPath, height)
}

// SetLastChat implements ui.LastChatSetter: persists which chat was last
// opened so it can be reopened on startup when open_last_chat is set.
func (a *adapter) SetLastChat(accountJID, chatAddress string) error {
	return config.SetLastChat(a.cfgPath, accountJID, chatAddress)
}

// FetchOwnDeviceList implements ui.OmemoDeviceManager: fetches the account's
// currently-published OMEMO device list (XEP-0384 PEP) for the "view/purge
// devices" popup, along with this instance's own device ID.
func (a *adapter) FetchOwnDeviceList(accountIdx int) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: fmt.Errorf("account is still connecting")}
	}
	if s.omemoMgr == nil {
		return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: fmt.Errorf("omemo isn't ready for this account")}
	}
	list, err := s.client.Load().FetchOmemoDeviceList(context.Background(), s.account.JID)
	if err != nil {
		return ui.OmemoDeviceListMsg{AccountIdx: accountIdx, Err: err}
	}
	devices := make([]uint32, len(list.Devices))
	for i, d := range list.Devices {
		devices[i] = uint32(d)
	}
	return ui.OmemoDeviceListMsg{
		AccountIdx: accountIdx,
		Local:      uint32(s.omemoMgr.LocalDevice().ID),
		Devices:    devices,
	}
}

// PurgeOwnDeviceList implements ui.OmemoDeviceManager: republishes the
// account's OMEMO device list containing only the IDs in keep (this
// instance's own device is always included regardless, so it can never
// accidentally remove itself).
func (a *adapter) PurgeOwnDeviceList(accountIdx int, keep []uint32) tea.Msg {
	s, ok := a.session(accountIdx)
	if !ok {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: fmt.Errorf("account is still connecting")}
	}
	if s.omemoMgr == nil {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: fmt.Errorf("omemo isn't ready for this account")}
	}
	local := uint32(s.omemoMgr.LocalDevice().ID)
	hasLocal := false
	devices := make([]omemolib.DeviceID, 0, len(keep)+1)
	for _, id := range keep {
		devices = append(devices, omemolib.DeviceID(id))
		if id == local {
			hasLocal = true
		}
	}
	if !hasLocal {
		devices = append(devices, omemolib.DeviceID(local))
	}

	ctx := context.Background()
	if err := s.client.Load().PublishOmemoDeviceList(ctx, omemolib.DeviceList{JID: s.account.JID, Devices: devices}); err != nil {
		return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Err: err}
	}

	out := make([]uint32, len(devices))
	for i, d := range devices {
		out[i] = uint32(d)
	}
	return ui.OmemoDevicePurgedMsg{AccountIdx: accountIdx, Local: local, Devices: out}
}

// SetTyping implements ui.MessageSender: sends a XEP-0085 chat state
// notification to "to" — no persistence, no encryption, it's ephemeral.
func (a *adapter) SetTyping(accountIdx int, to string, composing bool) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	state := xmpp.ChatStateActive
	if composing {
		state = xmpp.ChatStateComposing
	}
	return s.client.Load().SendChatState(context.Background(), to, state)
}

// RenameContact implements ui.ContactRenamer: pushes name as a roster set
// (RFC 6121) to the server, and mirrors it into local storage and the
// in-memory roster cache used by rosterName. An empty name clears the
// contact's nickname there too, matching what a fresh roster fetch would
// show after the server applies the same change.
func (a *adapter) RenameContact(accountIdx int, address, name string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}

	if err := s.client.Load().SetRosterName(context.Background(), address, name); err != nil {
		return err
	}

	subs := ""
	if entries := s.roster.Load(); entries != nil {
		subs = (*entries)[address].Subs
	}
	if err := s.db.UpsertRoster(context.Background(), storage.UpsertRosterParams{
		AccountJid: s.account.JID, Jid: address, Name: name, Subs: subs,
	}); err != nil {
		debugf("warning: persisting renamed roster entry %s: %v\n", address, err)
	}

	updated := make(map[string]rosterEntry)
	if entries := s.roster.Load(); entries != nil {
		for k, v := range *entries {
			updated[k] = v
		}
	}
	updated[address] = rosterEntry{Name: name, Subs: subs}
	s.roster.Store(&updated)

	return nil
}

// SetChatEncryption implements ui.ChatEncryptionSetter: persists the chosen
// outgoing message encryption ("omemo", "gpg", or "none") for one chat.
func (a *adapter) SetChatEncryption(accountIdx int, peerJID, mode string) error {
	s, ok := a.session(accountIdx)
	if !ok {
		return fmt.Errorf("unknown account %d", accountIdx)
	}
	return s.db.SetChatEncryptionMode(context.Background(), storage.SetChatEncryptionModeParams{
		AccountJid: s.account.JID,
		RosterJid:  peerJID,
		Mode:       mode,
	})
}

func (a *adapter) Send(accountIdx int, to, body string, opts ui.SendOptions) (string, error) {
	return a.send(context.Background(), accountIdx, to, body, opts)
}

// send is the context-aware implementation behind Send. File uploads use it
// with their deadline so a subsequent peer-key lookup or stanza send cannot
// outlive the operation that initiated it.
func (a *adapter) send(ctx context.Context, accountIdx int, to, body string, opts ui.SendOptions) (string, error) {
	s, valid := a.session(accountIdx)
	if !valid {
		return "", fmt.Errorf("unknown account %d", accountIdx)
	}

	if opts.ReactionTargetID != "" {
		id, err := s.client.Load().Send(ctx, to, "", xmpp.SendOptions{
			ReactionTargetID: opts.ReactionTargetID,
			Reactions:        opts.Reactions,
		})
		if err != nil {
			return "", err
		}
		if err := replaceReactions(ctx, s, to, opts.ReactionTargetID, meReactorJID, opts.Reactions); err != nil {
			debugf("warning: persisting our own reactions: %v\n", err)
		}
		return id, nil
	}

	if opts.RetractID != "" {
		// The retraction body is fixed fallback text, not user content —
		// nothing to encrypt for the peer. Once the peer's been told to
		// retract it, there's no reason to keep our own copy either.
		id, err := s.client.Load().Send(ctx, to, "", xmpp.SendOptions{RetractID: opts.RetractID})
		if err != nil {
			return "", err
		}
		if _, err := s.db.DeleteMessageByID(ctx, storage.DeleteMessageByIDParams{
			AccountJid: s.account.JID,
			IDAttr:     nullString(opts.RetractID),
			RosterJid:  nullString(to),
		}); err != nil {
			debugf("warning: deleting retracted message from storage: %v\n", err)
		}
		return id, nil
	}

	plaintext := body
	if opts.ReplyToID != "" {
		// Build quoted text for encrypted replies - the quoted text is inside the encrypted payload
		// Don't include author name in encrypted quotes (just the quoted content)
		lines := strings.Split(opts.QuotedBody, "\n")
		for i, l := range lines {
			lines[i] = "> " + l
		}
		quote := strings.Join(lines, "\n") + "\n"
		plaintext = quote + body
	}

	wireBody := plaintext
	sendOpts := xmpp.SendOptions{
		ReplaceID:    opts.ReplaceID,
		ReplyToID:    opts.ReplyToID,
		QuotedAuthor: opts.QuotedAuthor,
		QuotedBody:   opts.QuotedBody,
	}

	e2eEncrypted := false
	e2eeMethod := ""
	switch resolveEncryptionMode(ctx, s, to) {
	case "omemo":
		debugf("send: using omemo encryption for %s to %s", s.account.JID, to)
		if s.omemoMgr != nil {
			enc, deviceErrs, err := s.omemoMgr.EncryptMessage(ctx, to, []byte(plaintext))
			if err != nil {
				// The manager only auto-fetches a peer's device list when its
				// cache is empty, so a peer that rotated/added devices since
				// our last fetch looks unencryptable until we force a resync.
				debugf("send: omemo encrypt failed for %s to %s: %v (device errors: %v); forcing device resync", s.account.JID, to, err, deviceErrs)
				if syncErr := s.omemoMgr.SyncDevices(ctx, to); syncErr != nil {
					debugf("send: omemo device resync for %s failed: %v", to, syncErr)
					return "", fmt.Errorf("omemo-encrypting to %s: device list resync failed: %w", to, syncErr)
				}
				enc, deviceErrs, err = s.omemoMgr.EncryptMessage(ctx, to, []byte(plaintext))
				if err != nil {
					debugf("send: omemo encrypt failed for %s to %s after resync: %v (device errors: %v)", s.account.JID, to, err, deviceErrs)
					return "", fmt.Errorf("omemo-encrypting to %s: %w (device errors: %v)", to, err, deviceErrs)
				}
			}
			debugf("send: omemo encrypt succeeded for %s to %s, %d keys", s.account.JID, to, len(enc.Keys))
			sendOpts.Encrypted = xmpp.EncodeOmemoMessage(enc)
			wireBody = ""
			e2eEncrypted = true
			e2eeMethod = "omemo"
		} else {
			debugf("note: omemo not ready for %s; sending unencrypted\n", s.account.JID)
		}
	case "gpg":
		if peerKey := resolvePeerKey(ctx, s, to); peerKey != "" {
			ct, err := s.gpg.Encrypt(plaintext, peerKey)
			if err != nil {
				return "", fmt.Errorf("encrypting to %s: %w", to, err)
			}
			wireBody = ct
			e2eEncrypted = true
			e2eeMethod = "gpg"
		}
	}

	id, err := s.client.Load().Send(ctx, to, wireBody, sendOpts)
	if err != nil {
		return "", err
	}

	if opts.ReplaceID != "" {
		// A correction amends the original message in place; it isn't a new
		// row in history.
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid: s.account.JID,
			Body:       sealedBody,
			Encrypted:  encrypted,
			IDAttr:     nullString(opts.ReplaceID),
			RosterJid:  nullString(to),
		}); err != nil {
			debugf("warning: persisting correction: %v\n", err)
		}
		return id, nil
	}

	sealedBody, encrypted := encryptForStorage(s, body)
	if _, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:    s.account.JID,
		Sent:          true,
		ToAttr:        nullString(to),
		IDAttr:        nullString(id),
		Body:          sealedBody,
		Encrypted:     encrypted,
		E2eEncrypted:  e2eEncrypted,
		E2eeMethod:    nullString(e2eeMethod),
		StanzaType:    "chat",
		RosterJid:     nullString(to),
		ReplyToIDAttr: nullString(opts.ReplyToID),
	}); err != nil {
		debugf("warning: persisting sent message: %v\n", err)
	}
	return id, nil
}

// SendFile implements ui.FileSender. Uploading and the follow-up message send
// run as a Bubble Tea command, so the terminal remains responsive while a
// slot is discovered and the file is transferred.
func (a *adapter) SendFile(accountIdx int, to, path string, opts ui.SendOptions) tea.Msg {
	result := ui.FileSendResultMsg{AccountIdx: accountIdx, To: to, Path: path, ReplyToID: opts.ReplyToID}
	s, ok := a.session(accountIdx)
	if !ok {
		result.Err = fmt.Errorf("unknown account %d", accountIdx)
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Determine if we should encrypt the file
	encryptFile := false
	switch resolveEncryptionMode(ctx, s, to) {
	case "omemo":
		encryptFile = s.omemoMgr != nil
	case "gpg":
		encryptFile = resolvePeerKey(ctx, s, to) != ""
	}

	var url string
	var err error

	if encryptFile {
		// Encrypt file with AES-256-GCM before upload (XEP-0454)
		f, err := os.Open(path)
		if err != nil {
			result.Err = fmt.Errorf("opening file: %w", err)
			return result
		}
		defer f.Close()

		ciphertext, iv, key, err := aesgcm.EncryptReader(f)
		if err != nil {
			result.Err = fmt.Errorf("encrypting file: %w", err)
			return result
		}

		// Upload encrypted data
		reader := bytes.NewReader(ciphertext)
		url, err = s.client.Load().UploadFileWithReader(ctx, path, reader)
		if err != nil {
			result.Err = fmt.Errorf("uploading encrypted file: %w", err)
			return result
		}

		// Build aesgcm:// URL with IV+key in anchor
		url, err = aesgcm.BuildAESGCMURL(url, iv, key)
		if err != nil {
			result.Err = fmt.Errorf("building aesgcm URL: %w", err)
			return result
		}
	} else {
		// Unencrypted upload
		url, err = s.client.Load().UploadFile(ctx, path)
		if err != nil {
			result.Err = err
			return result
		}
	}

	id, err := a.send(ctx, accountIdx, to, url, opts)
	if err != nil {
		result.Err = err
		return result
	}
	result.URL = url
	result.ID = id
	return result
}
