- make margin above help line (below textinput ) slighly smaller
- chat view: add attach file, emoji buttons
- only launch notification daemon if its enabled in config, enabled by default
- show encryption information in message info
- message status? 'sent' / 'delivered' icons?
- bind keybinds with key codes, not to latin
- code syntax highlight. if message has ``` code ``` or ```go some go code ``` or ```rust somecode```
- add OMEMO v1 support?

- ctrl+z to undo last change in message draft. should store all changes in this session, until message is sent, then clear. ctrl+shift+z redo?
- chat view: cursor can get out of view (not scrolled to)
- invalid highlighting of non-reply messages, partially gray
- on small screens, only show ctrl+? in help line, and show help fullscreen instead
- account manager should look like button and show hover indication
- save message drafts for each chat (encrypted), on exit/chat switch
- account manager: remove 'accounts:' labewnloaded
- add config ui toggle options (e.g. show/hide names in chat, show glyphs in chat, chat time/date layout + only show time (if its today)(only with default time layout))

- preview files text contents
- account manager: fix adding duplicate accounts
- store all accounts data in single sqlite db?
- chat view: keyboard navigation on messages doesnt scroll view pane, when cursor gets out of screen view
- chat list: add 'a' keybind and 'add' button at bottom dock to add new contacts
- message deletion: shouldnt delete message locally, instead just show in deleted message state, with option to see its contents
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
- chat view: vim motions for textinput (visual/insert mode emulation)


# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto
