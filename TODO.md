- avatar modal: add mouse support (use same modal preset + ensure good UX for file selection/preview/reselection)
- green account alias out of place?
- video calls: option to stop streaming video
- fix: rarely, cannot scroll past certain message and load older history
- design proper tray icon/logo
- increase message size, before it becomes collapsible
- open chat composer (textbox) of message size (with limits)
- pasted text to chat composer(inputbox) isnt shown in chat as it is in composer. e.g. multiline insert lines get bad padding from left
- ellipsis must not wrap on second line, if replied-to message text is overflowing (e.g. does not account for ellipsis char length itself)
- option to choose mic + mid call
- fix: handle pinentry-tty: when accessing gpg, it can ask pinentry-tty password, let it fully take view and let user to type his password
- ui(config): allow setting single-line display of messages in chat, off by default (e.g. whole status line + control buttons on same line as message, after message text)
- add set avatar option
- connect to wayland clipboard paste socket and override bubbletea's ctrl+v for handling images
- non-focused state of app, with open chat doesnt send notifications
- some modals are overflowing out of window (e.g. localstorage password change)
- add keyboard navigation to modals (and ways to open them)

- select & yank text in draft with mouse

- improve signaling to other clients on call hang / app quit / etc. so other clients would not hang forever
- account manager: fix adding duplicate accounts
- make sure user can paste to all textinputs (e.g. account add JID/password)

- change picture attachment icon ? from 🖼
- resubscribe action does nothing?
- speed up moving cursor on textinput, based on how long is held, OR add ctrl+d/ctrl+u binds there?
- refactor: create generic ui components and reuse them across repo
- implement notes? e.g. writing yourself
- on error during call, should hang up the call (send event to other peer)
- if tui relaunched, then calling statusbar not shown
- show own full account address somewhere (in case alias is set)
- move change password(encryption) functionality to RMB click on accounts tab modal
- simplify emoji selection

- handle extremely large text files pasted in message prompt / sent in chat (lags/slow)
- check how local/remote messages timestampt are handled
- in devices show proper OMEMO fingerprint
- show more contact info on status line Name press
- option to remove specific chat history (on server)
- implement backups, compatible with conversations

- refactor: move components to ui/ package
- optimization speed up cursor message selection in chat and for scrolling
- preview files text contents (in separate viewport?)
- implement group chat support
- list group chat participants, ability to see info about them, start chat with them
- chat view: vim motions for textinput (visual/insert mode emulation)
- implement go-to specific date in chat history
- add OTR as encryption method
