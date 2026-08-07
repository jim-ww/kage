- messages sent from same account on other devices do not appear in chat
- send failed, no
- change drag & drop behavior: add dropped file as attachment, instead of sending right away. ensure multi-file drop is handled too

- make daemon more lightweight
- handle extremely large text files pasted in message prompt / sent in chat (lags/slow)
- filepicker: add mouse support
- ctrl+f add sort for files creation date DESC
- check how local/remote messages timestampt are handled
- in devices show proper OMEMO fingerprint
- ^shift+U bind doesnt work (omemo devices)
- add keyboard navigation to modals (and ways to open them)
- show more contact info on status line Name press
- make sure user can paste to all textinputs (e.g. account add JID/password)
- account manager: fix adding duplicate accounts
- option to remove specific chat history (on server)
- implement backups, compatible with conversations
- omit options from config, that have default values

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
