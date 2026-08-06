- add quick way to clear draft message
- client-server architecture? so any number of clients would talk to daemon? instead of making duplicate connections
- or only allow one instance at a time?
- daemon tray: open kage client on LMB click
- handle extremely large text files pasted in message prompt / sent in chat (lags/slow)
- make daemon more lightweight
- filepicker: add mouse support
- show decrypted message content in notifications
- ctrl+f add sort for files creation date DESC
- enter adds some ' ' after newline
- messages sent from same account on other devices do not appear in chat
- check how local/remote messages timestampt are handled
- in devices show proper OMEMO fingerprint
- ^shift+U bind doesnt work (omemo devices)
- add backspace binding in help line
- checking notification file lock is not enough: if notification daemon was killed, it stays on disk
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
