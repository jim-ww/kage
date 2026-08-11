- add command controls over daemon (e.g. close)
- replies to attachments not properly seen on other clients? (e.g. Dino)
- bind ctrl+~ to quickly toggle open bigger message textinput
- quick way to erase message while copying it (draft)
- ui freezing in some cases, CPU usage goes up. on paste, or after editing certain amount of text?
- textinput long message reaching limit, cannot break on new line
- uploading your own files must not download them from server (must be downloaded)
- send notifications, even if chat is focused, but window is not (e.g. on separate screen). add timer on last input?
- ctrl+e should automatically select last message for edit (as no other can be edited)
- config: add attachmentsDir cfg
- add proper login/register modal
- reattaching(mapping) messages from json exports to separate accounts
- resubscribe action does nothing?
- filepicker: make binds as buttons at bottom clickable
- speed up moving cursor on textinput, based on how long is held
- speed up filepicker: add cursor pagination (e.g. limited max items displayed)
- refactor: create generic ui components and reuse them across repo
- esc should not de-focus chat when in editing state (only cancel editing)
- write e2e tests for all features, on local prosody instance, ran with 'integration' build tag
- implement notes? e.g. writing yourself
- improve signaling to other clients on call hang / app quit / etc. so other clients would not hang forever
- add KAGE_DEBUG variable instead/in addition of/to flag?
- sometimes status shows as offline, when other client is online. could be either status send/receive problem
- on error during call, should hang up the call (send event to other peer)
- log in single file
- if tui relaunched, then calling statusbar not shown
- "kage's background service disconnected; please restart kage" is always printed on quit
- messages sent from same account on other devices do not appear in chat
- send failed, no
- add option to move carret in message input with mouse (e.g. press on specific word character)
- change drag & drop behavior: add dropped file as attachment, instead of sending right away. ensure multi-file drop is handled too
- show own full account address somewhere (in case alias is set)
- move change password(encryption) functionality to RMB click on accounts tab modal
- simplify emoji selection

- when images added with ctrl+p, they added and sent as single message, not multiple?
- handle extremely large text files pasted in message prompt / sent in chat (lags/slow)
- add keyboard navigation to modals (and ways to open them)
- check how local/remote messages timestampt are handled
- in devices show proper OMEMO fingerprint
- ^shift+U bind doesnt work (omemo devices)
- show more contact info on status line Name press
- make sure user can paste to all textinputs (e.g. account add JID/password)
- account manager: fix adding duplicate accounts
- option to remove specific chat history (on server)
- implement backups, compatible with conversations
- omit options from config, that have default values
- search message content in across chats

- refactor: move components to ui/ package
- optimization speed up cursor message selection in chat and for scrolling
- preview files text contents (in separate viewport?)
- implement group chat support
- list group chat participants, ability to see info about them, start chat with them
- chat view: vim motions for textinput (visual/insert mode emulation)
- implement go-to specific date in chat history
- add OTR as encryption method


# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto
# screen-sharing in calls
# video calls
