## Bugs
- after opening image/attachment returns to chat in wrong position?
- video stream worsens call quality (adds lag)
- on nixos rebuild, call hangs or something. or one of clients dies
- call widget is ugly and unhelpful
- handle extremely large images pasted in message prompt, maybe connect to wayland clipboard paste socket and override bubbletea's ctrl+v for handling images

## Optimization
- optimization speed up cursor message selection in chat and for scrolling
- lags on big window scale

## UX

## Refactor
- ui/zone replaced bubblezone (linear Scan, synchronous zones): maybe publish it as a bubblezone fork / upstream PR
- live tests share the alice/bob devtest accounts, so `go test -tags integration ./...` is flaky two ways: packages run in parallel and fight over the same roster/archive, and MAM paging tests depend on an archive that grows every run. give each test its own throwaway accounts (see newThrowawayAccount) or reset devtest/prosody/data per run
- create generic ui components and reuse them across repo:
  many ui places of app have its own one-off modals, instead of sharing single, well defined and dynamic modal component. and beside modal rendering itself, we also need proper 'list' component. not using random bullshit in every place.

## Features
- auto adjust video/audio call quality on bad internet connection
- add more clear mic/video
- should also encrypt saved attachments?
- archive contact chats? integrate that together with existing 'hide' feature? e.g. hidden == archived
- account logount option (that also cleans up OMEMO devices)
- add image preview for file manager? same way we have avatars
- ux: open chat composer (textbox) of message size (with limits)
- group chats: OMEMO in members-only non-anonymous rooms; room MAM (XEP-0313 against the room) instead of join history; occupant-id (XEP-0421) to tell senders apart across nick changes; room config/moderation (kick, ban, roles)
- select & yank text in draft with mouse
- chat view: vim motions for textinput (visual/insert mode emulation)
- preview files text contents (in separate viewport?)

## Design
- green account alias text out of place?
- design proper tray icon/logo
