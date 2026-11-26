package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
)

// gpgLockedPromptInterval is how often a TUI is asked again to unlock a key
// that stays locked as more messages for it arrive.
const gpgLockedPromptInterval = time.Minute

// gpgPendingMsg is a stored incoming message whose gpg body couldn't be
// decrypted for a locked key: the daemon has no terminal for a pinentry to
// ask on. Retried by RetryGPGDecrypt once a TUI unlocked the key.
type gpgPendingMsg struct {
	storeID int64
	from    string // bare JID (chat)
	armored string
}

// gpgPending is an account's gpgPendingMsgs.
type gpgPending struct {
	mu         sync.Mutex
	items      []gpgPendingMsg
	promptedAt time.Time
}

// add records p, reporting whether TUIs should be asked to unlock: not
// again within gpgLockedPromptInterval.
func (g *gpgPending) add(p gpgPendingMsg, now time.Time) (prompt bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.items = append(g.items, p)
	if now.Sub(g.promptedAt) < gpgLockedPromptInterval {
		return false
	}
	g.promptedAt = now
	return true
}

// take empties the list, returning what it held.
func (g *gpgPending) take() []gpgPendingMsg {
	g.mu.Lock()
	defer g.mu.Unlock()
	items := g.items
	g.items = nil
	return items
}

// putBack returns items take handed out and couldn't be decrypted.
func (g *gpgPending) putBack(items []gpgPendingMsg) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.items = append(items, g.items...)
}

// gpgDecryptLocked records a stored message that failed to decrypt for a
// locked key, and asks attached TUIs to unlock it.
func gpgDecryptLocked(srv *ipc.Server, accountIdx int, s *accountSession, p gpgPendingMsg) {
	if s.gpgPending.add(p, time.Now()) {
		broadcast(srv, ui.GPGLockedMsg{AccountIdx: accountIdx, KeyID: s.account.GPGKeyID})
	}
}

// RetryGPGDecrypt decrypts the account's messages that arrived while its gpg
// key was locked, updating them in storage and on every TUI.
func (a *adapter) RetryGPGDecrypt(accountIdx int) error {
	s, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	return retryGPGDecrypt(context.Background(), a.srv, accountIdx, s, s.gpg.Decrypt, s.db.UpdateMessageBodyByRowID)
}

func retryGPGDecrypt(
	ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession,
	decrypt func(ciphertext, sender string) (string, error),
	update func(context.Context, storage.UpdateMessageBodyByRowIDParams) error,
) error {
	var still []gpgPendingMsg
	var lastErr error
	for _, p := range s.gpgPending.take() {
		pt, err := decrypt(p.armored, "")
		if err != nil {
			still, lastErr = append(still, p), err
			continue
		}
		body := stripReplyQuote(pt)
		sealed, encrypted := encryptForStorage(s, body)
		if err := update(ctx, storage.UpdateMessageBodyByRowIDParams{ID: p.storeID, Body: sealed, Encrypted: encrypted}); err != nil {
			slog.Warn("storing decrypted gpg message", "jid", s.account.JID, "store_id", p.storeID, "err", err)
			still, lastErr = append(still, p), err
			continue
		}
		broadcast(srv, ui.MessageDecryptedMsg{AccountIdx: accountIdx, From: p.from, StoreID: p.storeID, Content: body})
	}
	s.gpgPending.putBack(still)
	if len(still) > 0 {
		return fmt.Errorf("%d gpg message(s) still can't be decrypted: %w", len(still), lastErr)
	}
	return nil
}
