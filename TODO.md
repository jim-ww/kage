## Bugs
- after opening image/attachment returns to chat in wrong position?
- on nixos rebuild, call hangs or something. or one of clients dies

## Optimization
- optimization speed up cursor message selection in chat and for scrolling
- lags on big window scale

## Refactor
- live tests share the alice/bob devtest accounts, so `go test -tags integration ./...` is flaky two ways: packages run in parallel and fight over the same roster/archive, and MAM paging tests depend on an archive that grows every run. give each test its own throwaway accounts (see newThrowawayAccount) or reset devtest/prosody/data per run

## Features
- auto adjust video/audio call quality on bad internet connection
- add more clear mic/video
- should also encrypt saved attachments?
- archive contact chats? integrate that together with existing 'hide' feature? e.g. hidden == archived
- account logount option (that also cleans up OMEMO devices)
- add image preview for file manager? same way we have avatars
- ux: open chat composer (textbox) of message size (with limits)
- group chats, admin: room settings (name, password, members-only, ...) via the muc#owner config form
- group chats, admin: member list (add/remove members, admins, owners - muc#admin affiliations)
- group chats, admin: kick, ban, grant/revoke voice and moderator
- group chats, admin: moderation - retract others' messages (XEP-0425)
- group chats: change the subject
- group chats: invite someone (XEP-0249 direct invitation, mediated for members-only rooms)
- group chats: tab-complete nicks in the compose box
- group chats: highlight messages that mention our nick
- group chats: per-room notification setting (all / mentions / none)
- group chats: unread count only for mentions/private messages by default, like notifications
- group chats: room avatars (vcard-temp avatar of the room)
- group chats: show who's typing (chat states in rooms, bounded to a few names)
- group chats: browse a server's public rooms (disco#items on its conference service) and search search.jabber.network
- group chats: open xmpp: links clicked elsewhere (x-scheme-handler/xmpp desktop entry, `kage open <uri>` handing it to the TUI)
- group chats: legacy bookmarks (XEP-0048) for servers/clients that only have those
- select & yank text in draft with mouse
- chat view: vim motions for textinput (visual/insert mode emulation)
- preview files text contents (in separate viewport?)
