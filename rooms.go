package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jim-ww/kage/daemon"
	"github.com/jim-ww/kage/ipc"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/ui"
	"github.com/jim-ww/kage/xmpp"
	omemolib "github.com/jim-ww/omemo-go"
	"mellium.im/xmpp/jid"
)

// roomState is one room (XEP-0045) the account is in, or trying to be.
type roomState struct {
	name, nick, password, subject string

	// bookmarked is set once the room was seen in our bookmarks
	// (XEP-0402): its bookmark gone, the room was left on another client.
	bookmarked bool

	// info is what the room said of itself, asked before each join.
	info xmpp.RoomInfo
	// occupantID is ours in the room (XEP-0421), "" if it stamps none.
	occupantID string

	joined    bool
	joinErr   string
	occupants map[string]ui.Occupant

	// privates are the nicks we have a private chat with (XEP-0045 §7.5).
	privates map[string]bool

	// members are the room's owners, admins and members by bare JID, and
	// realJIDs every nick's bare JID seen this session, its occupant gone
	// or not: who an encrypted message is for, and from.
	members  map[string]bool
	realJIDs map[string]string

	// waiters are JoinRoom calls waiting for the room's answer.
	waiters []chan error

	// pendingNick is the nick SetRoomNick asked the room for, nickWaiter
	// waiting for its answer.
	pendingNick string
	nickWaiter  chan error

	// subjectID is the message SetRoomSubject sent, subjectWaiter waiting
	// for the room's answer: the new subject, or the message bounced.
	subjectID     string
	subjectWaiter chan error
}

// roomSet is an account's rooms by bare JID. Their chats sit beside the
// roster's, keyed the same way.
type roomSet struct {
	mu     sync.Mutex
	rooms  map[string]*roomState
	timers map[string]*time.Timer
}

// roomBroadcastDelay gathers a room's state changes into one broadcast:
// joining a busy room brings a presence per occupant.
const roomBroadcastDelay = 150 * time.Millisecond

// roomJoinTimeout is how long JoinRoom waits for the room to answer.
const roomJoinTimeout = 20 * time.Second

// roomInfoTimeout bounds asking a room what it is before joining it.
const roomInfoTimeout = 10 * time.Second

// trustsOccupantID reports whether st's room stamps occupant-ids, so one on
// a stanza from it is the room's and not something its sender made up.
func (st roomState) trustsOccupantID(id string) bool {
	return st.info.OccupantID && id != ""
}

// ownInRoom reports whether a stanza in st's room from nick with
// occupantID is from us: by occupant-id where the room stamps them (it
// outlives nick changes), else by nick.
func (st roomState) ownInRoom(nick, occupantID string) bool {
	if st.trustsOccupantID(occupantID) && st.occupantID != "" {
		return occupantID == st.occupantID
	}
	return nick != "" && nick == st.nick
}

// isRoom reports whether addr is one of the account's rooms.
func (s *accountSession) isRoom(addr string) bool {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	_, ok := s.rooms.rooms[addr]
	return ok
}

// hasChat reports whether addr has a chat row: a roster entry, a room or
// a private chat in one.
func (s *accountSession) hasChat(addr string) bool {
	return s.inRoster(addr) || s.isRoom(addr) || s.isRoomPrivate(addr)
}

// isRoomPrivate reports whether addr, an occupant JID, has a private chat.
func (s *accountSession) isRoomPrivate(addr string) bool {
	nick := resourcePart(addr)
	if nick == "" {
		return false
	}
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	st, ok := s.rooms.rooms[bareJID(addr)]
	return ok && st.privates[nick]
}

// isGroup reports whether addr's chat is a room's or a private one in a
// room: plaintext, no calls, no roster.
func (s *accountSession) isGroup(addr string) bool {
	return s.isRoom(addr) || s.isRoomPrivate(addr)
}

// room is a copy of addr's state, without its waiters.
func (s *accountSession) room(addr string) (roomState, bool) {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	st, ok := s.rooms.rooms[addr]
	if !ok {
		return roomState{}, false
	}
	cp := *st
	cp.occupants = make(map[string]ui.Occupant, len(st.occupants))
	for k, v := range st.occupants {
		cp.occupants[k] = v
	}
	cp.privates = make(map[string]bool, len(st.privates))
	for k, v := range st.privates {
		cp.privates[k] = v
	}
	cp.members = maps.Clone(st.members)
	cp.realJIDs = maps.Clone(st.realJIDs)
	cp.waiters = nil
	return cp, true
}

// updateRoom runs fn on addr's state, reporting whether it's a room of ours.
func (s *accountSession) updateRoom(addr string, fn func(st *roomState)) bool {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	st, ok := s.rooms.rooms[addr]
	if ok {
		fn(st)
	}
	return ok
}

// roomAddrs is every room's JID.
func (s *accountSession) roomAddrs() []string {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	addrs := make([]string, 0, len(s.rooms.rooms))
	for addr := range s.rooms.rooms {
		addrs = append(addrs, addr)
	}
	slices.Sort(addrs)
	return addrs
}

// putRoom adds addr to the rooms in memory, or updates what it's joined as.
func (s *accountSession) putRoom(addr, name, nick, password string, bookmarked bool) {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	if s.rooms.rooms == nil {
		s.rooms.rooms = map[string]*roomState{}
	}
	st, ok := s.rooms.rooms[addr]
	if !ok {
		st = &roomState{occupants: map[string]ui.Occupant{}, privates: map[string]bool{}, members: map[string]bool{}, realJIDs: map[string]string{}}
		s.rooms.rooms[addr] = st
	}
	st.name, st.nick, st.password, st.bookmarked = cmp.Or(name, st.name), nick, password, bookmarked
}

// persistRoom stores addr's room as it is in memory.
func (s *accountSession) persistRoom(ctx context.Context, addr string) {
	st, ok := s.room(addr)
	if !ok {
		return
	}
	if err := s.db.UpsertRoom(ctx, storage.UpsertRoomParams{
		AccountJid: s.account.JID, Jid: addr, Name: st.name, Nick: st.nick, Password: st.password, Bookmarked: st.bookmarked,
	}); err != nil {
		slog.Warn("persisting room", "jid", s.account.JID, "room", addr, "err", err)
	}
}

// loadRooms puts the stored rooms in memory, returning them.
func (s *accountSession) loadRooms(ctx context.Context) []storage.ListRoomsRow {
	rows, err := s.db.ListRooms(ctx, s.account.JID)
	if err != nil {
		slog.Warn("loading rooms", "jid", s.account.JID, "err", err)
		return nil
	}
	for _, r := range rows {
		s.putRoom(r.Jid, r.Name, r.Nick, r.Password, r.Bookmarked)
		s.updateRoom(r.Jid, func(st *roomState) { st.subject = r.Subject })
	}
	privates, err := s.db.ListRoomPrivates(ctx, s.account.JID)
	if err != nil {
		slog.Warn("loading private chats in rooms", "jid", s.account.JID, "err", err)
	}
	for _, p := range privates {
		s.updateRoom(p.Room, func(st *roomState) { st.privates[p.Nick] = true })
	}
	return rows
}

// privateAddrs is every private chat's occupant JID, by room.
func (s *accountSession) privateAddrs() []string {
	var out []string
	for _, room := range s.roomAddrs() {
		st, _ := s.room(room)
		for nick := range st.privates {
			out = append(out, room+"/"+nick)
		}
	}
	slices.Sort(out)
	return out
}

// newPrivateChat is the private chat with occupant addr, with its latest
// page of history.
func (s *accountSession) newPrivateChat(ctx context.Context, addr string) (chat ui.Chat, hist []ui.Message, hasMore bool) {
	room, nick := bareJID(addr), resourcePart(addr)
	st, _ := s.room(room)
	chat, hist, hasMore = s.newChat(ctx, addr, nick+" · "+cmp.Or(st.name, room), nil)
	chat.PrivateRoom = room
	chat.EncryptionMode = "none"
	if o, in := st.occupants[nick]; in {
		chat.Presence = o.Presence
	}
	return chat, hist, hasMore
}

// ensurePrivateChat gives occupant addr a private chat, broadcast, if it
// has none yet.
func (s *accountSession) ensurePrivateChat(ctx context.Context, srv *ipc.Server, accountIdx int, addr string) {
	room, nick := bareJID(addr), resourcePart(addr)
	added := false
	s.updateRoom(room, func(st *roomState) {
		if !st.privates[nick] {
			st.privates[nick], added = true, true
		}
	})
	if !added {
		return
	}
	if err := s.db.UpsertRoomPrivate(ctx, storage.UpsertRoomPrivateParams{AccountJid: s.account.JID, Room: room, Nick: nick}); err != nil {
		slog.Warn("persisting private chat", "with", addr, "err", err)
	}
	chat, hist, _ := s.newPrivateChat(ctx, addr)
	broadcast(srv, ui.ChatAddedMsg{AccountIdx: accountIdx, Chat: chat, Messages: hist})
}

// newRoomChat is addr's chat row, with its latest page of history.
func (s *accountSession) newRoomChat(ctx context.Context, addr string) (chat ui.Chat, hist []ui.Message, hasMore bool) {
	st, _ := s.room(addr)
	chat, hist, hasMore = s.newChat(ctx, addr, st.name, nil)
	applyRoomState(&chat, st)
	return chat, hist, hasMore
}

// applyRoomState puts st on chat.
func applyRoomState(chat *ui.Chat, st roomState) {
	chat.Room = true
	chat.Nick, chat.Subject, chat.Joined, chat.JoinError = st.nick, st.subject, st.joined, st.joinErr
	chat.Occupants = sortedOccupants(st.occupants)
	chat.Private = st.info.Private()
}

func sortedOccupants(m map[string]ui.Occupant) []ui.Occupant {
	out := make([]ui.Occupant, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b ui.Occupant) int {
		return cmp.Compare(strings.ToLower(a.Nick), strings.ToLower(b.Nick))
	})
	return out
}

// broadcastRoomChat tells attached clients about addr's chat (an upsert).
func (s *accountSession) broadcastRoomChat(ctx context.Context, srv *ipc.Server, accountIdx int, addr string) {
	chat, hist, _ := s.newRoomChat(ctx, addr)
	if unread, err := s.db.GetChatUnread(ctx, storage.GetChatUnreadParams{
		AccountJid: s.account.JID, RosterJid: addr,
	}); err == nil {
		chat.Unread = int(unread)
	}
	broadcast(srv, ui.ChatAddedMsg{AccountIdx: accountIdx, Chat: chat, Messages: hist})
}

// broadcastRoomState broadcasts addr's state soon, once for a burst of
// changes.
func (s *accountSession) broadcastRoomState(srv *ipc.Server, accountIdx int, addr string) {
	s.rooms.mu.Lock()
	defer s.rooms.mu.Unlock()
	if s.rooms.timers == nil {
		s.rooms.timers = map[string]*time.Timer{}
	}
	if _, pending := s.rooms.timers[addr]; pending {
		return
	}
	s.rooms.timers[addr] = time.AfterFunc(roomBroadcastDelay, func() {
		s.rooms.mu.Lock()
		delete(s.rooms.timers, addr)
		s.rooms.mu.Unlock()
		st, ok := s.room(addr)
		if !ok {
			return
		}
		broadcast(srv, ui.RoomStateMsg{
			AccountIdx: accountIdx,
			Room:       addr,
			Nick:       st.nick,
			Subject:    st.subject,
			Joined:     st.joined,
			JoinError:  st.joinErr,
			Occupants:  sortedOccupants(st.occupants),
			Private:    st.info.Private(),
			// What the room became decides what's sent to it.
			EncryptionMode: s.roomEncryptionMode(context.Background(), addr),
		})
	})
}

// defaultNick is the nick a room is joined with when none was given: our
// JID's localpart.
func defaultNick(accountJID string) string {
	if at := strings.IndexByte(accountJID, '@'); at > 0 {
		return accountJID[:at]
	}
	return accountJID
}

// enterRooms joins every room on a new stream, then catches up with the
// bookmarks other clients changed meanwhile.
func enterRooms(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession) {
	client, err := s.liveClient()
	if err != nil {
		return
	}
	for _, addr := range s.roomAddrs() {
		s.sendJoin(ctx, srv, accountIdx, client, addr)
	}
	s.syncBookmarks(ctx, srv, accountIdx, client)
}

// sendJoin asks addr what it is, then to enter it, for the history since
// its newest stored message.
func (s *accountSession) sendJoin(ctx context.Context, srv *ipc.Server, accountIdx int, client *xmpp.Client, addr string) {
	if _, ok := s.room(addr); !ok {
		return
	}
	infoCtx, cancel := context.WithTimeout(ctx, roomInfoTimeout)
	info, err := client.RoomInfo(infoCtx, addr)
	cancel()
	if err != nil {
		// A room our join is about to create has nothing to say yet.
		slog.Debug("asking room what it is", "jid", s.account.JID, "room", addr, "err", err)
	}
	s.updateRoom(addr, func(st *roomState) { st.info = info })
	st, ok := s.room(addr)
	if !ok {
		return
	}
	var since time.Time
	if latest, err := s.db.GetLatestMessageDelay(ctx, storage.GetLatestMessageDelayParams{
		AccountJid: s.account.JID, RosterJid: nullString(addr),
	}); err == nil && latest > 0 {
		since = time.Unix(latest, 0)
	}
	s.updateRoom(addr, func(st *roomState) {
		st.joined, st.joinErr = false, ""
		clear(st.occupants)
	})
	slog.Debug("joining room", "jid", s.account.JID, "room", addr, "nick", st.nick, "since", since)
	// A room with an archive is read from it once in (see syncRoomArchive),
	// all of what was missed rather than a few of the latest.
	opts := xmpp.JoinOptions{Password: st.password, Show: presenceShow(accountStatus(s.account.Status)), Since: since, NoHistory: st.info.MAM}
	if err := client.JoinRoom(ctx, addr, st.nick, opts); err != nil {
		slog.Warn("joining room", "jid", s.account.JID, "room", addr, "err", err)
		s.updateRoom(addr, func(st *roomState) { st.joinErr = err.Error() })
	}
	s.broadcastRoomState(srv, accountIdx, addr)
}

// roomsOffline marks every room left, the stream having ended.
func (s *accountSession) roomsOffline(srv *ipc.Server, accountIdx int) {
	for _, addr := range s.roomAddrs() {
		var waiters []chan error
		s.updateRoom(addr, func(st *roomState) {
			st.joined, st.joinErr = false, ""
			clear(st.occupants)
			waiters, st.waiters = st.waiters, nil
		})
		for _, w := range waiters {
			w <- errors.New("disconnected")
		}
		s.broadcastRoomState(srv, accountIdx, addr)
	}
}

// syncBookmarks makes the rooms match our bookmarks (XEP-0402): joins those
// another client added, leaves those it removed.
func (s *accountSession) syncBookmarks(ctx context.Context, srv *ipc.Server, accountIdx int, client *xmpp.Client) {
	bookmarks, err := client.Bookmarks(ctx)
	if err != nil {
		slog.Warn("fetching bookmarks", "jid", s.account.JID, "err", err)
		return
	}
	listed := map[string]bool{}
	for _, b := range bookmarks {
		st, known := s.room(b.Room)
		if !b.Autojoin {
			continue
		}
		listed[b.Room] = true
		if known {
			if !st.bookmarked || (b.Name != "" && b.Name != st.name) {
				s.putRoom(b.Room, b.Name, st.nick, st.password, true)
				s.persistRoom(ctx, b.Room)
				if b.Name != st.name {
					s.broadcastRoomChat(ctx, srv, accountIdx, b.Room)
				}
			}
			continue
		}
		slog.Debug("joining a room bookmarked elsewhere", "jid", s.account.JID, "room", b.Room)
		s.putRoom(b.Room, b.Name, cmp.Or(b.Nick, defaultNick(s.account.JID)), b.Password, true)
		s.persistRoom(ctx, b.Room)
		s.broadcastRoomChat(ctx, srv, accountIdx, b.Room)
		s.sendJoin(ctx, srv, accountIdx, client, b.Room)
	}
	for _, addr := range s.roomAddrs() {
		if st, ok := s.room(addr); ok && st.bookmarked && !listed[addr] {
			slog.Debug("leaving a room unbookmarked elsewhere", "jid", s.account.JID, "room", addr)
			if err := client.LeaveRoom(ctx, addr, st.nick); err != nil {
				slog.Warn("leaving room", "room", addr, "err", err)
			}
			s.forgetRoom(ctx, srv, accountIdx, addr)
		}
	}
}

// forgetRoom drops addr's room and chat; its history stays.
func (s *accountSession) forgetRoom(ctx context.Context, srv *ipc.Server, accountIdx int, addr string) {
	s.rooms.mu.Lock()
	st := s.rooms.rooms[addr]
	delete(s.rooms.rooms, addr)
	s.rooms.mu.Unlock()
	if st != nil {
		for _, w := range st.waiters {
			w <- errors.New("left the room")
		}
	}
	if err := s.db.DeleteRoom(ctx, storage.DeleteRoomParams{AccountJid: s.account.JID, Jid: addr}); err != nil {
		slog.Warn("deleting room", "room", addr, "err", err)
	}
	forgetChat(ctx, s.db, s.account.JID, addr)
	broadcast(srv, ui.ChatRemovedMsg{AccountIdx: accountIdx, Address: addr})
	// Private chats go through the room, gone with it.
	if st != nil {
		if err := s.db.DeleteRoomPrivates(ctx, storage.DeleteRoomPrivatesParams{AccountJid: s.account.JID, Room: addr}); err != nil {
			slog.Warn("deleting private chats", "room", addr, "err", err)
		}
		for nick := range st.privates {
			forgetChat(ctx, s.db, s.account.JID, addr+"/"+nick)
			broadcast(srv, ui.ChatRemovedMsg{AccountIdx: accountIdx, Address: addr + "/" + nick})
		}
	}
}

// handleRoomPresence applies an occupant's presence to their room.
func (s *accountSession) handleRoomPresence(ctx context.Context, srv *ipc.Server, accountIdx int, ev xmpp.RoomPresenceEvent) {
	var waiters []chan error
	var failure error
	var created, renamed, unknown, catchUp, private bool
	ok := s.updateRoom(ev.Room, func(st *roomState) {
		self := ev.Has(xmpp.MUCStatusSelf) || ev.Nick == st.nick
		if ev.RealJID != "" {
			st.realJIDs[ev.Nick] = ev.RealJID
			switch ev.Affiliation {
			case "owner", "admin", "member":
				st.members[ev.RealJID] = true
			case "none", "outcast":
				delete(st.members, ev.RealJID)
			}
		}
		if ev.Available {
			st.occupants[ev.Nick] = ui.Occupant{
				Nick: ev.Nick, JID: ev.RealJID, Affiliation: ev.Affiliation, Role: ev.Role,
				Presence: presenceFromShow(ev.Show),
			}
			if !self {
				return
			}
			renamed = st.nick != ev.Nick
			st.nick, st.occupantID = ev.Nick, ev.OccupantID
			if st.nickWaiter != nil && ev.Nick == st.pendingNick {
				st.nickWaiter <- nil
				st.nickWaiter, st.pendingNick = nil, ""
			}
			created = ev.Has(xmpp.MUCStatusCreated)
			if !st.joined {
				st.joined, st.joinErr = true, ""
				waiters, st.waiters = st.waiters, nil
				unknown = st.info == (xmpp.RoomInfo{})
				catchUp = st.info.MAM
				private = st.info.Private()
			}
			return
		}
		delete(st.occupants, ev.Nick)
		if !self {
			return
		}
		if ev.Has(xmpp.MUCStatusNickChanged) && ev.NewNick != "" {
			st.nick, renamed = ev.NewNick, true
			return
		}
		st.joined = false
		st.joinErr = removalReason(ev)
		clear(st.occupants)
		failure = errors.New(cmp.Or(st.joinErr, "left the room"))
		waiters, st.waiters = st.waiters, nil
	})
	if !ok {
		return
	}
	for _, w := range waiters {
		w <- failure
	}
	if renamed {
		s.persistRoom(ctx, ev.Room)
	}
	if private {
		go func() {
			if client, err := s.liveClient(); err == nil {
				s.refreshRoomMembers(ctx, client, ev.Room)
			}
		}()
	}
	if created {
		slog.Debug("created room; accepting its default configuration", "jid", s.account.JID, "room", ev.Room)
		go func() {
			client, err := s.liveClient()
			if err != nil {
				return
			}
			if err := client.ConfigureInstantRoom(ctx, ev.Room); err != nil {
				slog.Warn("configuring new room", "room", ev.Room, "err", err)
			}
			// Before it existed, it had nothing to say of itself.
			s.refreshRoomInfo(ctx, client, ev.Room)
		}()
	} else if catchUp {
		go func() {
			if client, err := s.liveClient(); err == nil {
				s.syncRoomArchive(ctx, srv, accountIdx, client, ev.Room)
			}
		}()
	} else if unknown {
		go func() {
			if client, err := s.liveClient(); err == nil {
				s.refreshRoomInfo(ctx, client, ev.Room)
			}
		}()
	}
	s.broadcastRoomState(srv, accountIdx, ev.Room)
}

// refreshRoomInfo asks room what it is again.
func (s *accountSession) refreshRoomInfo(ctx context.Context, client *xmpp.Client, room string) {
	ctx, cancel := context.WithTimeout(ctx, roomInfoTimeout)
	defer cancel()
	info, err := client.RoomInfo(ctx, room)
	if err != nil {
		slog.Warn("asking room what it is", "room", room, "err", err)
		return
	}
	var joined bool
	s.updateRoom(room, func(st *roomState) { st.info, joined = info, st.joined })
	if joined && info.Private() {
		s.refreshRoomMembers(ctx, client, room)
	}
}

// refreshRoomMembers reads who's affiliated with room, whom its encrypted
// messages are for whether they're in it or not.
func (s *accountSession) refreshRoomMembers(ctx context.Context, client *xmpp.Client, room string) {
	ctx, cancel := context.WithTimeout(ctx, roomInfoTimeout)
	defer cancel()
	members, err := client.RoomMembers(ctx, room)
	if err != nil {
		// The room may keep its member list to its admins: who's in it, and
		// who was seen in it, is what's left.
		slog.Debug("reading room members", "room", room, "err", err)
		return
	}
	s.updateRoom(room, func(st *roomState) {
		for _, m := range members {
			st.members[m] = true
		}
	})
}

// roomRecipients is whom a message encrypted for room is for: its members
// and everyone in it, but us.
func (s *accountSession) roomRecipients(room string) []string {
	st, _ := s.room(room)
	set := maps.Clone(st.members)
	for _, o := range st.occupants {
		if o.JID != "" {
			set[o.JID] = true
		}
	}
	delete(set, bareJID(s.account.JID))
	return slices.Sorted(maps.Keys(set))
}

// roomEncryptionMode is room's outgoing encryption: legacy OMEMO, what
// group chats use everywhere, by default in a private room (members-only,
// showing real JIDs), and only there; else none.
func (s *accountSession) roomEncryptionMode(ctx context.Context, room string) string {
	st, _ := s.room(room)
	if !st.info.Private() {
		return "none"
	}
	mode, err := s.db.GetChatEncryptionMode(ctx, storage.GetChatEncryptionModeParams{AccountJid: s.account.JID, RosterJid: room})
	if err == nil && mode == "none" {
		return "none"
	}
	return "omemo-v1"
}

// decryptRoomMessage decrypts an encrypted message in room, from the
// sender's real JID: their presence says it, or the room's archive. Failing
// that, the text says why. keyTransport is set for one with no content.
func (s *accountSession) decryptRoomMessage(ctx context.Context, room string, st roomState, ev xmpp.MessageEvent, own bool) (body string, failed, keyTransport bool) {
	sender := cmp.Or(ev.RealJID, st.realJIDs[resourcePart(ev.From)])
	if own {
		sender = bareJID(s.account.JID)
	}
	if sender == "" {
		return "[message could not be decrypted: the room doesn't say who sent it]", true, false
	}
	var mgr *omemolib.Manager
	var protocol omemolib.Protocol
	var enc *omemolib.EncryptedMessage
	var err error
	if ev.EncryptedV1 != nil {
		protocol = omemolib.ProtocolV1
		enc, err = xmpp.DecodeOmemoMessageV1(ev.EncryptedV1, sender)
	} else {
		protocol = omemolib.ProtocolV2
		enc, err = xmpp.DecodeOmemoMessage(ev.Encrypted, sender)
	}
	if err != nil {
		return "[message could not be decrypted: " + err.Error() + "]", true, false
	}
	if mgr = s.omemoManagerFor(protocol); mgr == nil {
		s.waitOmemoReady(ctx, omemoSetupGrace)
		mgr = s.omemoManagerFor(protocol)
	}
	if mgr == nil {
		return "[message could not be decrypted: omemo isn't ready]", true, false
	}
	pt, err := mgr.DecryptMessage(ctx, enc)
	switch {
	case errors.Is(err, omemolib.ErrOwnDeviceKeyMissing):
		return "[message could not be decrypted: not encrypted for this device]", true, false
	case err != nil:
		slog.Warn("decrypting room message", "room", room, "from", ev.From, "sender", sender, "err", err)
		if errors.Is(err, omemolib.ErrUnknownSession) || errors.Is(err, omemolib.ErrPreKeyNotFound) {
			healBrokenSession(ctx, s, mgr, protocol, enc.Sender, sender)
		}
		return "[message could not be decrypted: " + err.Error() + "]", true, false
	case pt == nil:
		return "", false, true
	}
	return string(pt), false, false
}

// presenceFromShow maps a <show/> value to a Presence of someone available.
func presenceFromShow(show string) ui.Presence {
	return mapPresence(xmpp.PresenceEvent{Available: true, Show: show})
}

// removalReason says why the room let us go, "" when we left.
func removalReason(ev xmpp.RoomPresenceEvent) string {
	switch {
	case ev.Has(xmpp.MUCStatusBanned):
		return "banned from the room"
	case ev.Has(xmpp.MUCStatusKicked):
		return "kicked from the room"
	case ev.Has(xmpp.MUCStatusRemoved), ev.Has(xmpp.MUCStatusMembersOnly):
		return "no longer a member"
	case ev.Has(xmpp.MUCStatusShutdown):
		return "the room's service shut down"
	}
	return ""
}

// handlePresenceError takes an error presence from a room as its refusal to
// let us in, reporting whether it was from one.
func (s *accountSession) handlePresenceError(srv *ipc.Server, accountIdx int, ev xmpp.PresenceErrorEvent) bool {
	room := bareJID(ev.From)
	var waiters []chan error
	reason := joinErrorText(ev.Condition, ev.Reason)
	ok := s.updateRoom(room, func(st *roomState) {
		if st.joined {
			// A nick change refused.
			if st.nickWaiter != nil && resourcePart(ev.From) == st.pendingNick {
				st.nickWaiter <- errors.New(reason)
				st.nickWaiter, st.pendingNick = nil, ""
			}
			return
		}
		st.joinErr = reason
		waiters, st.waiters = st.waiters, nil
	})
	if !ok {
		return false
	}
	slog.Warn("room refused us", "jid", s.account.JID, "room", room, "reason", ev.Reason)
	for _, w := range waiters {
		w <- errors.New(reason)
	}
	s.broadcastRoomState(srv, accountIdx, room)
	return true
}

// joinErrorText words a room's refusal (XEP-0045 §7.2.6-7.2.11).
func joinErrorText(condition, reason string) string {
	switch condition {
	case "conflict":
		return "that nick is taken in the room"
	case "registration-required":
		return "the room is for members only"
	case "forbidden":
		return "banned from the room"
	case "not-authorized":
		return "the room needs a password"
	case "service-unavailable":
		return "the room is full"
	case "item-not-found":
		return "the room doesn't exist"
	case "not-allowed":
		return "the room can't be created"
	case "jid-malformed":
		return "the nick isn't valid"
	}
	return reason
}

// handleRoomSubject stores and broadcasts a room's new subject.
func (s *accountSession) handleRoomSubject(ctx context.Context, srv *ipc.Server, accountIdx int, ev xmpp.RoomSubjectEvent) {
	if !s.updateRoom(ev.Room, func(st *roomState) {
		st.subject = ev.Subject
		if st.subjectWaiter != nil && ev.Nick == st.nick {
			st.subjectWaiter <- nil
			st.subjectID, st.subjectWaiter = "", nil
		}
	}) {
		return
	}
	if err := s.db.SetRoomSubject(ctx, storage.SetRoomSubjectParams{AccountJid: s.account.JID, Jid: ev.Room, Subject: ev.Subject}); err != nil {
		slog.Warn("persisting room subject", "room", ev.Room, "err", err)
	}
	s.broadcastRoomState(srv, accountIdx, ev.Room)
}

// roomStanzaRef is what to refer to our stored message id in room by:
// the stanza-id the room gave it (XEP-0444 §3.2, XEP-0461 §3), else id.
func (s *accountSession) roomStanzaRef(ctx context.Context, room, id string) string {
	ref, err := s.db.GetRoomStanzaIDByIDAttr(ctx, storage.GetRoomStanzaIDByIDAttrParams{
		AccountJid: s.account.JID, RosterJid: nullString(room), IDAttr: nullString(id),
	})
	if err != nil || !ref.Valid || ref.String == "" {
		return id
	}
	return ref.String
}

// roomTargetID is the stored id of the message ref refers to in room: by
// its room stanza-id, else ref itself.
func (s *accountSession) roomTargetID(ctx context.Context, room, ref string) string {
	if ref == "" {
		return ""
	}
	id, err := s.db.GetIDAttrByRoomStanzaID(ctx, storage.GetIDAttrByRoomStanzaIDParams{
		AccountJid: s.account.JID, RosterJid: nullString(room), RoomStanzaID: nullString(ref),
	})
	if err != nil || !id.Valid {
		return ref
	}
	return id.String
}

// roomSentBy reports whether the stored message id in room was sent by
// from, or by us when own: in a room anyone could otherwise correct or
// retract anyone's.
func (s *accountSession) roomSentBy(ctx context.Context, room, id string, ev xmpp.MessageEvent, own bool) bool {
	row, err := s.db.GetMessageSenderByIDAttr(ctx, storage.GetMessageSenderByIDAttrParams{
		AccountJid: s.account.JID, RosterJid: nullString(room), IDAttr: nullString(id),
	})
	if err != nil {
		return false
	}
	if own || row.Sent {
		return own && row.Sent
	}
	// The occupant-id outlives a nick, which someone else could take after.
	if st, _ := s.room(room); st.trustsOccupantID(ev.OccupantID) && row.Occupantid.Valid {
		return row.Occupantid.String == ev.OccupantID
	}
	return row.Fromattr.String == ev.From
}

// repliesToOwn reports whether the stored message id in room is one of
// ours.
func (s *accountSession) repliesToOwn(ctx context.Context, room, id string) bool {
	if id == "" {
		return false
	}
	row, err := s.db.GetMessageSenderByIDAttr(ctx, storage.GetMessageSenderByIDAttrParams{
		AccountJid: s.account.JID, RosterJid: nullString(room), IDAttr: nullString(id),
	})
	return err == nil && row.Sent
}

// roomOccupantOf is the occupant JID of whoever sent the stored message id
// in room, "" if unknown.
func (s *accountSession) roomOccupantOf(ctx context.Context, room, id string) string {
	row, err := s.db.GetMessageSenderByIDAttr(ctx, storage.GetMessageSenderByIDAttrParams{
		AccountJid: s.account.JID, RosterJid: nullString(room), IDAttr: nullString(id),
	})
	if err != nil {
		return ""
	}
	if row.Sent {
		if st, ok := s.room(room); ok {
			return room + "/" + st.nick
		}
		return ""
	}
	return row.Fromattr.String
}

// handleRoomMessage stores and broadcasts a message in a room, reporting
// whether ev was one in ours. Rooms are plaintext: an encrypted message in
// one is stored as unreadable.
func handleRoomMessage(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, ev xmpp.MessageEvent) bool {
	_, ok := roomMessage(ctx, srv, accountIdx, s, ev, false)
	return ok
}

// roomMessage applies a message in a room: stores it, or the correction,
// retraction or reactions it is. A new message from the room's archive
// (archived) is returned for the caller to broadcast in its batch; a live
// one is broadcast, counted and maybe notified about here.
func roomMessage(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, ev xmpp.MessageEvent, archived bool) (*ui.Message, bool) {
	room := bareJID(ev.From)
	st, ok := s.room(room)
	if !ok {
		return nil, false
	}
	nick := resourcePart(ev.From)
	own := ev.Groupchat && st.ownInRoom(nick, ev.OccupantID)
	// A reaction set belongs to a person, not to a nick they may change.
	reactor := ev.From
	if st.trustsOccupantID(ev.OccupantID) {
		reactor = "occupant:" + ev.OccupantID
	}
	if own {
		reactor = meReactorJID
	}
	occupantID := ""
	if st.trustsOccupantID(ev.OccupantID) {
		occupantID = ev.OccupantID
	}

	if ev.ReactionTargetID != "" {
		target := s.roomTargetID(ctx, room, ev.ReactionTargetID)
		if err := replaceReactions(ctx, s, room, target, reactor, ev.Reactions); err != nil {
			slog.Warn("persisting room reactions", "room", room, "err", err)
		}
		broadcast(srv, ui.MessageReactionsMsg{
			AccountIdx: accountIdx, From: room, MessageID: target,
			Reactions: loadReactionsForMessage(ctx, s, room, target),
		})
		return nil, true
	}

	if ev.RetractID != "" {
		target := s.roomTargetID(ctx, room, ev.RetractID)
		if !s.roomSentBy(ctx, room, target, ev, own) {
			slog.Warn("dropping a room retraction not from the message's sender", "room", room, "from", ev.From)
			return nil, true
		}
		if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
			AccountJid: s.account.JID, IDAttr: nullString(target), RosterJid: nullString(room), Sent: own,
		}); err != nil {
			slog.Warn("persisting room retraction", "err", err)
		}
		broadcast(srv, ui.MessageRetractedMsg{AccountIdx: accountIdx, From: room, RetractID: target})
		return nil, true
	}

	// Before any decrypt: a message's key is used up the first time, and the
	// archive brings again what came live.
	if ev.ID != "" && ev.ReplaceID == "" {
		if exists, err := s.db.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
			AccountJid: s.account.JID, RosterJid: nullString(room), IDAttr: nullString(ev.ID),
		}); err == nil && exists {
			// Our own message's echo: the room has it, and says what it
			// calls it.
			if own {
				if ev.RoomStanzaID != "" {
					if err := s.db.SetRoomStanzaID(ctx, storage.SetRoomStanzaIDParams{
						AccountJid: s.account.JID, RosterJid: nullString(room), IDAttr: nullString(ev.ID), RoomStanzaID: nullString(ev.RoomStanzaID),
					}); err != nil {
						slog.Warn("persisting room stanza-id", "err", err)
					}
				}
				if _, err := s.db.MarkMessageDelivered(ctx, storage.MarkMessageDeliveredParams{
					AccountJid: s.account.JID, IDAttr: nullString(ev.ID), RosterJid: nullString(room),
				}); err != nil {
					slog.Warn("persisting room echo", "err", err)
				}
				broadcast(srv, ui.MessageDeliveredMsg{AccountIdx: accountIdx, From: room, MessageID: ev.ID})
			}
			return nil, true
		}
	}

	if ev.ReplaceID != "" && ev.ID != "" {
		if applied, err := s.db.CorrectionAppliedByIDAttr(ctx, storage.CorrectionAppliedByIDAttrParams{
			AccountJid: s.account.JID, RosterJid: room, IDAttr: nullString(ev.ID),
		}); err == nil && applied {
			return nil, true
		}
	}

	e2e := ev.Encrypted != nil || ev.EncryptedV1 != nil
	e2eeMethod := ""
	switch {
	case ev.EncryptedV1 != nil:
		e2eeMethod = "omemo-v1"
	case ev.Encrypted != nil:
		e2eeMethod = "omemo-v2"
	}
	body, decryptFailed := ev.Body, false
	if e2e {
		s.omemoMu.Lock()
		defer s.omemoMu.Unlock()
		var keyTransport bool
		body, decryptFailed, keyTransport = s.decryptRoomMessage(ctx, room, st, ev, own)
		if keyTransport {
			return nil, true
		}
	}
	body = stripReplyQuote(body)

	if ev.ReplaceID != "" {
		if !s.roomSentBy(ctx, room, ev.ReplaceID, ev, own) {
			slog.Warn("dropping a room correction not from the message's sender", "room", room, "from", ev.From)
			return nil, true
		}
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid: s.account.JID, Body: sealedBody, Encrypted: encrypted, Edited: true,
			E2eEncrypted: e2e, E2eeMethod: nullString(e2eeMethod),
			IDAttr: nullString(ev.ReplaceID), RosterJid: nullString(room), Sent: own,
		}); err != nil {
			slog.Warn("persisting room correction", "err", err)
		}
		if ev.ID != "" {
			if err := s.db.InsertAppliedCorrection(ctx, storage.InsertAppliedCorrectionParams{
				AccountJid: s.account.JID, RosterJid: room, IDAttr: nullString(ev.ID),
			}); err != nil {
				slog.Warn("recording applied room correction", "err", err)
			}
		}
		broadcast(srv, ui.MessageCorrectedMsg{AccountIdx: accountIdx, From: room, ReplaceID: ev.ReplaceID, NewContent: body, Encrypted: e2e, EncMethod: e2eeMethod})
		return nil, true
	}

	author := cmp.Or(nick, st.name, room)
	if own {
		author = "me"
	}
	replyTo := s.roomTargetID(ctx, room, ev.ReplyToID)
	quotedAuthor, quotedPreview := resolveReplyQuote(ctx, s, room, st.name, replyTo)

	sentAt := ev.SentAt
	if sentAt.IsZero() {
		sentAt = s.now()
	}
	stanzaType := "groupchat"
	sealedBody, encrypted := encryptForStorage(s, body)
	storeID, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:        s.account.JID,
		Sent:              own,
		FromAttr:          nullString(ev.From),
		IDAttr:            nullString(ev.ID),
		Body:              sealedBody,
		Encrypted:         encrypted,
		StanzaType:        stanzaType,
		E2eEncrypted:      e2e,
		E2eeMethod:        nullString(e2eeMethod),
		Delay:             sentAt.Unix(),
		RosterJid:         nullString(room),
		ReplyToIDAttr:     nullString(replyTo),
		ReplyQuoteAuthor:  nullString(quotedAuthor),
		ReplyQuotePreview: nullString(quotedPreview),
		OobUrls:           joinOOBURLs(ev.OOBURLs),
		OccupantID:        nullString(occupantID),
	})
	if err != nil {
		slog.Warn("persisting room message", "room", room, "err", err)
		return nil, true
	}
	if ev.RoomStanzaID != "" && ev.ID != "" {
		if err := s.db.SetRoomStanzaID(ctx, storage.SetRoomStanzaIDParams{
			AccountJid: s.account.JID, RosterJid: nullString(room), IDAttr: nullString(ev.ID), RoomStanzaID: nullString(ev.RoomStanzaID),
		}); err != nil {
			slog.Warn("persisting room stanza-id", "err", err)
		}
	}

	msg := ui.Message{
		ID:            ev.ID,
		StoreID:       storeID,
		Author:        author,
		Content:       body,
		SentAt:        sentAt,
		IsMe:          own,
		Encrypted:     e2e,
		EncMethod:     e2eeMethod,
		Attachments:   ev.OOBURLs,
		DecryptFailed: decryptFailed,
		QuotedAuthor:  quotedAuthor,
		QuotedPreview: quotedPreview,
	}
	msg.ReplyToID = replyTo
	if archived {
		return &msg, true
	}
	broadcast(srv, ui.IncomingMessageMsg{AccountIdx: accountIdx, From: room, ReplyToID: replyTo, Message: msg})
	countUnread(ctx, srv, s, room, []ui.Message{msg})
	autoDownloadAttachments(srv, accountIdx, room, []ui.Message{msg})

	// A busy room would notify all day: only what's for us does.
	if (ui.Mentions(body, st.nick) || s.repliesToOwn(ctx, room, replyTo)) && shouldNotify(s.account.JID, room, decryptFailed, own) {
		daemon.Notify(cmp.Or(st.name, room)+" · "+nick, notifyPreview(body, ev.OOBURLs))
	}
	return nil, true
}

// JoinRoom enters room as nick and bookmarks it, once the room let us in.
// A room new to us that refuses is dropped again.
func (a *adapter) JoinRoom(accountIdx int, room, nick, password string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	j, err := jid.Parse(strings.TrimSpace(room))
	if err != nil || j.Localpart() == "" {
		return fmt.Errorf("%q isn't a room address", room)
	}
	room = j.Bare().String()
	nick = cmp.Or(strings.TrimSpace(nick), defaultNick(s.account.JID))
	if err := xmpp.ValidRoomNick(room, nick); err != nil {
		return err
	}
	if s.inRoster(room) {
		return fmt.Errorf("%s is a contact, not a room", room)
	}
	ctx := context.Background()

	prior, known := s.room(room)
	if known && prior.joined && prior.nick == nick {
		return nil
	}
	s.putRoom(room, prior.name, nick, password, prior.bookmarked)
	answer := make(chan error, 1)
	s.updateRoom(room, func(st *roomState) { st.waiters = append(st.waiters, answer) })
	if !known {
		s.broadcastRoomChat(ctx, a.srv, accountIdx, room)
	}
	s.sendJoin(ctx, a.srv, accountIdx, client, room)

	select {
	case err = <-answer:
	case <-time.After(roomJoinTimeout):
		err = errors.New("the room didn't answer")
	}
	if err != nil {
		if !known {
			s.forgetRoom(ctx, a.srv, accountIdx, room)
		}
		return err
	}

	st, _ := s.room(room)
	if st.info.Name != "" {
		s.putRoom(room, st.info.Name, st.nick, password, prior.bookmarked)
		st, _ = s.room(room)
	}
	if err := client.PublishBookmark(ctx, xmpp.Bookmark{Room: room, Name: st.name, Nick: st.nick, Password: password, Autojoin: true}); err != nil {
		slog.Warn("bookmarking room", "room", room, "err", err)
	} else {
		s.putRoom(room, st.name, st.nick, password, true)
	}
	s.persistRoom(ctx, room)
	s.broadcastRoomChat(ctx, a.srv, accountIdx, room)
	return nil
}

// LeaveRoom exits room and drops its bookmark; history stays.
func (a *adapter) LeaveRoom(accountIdx int, room string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	st, ok := s.room(room)
	if !ok {
		return fmt.Errorf("not in %s", room)
	}
	ctx := context.Background()
	if err := client.LeaveRoom(ctx, room, st.nick); err != nil {
		return err
	}
	if err := client.RemoveBookmark(ctx, room); err != nil {
		slog.Warn("removing room bookmark", "room", room, "err", err)
	}
	s.forgetRoom(ctx, a.srv, accountIdx, room)
	return nil
}

// StartChat gives address a chat without asking them for anything, as a
// message from them would (see ensureChat).
func (a *adapter) StartChat(accountIdx int, address string) error {
	s, err := a.session(accountIdx)
	if err != nil {
		return err
	}
	j, err := jid.Parse(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("%q isn't an address", address)
	}
	ctx := context.Background()
	// An occupant JID: a private chat through their room.
	if s.isRoom(j.Bare().String()) && j.Resourcepart() != "" {
		s.ensurePrivateChat(ctx, a.srv, accountIdx, j.String())
		return nil
	}
	ensureChat(ctx, a.srv, accountIdx, s, j.Bare().String())
	return nil
}

// handlePrivateMessage stores and broadcasts a message sent privately
// between us and an occupant of one of our rooms, reporting whether ev was
// one. Only the two of us and the room relaying it see it.
func handlePrivateMessage(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, ev xmpp.MessageEvent) bool {
	peer := ev.From
	if ev.Outgoing {
		peer = ev.To
	}
	room, nick := bareJID(peer), resourcePart(peer)
	if !s.isRoom(room) || nick == "" {
		return false
	}
	s.ensurePrivateChat(ctx, srv, accountIdx, peer)

	if ev.ReactionTargetID != "" {
		reactor := peer
		if ev.Outgoing {
			reactor = meReactorJID
		}
		if err := replaceReactions(ctx, s, peer, ev.ReactionTargetID, reactor, ev.Reactions); err != nil {
			slog.Warn("persisting private reactions", "with", peer, "err", err)
		}
		broadcast(srv, ui.MessageReactionsMsg{
			AccountIdx: accountIdx, From: peer, MessageID: ev.ReactionTargetID,
			Reactions: loadReactionsForMessage(ctx, s, peer, ev.ReactionTargetID),
		})
		return true
	}
	if ev.RetractID != "" {
		if _, err := s.db.MarkMessageRetracted(ctx, storage.MarkMessageRetractedParams{
			AccountJid: s.account.JID, IDAttr: nullString(ev.RetractID), RosterJid: nullString(peer), Sent: ev.Outgoing,
		}); err != nil {
			slog.Warn("persisting private retraction", "err", err)
		}
		broadcast(srv, ui.MessageRetractedMsg{AccountIdx: accountIdx, From: peer, RetractID: ev.RetractID})
		return true
	}

	body := ev.Body
	decryptFailed := false
	if ev.Encrypted != nil || ev.EncryptedV1 != nil {
		body = "[message could not be decrypted: encrypted private messages in rooms aren't supported]"
		decryptFailed = true
	}
	body = stripReplyQuote(body)

	if ev.ReplaceID != "" {
		sealedBody, encrypted := encryptForStorage(s, body)
		if _, err := s.db.UpdateMessageBodyByID(ctx, storage.UpdateMessageBodyByIDParams{
			AccountJid: s.account.JID, Body: sealedBody, Encrypted: encrypted, Edited: true,
			IDAttr: nullString(ev.ReplaceID), RosterJid: nullString(peer), Sent: ev.Outgoing,
		}); err != nil {
			slog.Warn("persisting private correction", "err", err)
		}
		broadcast(srv, ui.MessageCorrectedMsg{AccountIdx: accountIdx, From: peer, ReplaceID: ev.ReplaceID, NewContent: body})
		return true
	}

	if ev.ID != "" {
		if exists, err := s.db.MessageExistsByIDAttr(ctx, storage.MessageExistsByIDAttrParams{
			AccountJid: s.account.JID, RosterJid: nullString(peer), IDAttr: nullString(ev.ID),
		}); err == nil && exists {
			return true
		}
	}

	author := nick
	if ev.Outgoing {
		author = "me"
	}
	st, _ := s.room(room)
	quotedAuthor, quotedPreview := resolveReplyQuote(ctx, s, peer, nick, ev.ReplyToID)
	sentAt := ev.SentAt
	if sentAt.IsZero() {
		sentAt = s.now()
	}
	sealedBody, encrypted := encryptForStorage(s, body)
	storeID, err := s.db.InsertMessage(ctx, storage.InsertMessageParams{
		AccountJid:        s.account.JID,
		Sent:              ev.Outgoing,
		FromAttr:          nullString(ev.From),
		ToAttr:            nullString(ev.To),
		IDAttr:            nullString(ev.ID),
		Body:              sealedBody,
		Encrypted:         encrypted,
		StanzaType:        "chat",
		Delay:             sentAt.Unix(),
		RosterJid:         nullString(peer),
		ReplyToIDAttr:     nullString(ev.ReplyToID),
		ReplyQuoteAuthor:  nullString(quotedAuthor),
		ReplyQuotePreview: nullString(quotedPreview),
		OobUrls:           joinOOBURLs(ev.OOBURLs),
	})
	if err != nil {
		slog.Warn("persisting private message", "with", peer, "err", err)
		return true
	}
	msg := ui.Message{
		ID:            ev.ID,
		StoreID:       storeID,
		Author:        author,
		Content:       body,
		SentAt:        sentAt,
		IsMe:          ev.Outgoing,
		Attachments:   ev.OOBURLs,
		DecryptFailed: decryptFailed,
		QuotedAuthor:  quotedAuthor,
		QuotedPreview: quotedPreview,
	}
	broadcast(srv, ui.IncomingMessageMsg{AccountIdx: accountIdx, From: peer, ReplyToID: ev.ReplyToID, Message: msg})
	countUnread(ctx, srv, s, peer, []ui.Message{msg})
	autoDownloadAttachments(srv, accountIdx, peer, []ui.Message{msg})
	if shouldNotify(s.account.JID, peer, decryptFailed, ev.Outgoing) {
		daemon.Notify(nick+" · "+cmp.Or(st.name, room), notifyPreview(body, ev.OOBURLs))
	}
	return true
}

// roomSelfPingInterval is how often each room is asked whether it still
// has us (XEP-0410).
const roomSelfPingInterval = 5 * time.Minute

// roomSelfPingTimeout bounds one self-ping: a room's service may be gone.
const roomSelfPingTimeout = 30 * time.Second

// selfPingRooms checks on client's stream, until it ends, that every room
// we think we're in still has us, rejoining one that dropped us: a room
// restarting forgets its occupants without a word.
func selfPingRooms(ctx context.Context, srv *ipc.Server, accountIdx int, s *accountSession, client *xmpp.Client) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic self-pinging rooms", "jid", s.account.JID, "panic", r)
		}
	}()
	ticker := time.NewTicker(roomSelfPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.client.Load() != client || client.Closed() {
			return
		}
		s.checkRooms(ctx, func(ctx context.Context, room, nick string) xmpp.SelfPingResult {
			ctx, cancel := context.WithTimeout(ctx, roomSelfPingTimeout)
			defer cancel()
			return client.SelfPing(ctx, room, nick)
		}, func(room string) { s.sendJoin(ctx, srv, accountIdx, client, room) })
		for _, room := range s.roomAddrs() {
			s.broadcastRoomState(srv, accountIdx, room)
		}
	}
}

// checkRooms self-pings every room we're in, rejoining those that no
// longer have us.
func (s *accountSession) checkRooms(ctx context.Context, ping func(ctx context.Context, room, nick string) xmpp.SelfPingResult, rejoin func(room string)) {
	for _, room := range s.roomAddrs() {
		st, ok := s.room(room)
		if !ok || !st.joined {
			continue
		}
		if ping(ctx, room, st.nick) != xmpp.NotJoined {
			continue
		}
		slog.Warn("the room dropped us; rejoining", "jid", s.account.JID, "room", room)
		rejoin(room)
	}
}

// roomNickTimeout is how long SetRoomNick waits for the room to answer.
const roomNickTimeout = 15 * time.Second

// SetRoomNick changes our nick in room (XEP-0045 §7.6), and in its
// bookmark once the room took it.
func (a *adapter) SetRoomNick(accountIdx int, room, nick string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	nick = strings.TrimSpace(nick)
	if err := xmpp.ValidRoomNick(room, nick); err != nil {
		return err
	}
	st, ok := s.room(room)
	switch {
	case !ok:
		return fmt.Errorf("not in %s", room)
	case !st.joined:
		return errors.New("not in the room yet")
	case st.nick == nick:
		return nil
	}
	answer := make(chan error, 1)
	s.updateRoom(room, func(st *roomState) { st.pendingNick, st.nickWaiter = nick, answer })
	ctx := context.Background()
	if err := client.SetRoomPresence(ctx, room, nick, presenceShow(accountStatus(s.account.Status))); err != nil {
		s.updateRoom(room, func(st *roomState) { st.pendingNick, st.nickWaiter = "", nil })
		return err
	}
	select {
	case err = <-answer:
	case <-time.After(roomNickTimeout):
		s.updateRoom(room, func(st *roomState) { st.pendingNick, st.nickWaiter = "", nil })
		err = errors.New("the room didn't answer")
	}
	if err != nil {
		return err
	}
	st, _ = s.room(room)
	if st.bookmarked {
		if err := client.PublishBookmark(ctx, xmpp.Bookmark{Room: room, Name: st.name, Nick: st.nick, Password: st.password, Autojoin: true}); err != nil {
			slog.Warn("updating room bookmark's nick", "room", room, "err", err)
		}
	}
	s.persistRoom(ctx, room)
	return nil
}

// SetRoomSubject changes room's subject, once the room took it.
func (a *adapter) SetRoomSubject(accountIdx int, room, subject string) error {
	s, client, err := a.online(accountIdx)
	if err != nil {
		return err
	}
	st, ok := s.room(room)
	switch {
	case !ok:
		return fmt.Errorf("not in %s", room)
	case !st.joined:
		return errors.New("not in the room yet")
	case st.subject == subject:
		return nil
	}
	answer, id := make(chan error, 1), xmpp.NewID()
	s.updateRoom(room, func(st *roomState) { st.subjectID, st.subjectWaiter = id, answer })
	if err = client.SetRoomSubject(context.Background(), room, id, subject); err == nil {
		select {
		case err = <-answer:
		case <-time.After(roomNickTimeout):
			err = errors.New("the room didn't answer")
		}
	}
	s.updateRoom(room, func(st *roomState) {
		if st.subjectWaiter == answer {
			st.subjectID, st.subjectWaiter = "", nil
		}
	})
	return err
}

// roomSubjectBounced answers the SetRoomSubject waiting on ev's message,
// reporting whether there was one.
func (s *accountSession) roomSubjectBounced(ev xmpp.MessageBouncedEvent) bool {
	answered := false
	s.updateRoom(bareJID(ev.From), func(st *roomState) {
		if st.subjectWaiter != nil && ev.ID != "" && ev.ID == st.subjectID {
			st.subjectWaiter <- errors.New(cmp.Or(ev.Reason, "the room refused"))
			st.subjectID, st.subjectWaiter = "", nil
			answered = true
		}
	})
	return answered
}

// presenceToRooms sends our new status to every room we're in: a room has
// only the presence sent to it.
func (s *accountSession) presenceToRooms(ctx context.Context, client *xmpp.Client, show string) {
	for _, room := range s.roomAddrs() {
		if st, ok := s.room(room); ok && st.joined {
			if err := client.SetRoomPresence(ctx, room, st.nick, show); err != nil {
				slog.Warn("sending status to room", "room", room, "err", err)
			}
		}
	}
}

// Room archive catch-up (XEP-0313 §5.1.2): the latest page on a first
// join, else everything after the newest message stored, within reason.
const (
	roomArchivePage     = 100
	roomArchiveFirst    = 50
	roomArchiveMaxPages = 20
)

// syncRoomArchive reads what room's archive has that we don't, after the
// newest stored message the room gave an id to.
func (s *accountSession) syncRoomArchive(ctx context.Context, srv *ipc.Server, accountIdx int, client *xmpp.Client, room string) {
	cursor := ""
	if ref, err := s.db.GetLatestRoomStanzaID(ctx, storage.GetLatestRoomStanzaIDParams{
		AccountJid: s.account.JID, RosterJid: nullString(room),
	}); err == nil && ref.Valid {
		cursor = ref.String
	}
	for page := 0; page < roomArchiveMaxPages; page++ {
		pageCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		max := uint64(roomArchivePage)
		if cursor == "" {
			max = roomArchiveFirst
		}
		res, err := client.FetchRoomArchive(pageCtx, room, cursor, max)
		cancel()
		if err != nil {
			slog.Warn("reading room archive", "jid", s.account.JID, "room", room, "after", cursor, "err", err)
			return
		}
		// One the room no longer knows: start over from its latest.
		if cursor != "" && len(res.Items) == 0 && res.Last == "" && !res.Complete {
			cursor = ""
			continue
		}
		var msgs []ui.Message
		for _, am := range res.Items {
			if msg, _ := roomMessage(ctx, srv, accountIdx, s, roomArchiveEvent(am), true); msg != nil {
				msgs = append(msgs, *msg)
			}
		}
		slog.Debug("room archive page", "jid", s.account.JID, "room", room, "after", cursor, "items", len(res.Items), "new", len(msgs), "complete", res.Complete)
		if len(msgs) > 0 {
			broadcast(srv, ui.HistorySyncedMsg{AccountIdx: accountIdx, From: room, Messages: msgs})
			countUnread(ctx, srv, s, room, msgs)
			autoDownloadAttachments(srv, accountIdx, room, msgs)
		}
		// The latest page is all a first join reads; a later one reads on.
		if cursor == "" || res.Complete || res.Last == "" || res.Last == cursor {
			return
		}
		cursor = res.Last
	}
}

// roomArchiveEvent is an item of a room's archive as the message it was.
func roomArchiveEvent(am xmpp.ArchivedMessage) xmpp.MessageEvent {
	ev := xmpp.MessageEvent{
		ID:               cmp.Or(am.ID, am.ArchiveID),
		From:             am.From,
		Body:             am.Body,
		SentAt:           am.SentAt,
		ReplaceID:        am.ReplaceID,
		ReplyToID:        am.ReplyToID,
		RetractID:        am.RetractID,
		ReactionTargetID: am.ReactionTargetID,
		Reactions:        am.Reactions,
		Encrypted:        am.Encrypted,
		EncryptedV1:      am.EncryptedV1,
		OOBURLs:          am.OOBURLs,
		Groupchat:        true,
		RoomStanzaID:     am.ArchiveID,
		OccupantID:       am.OccupantID,
		RealJID:          am.RealJID,
	}
	return ev
}

// encryptForRoom encrypts plaintext once for every recipient in room (see
// roomRecipients), with device lists not refreshed lately fetched again.
func encryptForRoom(ctx context.Context, s *accountSession, mgr *omemolib.Manager, protocol omemolib.Protocol, room string, plaintext []byte) (*omemolib.EncryptedMessage, error) {
	recipients := s.roomRecipients(room)
	if len(recipients) == 0 {
		return nil, errors.New("no one in the room to encrypt for yet")
	}
	for _, r := range recipients {
		refreshPeerDevicesIfStale(ctx, s, protocol, r, mgr.SyncDevices)
	}
	enc, deviceErrs, err := mgr.EncryptMessageTo(ctx, recipients, plaintext)
	if err != nil {
		return nil, fmt.Errorf("omemo-encrypting for %s: %w (device errors: %v)", room, err, deviceErrs)
	}
	// Each member someone can't read for is told about in the log: a
	// message to the room otherwise looks sent to all.
	keyed := map[omemolib.DeviceID]bool{}
	for _, k := range enc.Keys {
		keyed[k.Device] = true
	}
	for _, r := range recipients {
		devices, err := mgr.KnownDevices(ctx, r)
		if err != nil || !slices.ContainsFunc(devices, func(d omemolib.DeviceID) bool { return keyed[d] }) {
			slog.Warn("send: room message carries no key for a member; they won't read it", "room", room, "member", r, "devices", devices, "err", err)
		}
	}
	for _, de := range deviceErrs {
		slog.Debug("send: room message not encrypted for a device", "room", room, "device_jid", de.Device.JID, "device_id", de.Device.ID, "err", de.Err)
	}
	return enc, nil
}
