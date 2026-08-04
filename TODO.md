- dont print any errors in bubble view with fmt.Print
- esc bind must not quit application
- chat list: toggle on bind/button
- chat view: add attach file, emoji buttons
- ctrl+? for full help, remove help bar
- message status? 'sent' / 'delivered' icons?
- save last chat ID? open it on startup, if exists
- account manager should look like button and show hover indication
- statusline: dont show name if address == name
 account manager: add way to access options from keyboard
- file picker: add fuzzy find on '/'
- show current encryption configured for chat
- add padding for messages names to make messages start from same point?
- fix(save as): must always ask for location. add UI component?
- add loading indicator (spinning line | ) for uploading files
- show files in chat more beautifully: add normalized name for uploaded files and show nice icons based on file type, and also show if its already downloaded
- add config option to
- code syntax highlight. if message has ``` code ``` or ```go some go code ``` or ```rust somecode```
- preview files text contents
- account manager: fix adding duplicate accounts
- desktop notifications on new messages
- store all accounts data in single sqlite db?
- chat view: keyboard navigation on messages doesnt scroll view pane, when cursor gets out of screen view
- chat list: add 'a' keybind and 'add' button at bottom dock to add new contacts
- message deletion: shouldnt delete message locally, instead just show in deleted message state, with option to see its contents
- implement background read-only daemon for desktop notifications?
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
