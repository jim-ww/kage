 [message could not be decrypted:
                   │                ratchet decrypt: no message key is
                   │                cached for this message]

- messages sent from same account on other devices do not appear in chat
- check how local/remote messages timestampt are handled
> » [09:10 🔒 ✓] [message could not be decrypted:
                   │                  establish session: consume one-time
                   │                  prekey 23: consume prekey 23: sql: no
                   │                  rows in result set]
- doesnt encrypt for other own devices?
- ctrl+w replace with ctrl+s, and ctrl+shift+s as save as (selecting path)
- in devices show proper OMEMO fingerprint
- ^shift+U bind doesnt work (omemo devices)
- add backspace binding in help line
- cant open/send files? images (omemo?)
- add download % when loading file
- add keyboard navigation to modals (and ways to open them)
- show more contact info on status line Name press
- make sure user can paste to all textinputs (e.g. account add JID/password)
- on small screens, only show ctrl+? in help line, and show help fullscreen instead
- account manager: fix adding duplicate accounts
- option to remove specific chat history (on server)
- implement backups, compatible with conversations
- omit options from config, that have default values

- ctrl+z to undo last change in message draft. should store all changes in this session, until message is sent, then clear. ctrl+shift+z redo?
- account manager should look like button and show hover indication
- save message drafts for each chat (encrypted, if encryption enabled), on exit/chat switch

- preview files text contents (in separate viewport?)
- implement group chat support
- list group chat participants, ability to see info about them, start chat with them
- search message content in across chats
- optimization speed up cursor message selection in chat and for scrolling
- optimization: load messages history per chat, in batches as needed, instead of fetching full list for all at startup
- chat view: vim motions for textinput (visual/insert mode emulation)
- implement go-to specific date in chat history
- add OTR as encryption method


# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto
# screen-sharing in calls
# video calls
