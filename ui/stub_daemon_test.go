package ui

import tea "charm.land/bubbletea/v2"

// stubDaemon is a Daemon doing nothing: test fakes embed it and override
// only the methods their test is about.
type stubDaemon struct{}

var _ Daemon = stubDaemon{}

func (stubDaemon) Send(int, string, string, SendOptions) (string, error)       { return "", nil }
func (stubDaemon) MarkRetracted(int, string, string) error                     { return nil }
func (stubDaemon) SetTyping(int, string, bool) error                           { return nil }
func (stubDaemon) DeleteQueued(int, string) error                              { return nil }
func (stubDaemon) PurgeMessage(int, string, string) error                      { return nil }
func (stubDaemon) SendFile(int, string, string, SendOptions) tea.Msg           { return nil }
func (stubDaemon) UploadFile(int, string, string, string, SendOptions) tea.Msg { return nil }
func (stubDaemon) CancelUpload(string)                                         {}
func (stubDaemon) LoadHistoryWindow(int, string, *HistoryAnchor) tea.Cmd       { return nil }
func (stubDaemon) SearchHistory(int, string, string) tea.Cmd                   { return nil }
func (stubDaemon) RenameContact(int, string, string) error                     { return nil }
func (stubDaemon) AddContact(int, string) tea.Msg                              { return nil }
func (stubDaemon) RemoveContact(int, string) tea.Msg                           { return nil }
func (stubDaemon) ResubscribeContact(int, string) tea.Msg                      { return nil }
func (stubDaemon) SetChatFlag(string, string, ChatSetting, bool) error         { return nil }
func (stubDaemon) SetChatEncryption(int, string, string) error                 { return nil }
func (stubDaemon) ResetChatUnread(string, string) error                        { return nil }
func (stubDaemon) ChatUnreadCounts(string) (map[string]int, error)             { return nil, nil }
func (stubDaemon) SaveDraft(string, string, string) error                      { return nil }
func (stubDaemon) AddAccount(string, string, string, bool) tea.Msg             { return nil }
func (stubDaemon) RemoveAccount(int) tea.Msg                                   { return nil }
func (stubDaemon) SetAccountStatus(int, Presence) tea.Msg                      { return nil }
func (stubDaemon) SetOwnAvatar(int, string) error                              { return nil }
func (stubDaemon) RemoveOwnAvatar(int) error                                   { return nil }
func (stubDaemon) FetchOwnDeviceList(int) tea.Msg                              { return nil }
func (stubDaemon) PurgeOwnDeviceList(int, []OmemoDevice) tea.Msg               { return nil }
func (stubDaemon) ChangeStoragePassword(string) error                          { return nil }
func (stubDaemon) SaveState(StateChange) error                                 { return nil }
func (stubDaemon) SetFocusState(string, string, bool) error                    { return nil }
func (stubDaemon) StartCall(int, string) tea.Msg                               { return nil }
func (stubDaemon) StartVideoCall(int, string, bool) tea.Msg                    { return nil }
func (stubDaemon) AnswerCall(int) tea.Msg                                      { return nil }
func (stubDaemon) HangupCall(int) tea.Msg                                      { return nil }
func (stubDaemon) RejectCall(int) tea.Msg                                      { return nil }
func (stubDaemon) MuteCall(int, bool) tea.Msg                                  { return nil }
func (stubDaemon) ScreenShare(int, bool, bool) tea.Msg                         { return nil }
func (stubDaemon) ReopenVideo(int) tea.Msg                                     { return nil }
func (stubDaemon) SetDesktopAudio(bool) tea.Msg                                { return nil }
func (stubDaemon) SetDesktopAudioVolume(int) tea.Msg                           { return nil }
func (stubDaemon) ListAudioDevices() tea.Msg                                   { return nil }
func (stubDaemon) SetAudioDevice(bool, string) tea.Msg                         { return nil }
