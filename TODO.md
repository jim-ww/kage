- chat view: add attach file, emoji buttons
- message status? 'sent' / 'delivered' icons?
- save last chat ID? open it on startup, if exists
- improve chat view text input. shift+enter should break to new line, and also show multiline properly
- account manager should look like button and show hover indication
- account manager: remove 'accounts:' labewnloaded
- add config ui toggle options (e.g. show/hide names in chat, show glyphs in chat, chat date layout)
- code syntax highlight. if message has ``` code ``` or ```go some go code ``` or ```rust somecode```
- preview files text contents
- account manager: fix adding duplicate accounts
- store all accounts data in single sqlite db?
- chat view: keyboard navigation on messages doesnt scroll view pane, when cursor gets out of screen view
- chat list: add 'a' keybind and 'add' button at bottom dock to add new contacts
- message deletion: shouldnt delete message locally, instead just show in deleted message state, with option to see its contents
- implement background read-only daemon for desktop notifications on new messages. it should be started on app launch, unless such process already exists. check with flock lib? also, it shouldn't be closed on app exit
- list chat participants, ability to see info about them, start chat with them
- account manager: status ('● Online', '◐ Away', '○ Offline') and manage it (e.g. offline stops syncing that account)
- refactor main.go, xmpp/client.go, others
- setup dev prosody fully with nix
- send/receive messages: text, images, files
- search message content in single/multiple chats
- import/export messages in json format
- manage(add/remove/see) contacts, also remove contacts permanently?
- speed up cursor message selection in chat
- optimisation: load messages history per chat, in batches as needed, instead of fetching full list for all at startup


# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto
