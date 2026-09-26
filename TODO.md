## Bugs
- add image preview for file manager? same way we have avatars
- fix: rarely, cannot scroll past certain message and load older history
- non-focused state of app, with open chat doesnt send notifications?
- fix: handle pinentry-tty: when accessing gpg, it can ask pinentry-tty password, let it fully take view and let user to type his password
- account manager: fix adding duplicate accounts
- make sure user can paste to all textinputs (e.g. account add JID/password)
- handle extremely large text AND images pasted in message prompt / sent in chat (lags/slow), maybe connect to wayland clipboard paste socket and override bubbletea's ctrl+v for handling images
- improve signaling to other clients on call hang / app quit / etc. so other clients would not hang forever
- if tui relaunched, then calling statusbar not shown?
- check how local/remote messages timestampt are handled?
- contacts: resubscribe action does nothing?

## Optimization
- optimization speed up cursor message selection in chat and for scrolling
- speed up moving cursor on textinput, based on how long is held, OR add ctrl+d/ctrl+u binds there?
- lags on big window scale

## UX
- delete message modal has no mouse support. other modals must be checked too.
- hard to grab left/bottom panel border for resize

## Refactor
- create generic ui components and reuse them across repo

## Features
- calls: option to choose mic + mid call
- ux: open chat composer (textbox) of message size (with limits)
- ui: show own full account address somewhere (in case alias is set)
- search: go-to specific date in chat history (implement as part of search feature?)
- show more contact info on status line Name press
- group chat support + list group chat participants, ability to see their info, start chat with any participant
- in devices show proper OMEMO fingerprint
- select & yank text in draft with mouse
- chat view: vim motions for textinput (visual/insert mode emulation)
- preview files text contents (in separate viewport?)

## Design
- green account alias text out of place?
- design proper tray icon/logo
