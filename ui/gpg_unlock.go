package ui

import (
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/jim-ww/kage/ui/modal"
)

// SetGPGUnlock sets how a locked gpg key gets unlocked on this terminal:
// cmd runs with the terminal handed over, so a tty pinentry can take the
// whole screen. Without it, GPGLockedMsg is ignored.
func (m *Model) SetGPGUnlock(cmd func(keyID string) *exec.Cmd) {
	m.gpgUnlock = cmd
}

// gpgUnlockedMsg is the unlock command for accountIdx's key finished.
type gpgUnlockedMsg struct {
	accountIdx int
	err        error
}

// gpgRetriedMsg is Daemon.RetryGPGDecrypt finished.
type gpgRetriedMsg struct{ err error }

// askGPGUnlock offers to unlock the key msg reports locked, unless already
// asking or unlocking.
func (m *Model) askGPGUnlock(msg GPGLockedMsg) tea.Cmd {
	if m.gpgUnlock == nil || m.gpgUnlocking || msg.KeyID == "" ||
		msg.AccountIdx < 0 || msg.AccountIdx >= len(m.accounts) {
		return nil
	}
	m.gpgUnlocking = true
	name := m.accounts[msg.AccountIdx].DisplayName()
	c := modal.NewConfirm("gpg messages for " + name + " need key " + msg.KeyID + " unlocked to be read. Unlock it now?")
	c.Yes, c.No, c.Match = m.keys.ConfirmYes, m.keys.ConfirmNo, matchesKey
	return m.openModal(&modal.Modal[*Model]{
		Title: "Unlock gpg key?",
		Body:  c,
		OnSubmit: func(m *Model, _ any) tea.Cmd {
			return tea.ExecProcess(m.gpgUnlock(msg.KeyID), func(err error) tea.Msg {
				return gpgUnlockedMsg{accountIdx: msg.AccountIdx, err: err}
			})
		},
		OnCancel: func(m *Model) tea.Cmd {
			m.gpgUnlocking = false
			return nil
		},
	})
}

// gpgUnlocked has the daemon read what arrived while the key was locked.
func (m *Model) gpgUnlocked(msg gpgUnlockedMsg) tea.Cmd {
	m.gpgUnlocking = false
	if msg.err != nil {
		return m.showNotification("unlocking gpg key: " + msg.err.Error())
	}
	d := m.daemon
	return func() tea.Msg {
		return gpgRetriedMsg{err: d.RetryGPGDecrypt(msg.accountIdx)}
	}
}

// applyMessageDecrypted replaces a message's ciphertext with what it says.
func (m *Model) applyMessageDecrypted(msg MessageDecryptedMsg) tea.Cmd {
	chatIdx := m.chatIndexByAddress(msg.AccountIdx, msg.From)
	if chatIdx < 0 {
		return nil
	}
	msgs := m.accounts[msg.AccountIdx].Messages[chatIdx]
	idx := -1
	for i := range msgs {
		if msgs[i].StoreID == msg.StoreID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	msgs[idx].Content = msg.Content
	msgs[idx].DecryptFailed = false
	isOpenChat := msg.AccountIdx == m.currentAccount && chatIdx == m.currentChatIndex()
	var cmd tea.Cmd
	if idx == len(msgs)-1 {
		cmd = m.setChatLastMessage(msg.AccountIdx, chatIdx, MessagePreviewContent(msgs[idx]))
	}
	if isOpenChat {
		m.refreshViewport()
	}
	return cmd
}
