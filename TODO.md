- nix flake: add desktop entry
- do not write duplicate image files on image view
- dont cycle through encryption
- show encryption
- panic on minimized window
- ctrl+? for full help, remove help bar
- show files in chat more beautifully
- add padding for messages names to make messages start from same point
- move statuline to center
- add loading indicator for uploading files
- add normalized name for uploaded files and show nice icons based on file type
- code syntax highlight
- preview files
- fix save as dialog
- account manager: fix adding duplicate accounts
- desktop notifications on new messages
- store all accounts data in single sqlite db?
- chat view: keyboard navigation on messages doesnt scroll view pane, when cursor gets out of screen view
- chat list: add 'a' keybind and 'add' button at bottom dock to add new contacts
- message deletion: shouldnt delete message locally, instead just show in deleted message state, with option to see its contents
- implement background read-only daemon for desktop notifications?
- list chat participants, ability to see info about them, start chat with them
- account manager: status ('● Online', '◐ Away', '○ Offline') and manage it (e.g. offline stops syncing that account)
- message status? 'sent' / 'delivered'?
- send/receive messages: text, images, files
- search message content in single/multiple chats
- import/export messages
- manage(add/remove/see) contacts


# e2ee: OpenPGP https://xmpp.org/extensions/xep-0374.html
# calls: Jingle (XEP-0166) https://xmpp.org/extensions/xep-0166.html
- sound: https://github.com/ebitengine/oto
