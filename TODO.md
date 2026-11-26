## Bugs
- video stream worsens call quality (adds lag)
- problems with Japanese input in inputbox, e.g. (ﾟДﾟ)ｳ
- on nixos rebuild, call hangs or something. or one of clients dies
- call widget is ugly and unhelpful. some keybindings do not work
- fix: handle pinentry-tty: when accessing gpg, it can ask pinentry-tty password, let it fully take view and let user to type his password
- handle extremely large text AND images pasted in message prompt / sent in chat (lags/slow), maybe connect to wayland clipboard paste socket and override bubbletea's ctrl+v for handling images
- improve signaling to other clients on call hang / app quit / etc. so other clients would not hang forever
- if tui relaunched, then calling statusbar not shown?
- adding account, or changing config file values imperatively from app, when config is managed by nix

## Optimization
- optimization speed up cursor message selection in chat and for scrolling
- speed up moving cursor on textinput, based on how long is held, OR add ctrl+d/ctrl+u binds there?
- lags on big window scale

## UX
- add scrollbar for messages list, and perhaps, for chat list too

## Refactor
- ui/zone replaced bubblezone (linear Scan, synchronous zones): maybe publish it as a bubblezone fork / upstream PR
- live tests share the alice/bob devtest accounts, so `go test -tags integration ./...` is flaky two ways: packages run in parallel and fight over the same roster/archive, and MAM paging tests depend on an archive that grows every run. give each test its own throwaway accounts (see newThrowawayAccount) or reset devtest/prosody/data per run
- create generic ui components and reuse them across repo:
  many ui places of app have its own one-off modals, instead of sharing single, well defined and dynamic modal component. and beside modal rendering itself, we also need proper 'list' component. not using random bullshit in every place.

## Features
- auto adjust video/audio call quality on bad internet connection
- add more clear mic/video
- media previews: for images, videos, links
- should also encrypt saved attachments?
- block/unblock contacts
- archive contact chats? integrate that together with existing 'hide' feature? e.g. hidden == archived
- account logount option (that also cleans up OMEMO devices)
- calls: option to choose mic + mid call
- add image preview for file manager? same way we have avatars
- ux: open chat composer (textbox) of message size (with limits)
- search: go-to specific date in chat history (implement as part of search feature?)
- group chat support + list group chat participants, ability to see their info, start chat with any participant
- select & yank text in draft with mouse
- chat view: vim motions for textinput (visual/insert mode emulation)
- preview files text contents (in separate viewport?)

## Design
- green account alias text out of place?
- design proper tray icon/logo
