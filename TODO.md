- make sure user can paste to all textinputs
- chat view: add attach file, emoji buttons
- on small screens, only show ctrl+? in help line, and show help fullscreen instead
- account manager: fix adding duplicate accounts
- option to remove all chat history (on server)
- omit options from config, that have default values
- read/unread message count on chat name?
- replace all debugf calls with slog

- ctrl+z to undo last change in message draft. should store all changes in this session, until message is sent, then clear. ctrl+shift+z redo?
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
- implement go-to specific date in chat history
- add OTR as encryption method


# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto
# screen-sharing in calls
# video calls
