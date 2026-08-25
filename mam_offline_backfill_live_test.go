//go:build integration

package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jim-ww/kage/config"
	"github.com/jim-ww/kage/storage"
	"github.com/jim-ww/kage/xmpp"
)

// devtest/prosody keeps its archive forever (archive_expires_after = "never"
// in prosody.cfg.lua.tmpl) so runs can share alice@localhost's history. Every
// test here tags its own messages with a unique prefix and filters on it,
// rather than assuming it owns the archive.
func backfillPrefix(name string) string {
	return fmt.Sprintf("%s-%d ", name, time.Now().UnixNano())
}

// newBackfillSession builds alice's kage-side session: her own storage, and a
// roster holding just bob, which is what syncArchive iterates.
func newBackfillSession(t *testing.T, dbName string) (*accountSession, *sql.DB) {
	t.Helper()
	dbConn, queries, err := storage.Open(filepath.Join(t.TempDir(), dbName))
	if err != nil {
		t.Fatalf("open alice storage: %v", err)
	}
	sess := &accountSession{
		account: config.Account{JID: "alice@localhost", Password: "alicepw"},
		db:      queries,
	}
	sess.roster.Store(&map[string]rosterEntry{"bob@localhost": {Subs: "both"}})
	return sess, dbConn
}

func dialLive(t *testing.T, ctx context.Context, jid, password string, tlsConfig *tls.Config) *xmpp.Client {
	t.Helper()
	c, err := xmpp.Dial(ctx, jid, password, tlsConfig)
	if err != nil {
		t.Fatalf("dial %s: %v", jid, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// sendAll sends each body from client to peerJID, then waits for Prosody to
// finish archiving them - it indexes asynchronously as it routes, so a MAM
// query issued immediately after can race the server's own write.
func sendAll(t *testing.T, ctx context.Context, client *xmpp.Client, peerJID string, bodies []string) {
	t.Helper()
	for _, body := range bodies {
		if _, err := client.Send(ctx, peerJID, body, xmpp.SendOptions{}); err != nil {
			t.Fatalf("send %q: %v", body, err)
		}
	}
	time.Sleep(1500 * time.Millisecond)
}

// storedBodies returns the bodies of alice's stored messages with bob that
// carry prefix, oldest first.
func storedBodies(t *testing.T, ctx context.Context, sess *accountSession, prefix string) []storage.ListMessagesByRosterRow {
	t.Helper()
	rows, err := sess.db.ListMessagesByRoster(ctx, storage.ListMessagesByRosterParams{
		AccountJid: "alice@localhost",
		RosterJid:  nullString("bob@localhost"),
	})
	if err != nil {
		t.Fatalf("ListMessagesByRoster: %v", err)
	}
	var got []storage.ListMessagesByRosterRow
	for _, r := range rows {
		if strings.HasPrefix(r.Body.String, prefix) {
			got = append(got, r)
		}
	}
	return got
}

// TestMAMBackfillsPeerMessagesReceivedWhileOffline is the reported scenario,
// end to end against a real Prosody: alice's kage is closed, bob sends her a
// handful of messages, and alice reopens kage. Nothing about those messages
// ever reached her live - there was no stream to deliver them on - so MAM
// backfill on reconnect is the only thing that can surface them. When that
// silently gives up (as it did on a cursor the server had stopped resolving)
// the messages never appear anywhere in kage while showing up fine in every
// other client on the same account.
//
// The initial sync before the disconnect matters: it leaves a real cursor
// behind, so the reconnect exercises the incremental resume path this broke
// on rather than a first-ever walk from an empty cursor.
func TestMAMBackfillsPeerMessagesReceivedWhileOffline(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	sess, _ := newBackfillSession(t, "alice-offline-backfill.db")

	kage := dialLive(t, ctx, "alice@localhost", "alicepw", tlsConfig)
	sess.client.Store(kage)
	syncArchive(ctx, nil, 0, sess) // catch up, exactly like a real session start

	// kage closes: no live delivery, no carbons, nothing.
	if err := kage.Close(); err != nil {
		t.Fatalf("close alice (kage): %v", err)
	}

	bob := dialLive(t, ctx, "bob@localhost", "bobpw", tlsConfig)

	prefix := backfillPrefix("offline-backfill")
	want := []string{
		prefix + "you there?",
		prefix + "second while you were away",
		prefix + "third",
		prefix + "fourth",
		prefix + "last one",
	}
	sendAll(t, ctx, bob, "alice@localhost", want)

	// alice reopens kage. connectAccountLive's callers all follow it with
	// syncArchive; that is the whole recovery mechanism for this window.
	kage2 := dialLive(t, ctx, "alice@localhost", "alicepw", tlsConfig)
	sess.client.Store(kage2)
	syncArchive(ctx, nil, 0, sess)

	got := storedBodies(t, ctx, sess, prefix)
	if len(got) != len(want) {
		bodies := make([]string, len(got))
		for i, r := range got {
			bodies[i] = r.Body.String
		}
		t.Fatalf("backfilled %d of %d messages sent while offline\n got: %q\nwant: %q", len(got), len(want), bodies, want)
	}
	for i := range want {
		if got[i].Body.String != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i].Body.String, want[i])
		}
		if got[i].Sent {
			t.Errorf("message %d (%q): Sent=true, want false - bob sent it, not alice", i, got[i].Body.String)
		}
	}
}

// TestMAMPagingWalksEveryPageViaRSMLast pins that paging resumes from the
// page's RSM <last> id. syncArchiveForContact used to resume from the archive
// id of the final item it kept, which is not the same thing: a server
// archives chat states, receipts and markers alongside real messages and
// those get filtered out of the results, so the kept tail lags the page's
// real end - and a page filtered down to nothing at all reads as "the archive
// ends here", stranding the sync permanently before whatever follows.
//
// A small max forces several pages out of a handful of messages, so the walk
// has to chain Last correctly to see all of them.
func TestMAMPagingWalksEveryPageViaRSMLast(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	bob := dialLive(t, ctx, "bob@localhost", "bobpw", tlsConfig)

	prefix := backfillPrefix("rsm-last-paging")
	want := []string{
		prefix + "page one a",
		prefix + "page one b",
		prefix + "page two a",
		prefix + "page two b",
		prefix + "page three a",
	}
	sendAll(t, ctx, bob, "alice@localhost", want)

	alice := dialLive(t, ctx, "alice@localhost", "alicepw", tlsConfig)

	const perPage = 2
	var got []string
	after := ""
	pages := 0
	for ; pages < 500; pages++ {
		res, err := alice.FetchArchive(ctx, "bob@localhost", after, perPage)
		if err != nil {
			t.Fatalf("FetchArchive(after=%q): %v", after, err)
		}
		for _, item := range res.Items {
			if strings.HasPrefix(item.Body, prefix) {
				got = append(got, item.Body)
			}
		}
		if res.Complete || res.Last == "" {
			break
		}
		if res.Last == after {
			t.Fatalf("page %d reported last=%q, the same cursor it was asked to resume after - paging cannot advance", pages, res.Last)
		}
		after = res.Last
	}

	if len(got) != len(want) {
		t.Fatalf("walked %d pages and found %d of %d messages\n got: %q\nwant: %q", pages+1, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i], want[i])
		}
	}
	if pages == 0 {
		t.Errorf("everything arrived on one page (max=%d); this test only proves something if paging actually happened", perPage)
	}
}

// TestMAMCursorRecoversWithoutAStoredRowForItsArchiveID covers the regression
// that made the whole backfill silently unreachable in production.
//
// syncArchiveForContact deliberately advances its cursor past archive items
// that never get a messages row - an item this device can't decrypt
// (ErrOwnDeviceKeyMissing) would otherwise be re-fetched and re-attempted
// forever. Recovering from a cursor the server has stopped resolving then
// needs that cursor's timestamp to re-query by <start> date, and it used to
// look that up in the messages table by archive id: guaranteed to find
// nothing for precisely the cursors most likely to need recovering. The
// lookup failed, recovery reported "nothing to do", and the contact's sync
// stayed wedged on a dead cursor across every future reconnect.
//
// The cursor's row is deleted out from under it here to reproduce that state
// exactly, and recovery is driven directly - a real server won't refuse to
// resolve one of its own valid ids on demand, so the poisoning is done on the
// local side instead.
func TestMAMCursorRecoversWithoutAStoredRowForItsArchiveID(t *testing.T) {
	tlsConfig := devtestTLSConfig(t)
	ctx := context.Background()

	sess, dbConn := newBackfillSession(t, "alice-cursor-recovery.db")

	alice := dialLive(t, ctx, "alice@localhost", "alicepw", tlsConfig)
	sess.client.Store(alice)

	bob := dialLive(t, ctx, "bob@localhost", "bobpw", tlsConfig)

	// One message establishes a real cursor pointing at a real archive item.
	prefix := backfillPrefix("cursor-recovery")
	sendAll(t, ctx, bob, "alice@localhost", []string{prefix + "the message the cursor will park on"})
	syncArchive(ctx, nil, 0, sess)

	cursors, err := sess.db.ListMamSyncCursors(ctx, "alice@localhost")
	if err != nil {
		t.Fatalf("ListMamSyncCursors: %v", err)
	}
	var cur storage.ListMamSyncCursorsRow
	for _, c := range cursors {
		if c.Rosterjid == "bob@localhost" {
			cur = c
		}
	}
	if cur.Archiveid == "" {
		t.Fatalf("no mam cursor recorded for bob@localhost after syncing; cursors=%+v", cursors)
	}
	if cur.Lastsentat == 0 {
		t.Fatalf("cursor %q recorded no timestamp; recovery would have nothing to anchor on", cur.Archiveid)
	}

	// Reproduce the poisoned state: the cursor points at an archive item with
	// no messages row, exactly as it would after that item was skipped as
	// undecryptable.
	if _, err := dbConn.ExecContext(ctx,
		"DELETE FROM messages WHERE accountJID = ? AND archiveID = ?",
		"alice@localhost", cur.Archiveid); err != nil {
		t.Fatalf("deleting the cursor's messages row: %v", err)
	}
	if _, err := sess.db.GetMessageDelayByArchiveID(ctx, storage.GetMessageDelayByArchiveIDParams{
		AccountJid: "alice@localhost",
		ArchiveID:  sql.NullString{String: cur.Archiveid, Valid: true},
	}); err == nil {
		t.Fatal("the cursor's archive id still resolves in the messages table; the poisoned state was not reproduced")
	}

	// Messages arriving after the cursor went bad are the ones a wedged sync
	// can never reach.
	missed := []string{prefix + "sent after the cursor went bad", prefix + "and another"}
	sendAll(t, ctx, bob, "alice@localhost", missed)

	t.Run("recovers from the cursor's own timestamp", func(t *testing.T) {
		page, ok := sess.recoverMAMCursor(ctx, alice, "bob@localhost",
			mamCursor{archiveID: cur.Archiveid, sentAt: cur.Lastsentat}, 50)
		if !ok {
			t.Fatal("recoverMAMCursor gave up even though the cursor carries its own timestamp - this is the regression: it used to insist on a messages row that, by construction, is not there")
		}
		assertContainsAll(t, page, missed)
	})

	t.Run("recovers a legacy cursor with no timestamp by re-walking", func(t *testing.T) {
		// A cursor persisted before lastSentAt existed reads back as 0, and
		// with its messages row gone there is nothing left to anchor a <start>
		// date on - so the archive gets re-walked from the beginning instead
		// of the contact being abandoned.
		page, ok := sess.recoverMAMCursor(ctx, alice, "bob@localhost",
			mamCursor{archiveID: cur.Archiveid, sentAt: 0}, 50)
		if !ok {
			t.Fatal("recoverMAMCursor gave up on a legacy cursor instead of re-walking the archive")
		}
		if len(page.Items) == 0 && page.Last == "" {
			t.Fatal("re-walk came back with nothing at all")
		}
	})

	// Finally, the whole point: a normal sync from the poisoned cursor has to
	// end up with the missed messages in storage.
	if err := sess.db.UpsertMamSyncCursor(ctx, storage.UpsertMamSyncCursorParams{
		AccountJid: "alice@localhost",
		RosterJid:  "bob@localhost",
		ArchiveID:  cur.Archiveid,
		LastSentAt: cur.Lastsentat,
	}); err != nil {
		t.Fatalf("restoring the poisoned cursor: %v", err)
	}
	syncArchive(ctx, nil, 0, sess)

	got := storedBodies(t, ctx, sess, prefix)
	var bodies []string
	for _, r := range got {
		bodies = append(bodies, r.Body.String)
	}
	for _, want := range missed {
		found := false
		for _, b := range bodies {
			if b == want {
				found = true
			}
		}
		if !found {
			t.Errorf("after syncing from the poisoned cursor, %q is still missing from storage (have %q)", want, bodies)
		}
	}
}

// assertContainsAll checks every body in want appears in page.
func assertContainsAll(t *testing.T, page xmpp.ArchivePage, want []string) {
	t.Helper()
	seen := make(map[string]bool, len(page.Items))
	for _, item := range page.Items {
		seen[item.Body] = true
	}
	for _, body := range want {
		if !seen[body] {
			t.Errorf("recovered page is missing %q (got %d items)", body, len(page.Items))
		}
	}
}
