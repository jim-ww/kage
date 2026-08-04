- bind keybinds with key codes, not to latin
- make margin above help line (below textinput ) slighly smaller
- chat view: add attach file, emoji buttons
- show encryption information in message info
- message status? 'sent' / 'delivered' icons?
- code syntax highlight. if message has ``` code ``` or ```go some go code ``` or ```rust somecode```
- speed up chat view (messages render)
- on small screens, only show ctrl+? in help line, and show help fullscreen instead
- add OMEMO v1 support?
- account manager: fix adding duplicate accounts
- chat view: keyboard navigation on messages doesnt scroll view pane, when cursor gets out of screen view

- ctrl+z to undo last change in message draft. should store all changes in this session, until message is sent, then clear. ctrl+shift+z redo?
- chat view: cursor can get out of view (not scrolled to)
- invalid highlighting of non-reply messages, partially gray
- account manager should look like button and show hover indication
- save message drafts for each chat (encrypted), on exit/chat switch
- account manager: remove 'accounts:' labewnloaded

- preview files text contents (in separate viewport?)
- chat list: add 'a' keybind and 'add' button at bottom dock to add new contacts
- message deletion: shouldnt delete message locally, instead just show in deleted message state, with option to see its contents
- list chat participants, ability to see info about them, start chat with them
- account manager: status ('● Online', '◐ Away', '○ Offline') and manage it (e.g. offline stops syncing that account)
- setup dev prosody fully with nix?
- search message content in across chats
- import/export messages in json format
- manage(add/remove/see) contacts, also remove contacts permanently?
- optimization speed up cursor message selection in chat
- optimization: load messages history per chat, in batches as needed, instead of fetching full list for all at startup
- chat view: vim motions for textinput (visual/insert mode emulation)


# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto
# screen-sharing in calls
# video calls
