# ui
- fix: long messages in textinput show empty notification
# code:
- xmpp lib: mellium.im/xmpp
# e2ee: OpenPGP https://xmpp.org/extensions/xep-0374.html
# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto

# storage
- sqlite, modernc.org/sqlite, sqlc

### Features to implement
- list chat participants, ability to see info about them, start chat with them
- account management: status ('● Online', '◐ Away', '○ Offline') and manage it
- message status? 'sent' / 'delivered'?
- send/receive messages: text, images, files
- edit messages? mark as deleted?(hide)
- copy message text
- reply to messages
- search message content in single/multiple chats
- import/export messages
- switch between accounts
- add/remove contacts
- delete chat


#### reference projects
https://codeberg.org/mellium/communique-tui
