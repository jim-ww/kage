-- name: TruncateRoster :exec
DELETE FROM rosterJIDs
WHERE accountJID = sqlc.arg(account_jid);


-- name: DeleteRosterByJID :exec
DELETE FROM rosterJIDs
WHERE accountJID = sqlc.arg(account_jid)
	AND jid = sqlc.arg(jid);


-- name: UpsertRoster :exec
INSERT INTO rosterJIDs (accountJID, jid, name, subs)
VALUES (sqlc.arg(account_jid), sqlc.arg(jid), sqlc.arg(name), sqlc.arg(subs))
ON CONFLICT(accountJID, jid) DO UPDATE
SET
	name = excluded.name,
	subs = excluded.subs;


-- name: InsertRosterGroup :exec
INSERT INTO rosterGroups (accountJID, jid, name)
VALUES (sqlc.arg(account_jid), sqlc.arg(jid), sqlc.arg(name))
ON CONFLICT DO NOTHING;


-- name: UpsertRosterVersion :exec
INSERT INTO rosterVer (accountJID, ver)
VALUES (sqlc.arg(account_jid), sqlc.arg(ver))
ON CONFLICT(accountJID) DO UPDATE
SET ver = excluded.ver;


-- name: GetRosterVersion :one
SELECT ver
FROM rosterVer
WHERE accountJID = sqlc.arg(account_jid);


-- name: ListRoster :many
SELECT
	jid,
	name,
	subs
FROM rosterJIDs
WHERE accountJID = sqlc.arg(account_jid);


-- name: InsertMessage :one
INSERT INTO messages (
	accountJID,
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	encrypted,
	e2eEncrypted,
	e2eeMethod,
	stanzaType,
	originID,
	delay,
	rosterJID,
	archiveID,
	replyToIdAttr,
	oobURLs
)
VALUES (
	sqlc.arg(account_jid),
	sqlc.arg(sent),
	sqlc.arg(to_attr),
	sqlc.arg(from_attr),
	sqlc.arg(id_attr),
	sqlc.arg(body),
	sqlc.arg(encrypted),
	sqlc.arg(e2e_encrypted),
	sqlc.arg(e2ee_method),
	sqlc.arg(stanza_type),
	sqlc.arg(origin_id),
	IFNULL(
		NULLIF(sqlc.arg(delay), 0),
		CAST(strftime('%s', 'now') AS INTEGER)
	),
	sqlc.arg(roster_jid),
	sqlc.arg(archive_id),
	sqlc.arg(reply_to_id_attr),
	sqlc.arg(oob_urls)
)
ON CONFLICT (accountJID, originID, fromAttr) DO UPDATE
SET archiveID = excluded.archiveID
RETURNING id;


-- name: InsertCallLog :one
-- A local-only row recording the outcome of a finished voice call, inserted
-- directly by the daemon (never round-tripped through MAM) so it shows up in
-- the chat's normal message timeline like any other message.
INSERT INTO messages (
	accountJID,
	sent,
	rosterJID,
	stanzaType,
	delay,
	callDirection,
	callOutcome,
	callDurationSecs
)
VALUES (
	sqlc.arg(account_jid),
	sqlc.arg(sent),
	sqlc.arg(roster_jid),
	'call',
	CAST(strftime('%s', 'now') AS INTEGER),
	sqlc.arg(call_direction),
	sqlc.arg(call_outcome),
	sqlc.arg(call_duration_secs)
)
RETURNING id;


-- name: MarkMessagesReceived :exec
UPDATE messages
SET received = TRUE
WHERE accountJID = sqlc.arg(account_jid)
	AND sent = TRUE
	AND (
		idAttr = sqlc.arg(message_id)
		OR originID = sqlc.arg(message_id)
	);


-- name: UpdateMessageBodyByID :execrows
-- XEP-0308: apply a correction to a previously sent/received message,
-- identified by its own idAttr within a given contact's history. Also used
-- to opportunistically re-seal a plaintext row once a local storage
-- password becomes available.
UPDATE messages
SET
	body = sqlc.arg(body),
	encrypted = sqlc.arg(encrypted),
	e2eEncrypted = sqlc.arg(e2e_encrypted),
	e2eeMethod = sqlc.arg(e2ee_method),
	edited = sqlc.arg(edited)
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);


-- name: MarkMessageRetracted :execrows
UPDATE messages
SET retracted = TRUE
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);


-- name: MarkMessageEdited :execrows
UPDATE messages
SET edited = TRUE
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);


-- name: MarkMessageDelivered :execrows
UPDATE messages
SET delivered = TRUE
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);

-- name: MarkMessageServerAcked :execrows
UPDATE messages
SET serverAcked = TRUE
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);

-- name: MarkMessageSendFailed :execrows
UPDATE messages
SET sendFailed = TRUE
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid)
	AND serverAcked = FALSE;

-- name: DeleteMessageByID :execrows
-- Used when a manual retry (ui.actionRetryMessage) of a message previously
-- flagged sendFailed succeeds and gets its own fresh row: the old row (kept
-- around by idAttr, never a localID - see InsertMessage's doc comment) would
-- otherwise survive forever and keep reappearing as a Failed duplicate
-- alongside the newly-sent message on every reload.
DELETE FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);


-- name: ListMessagesByRoster :many
SELECT
	id,
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	encrypted,
	e2eEncrypted,
	e2eeMethod,
	stanzaType,
	delay,
	replyToIdAttr,
	retracted,
	edited,
	delivered,
	serverAcked,
	sendFailed,
	oobURLs,
	callDirection,
	callOutcome,
	callDurationSecs
FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid)
	AND stanzaType = COALESCE(
		NULLIF(sqlc.arg(stanza_type), ''),
		stanzaType
	)
ORDER BY delay ASC;

-- ListMessagesByRosterBefore returns one page of a chat's history older
-- than the (before_delay, before_id) cursor, newest-first (the caller
-- reverses rows back to chronological order for display). Pass
-- math.MaxInt64 for both cursor args to fetch the most recent page. Keyset
-- (not OFFSET) pagination: every page is an indexed range scan regardless
-- of how deep the user has scrolled, and pages stay correct even as new
-- messages are inserted concurrently. id (the table's rowid) breaks ties
-- between messages sharing the same delay (second-granularity timestamp).
-- Used instead of ListMessagesByRoster to avoid loading/decrypting an
-- entire multi-thousand message history at once.
-- name: ListMessagesByRosterBefore :many
SELECT
	id,
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	encrypted,
	e2eEncrypted,
	e2eeMethod,
	stanzaType,
	delay,
	replyToIdAttr,
	retracted,
	edited,
	delivered,
	serverAcked,
	sendFailed,
	oobURLs,
	callDirection,
	callOutcome,
	callDurationSecs
FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid)
	AND stanzaType = COALESCE(
		NULLIF(sqlc.arg(stanza_type), ''),
		stanzaType
	)
	AND (
		delay < sqlc.arg(before_delay)
		OR (delay = sqlc.arg(before_delay) AND id < sqlc.arg(before_id))
	)
ORDER BY delay DESC, id DESC
LIMIT sqlc.arg(page_limit);


-- ListMessagesByRosterAtOrAfter is ListMessagesByRosterBefore's mirror: one
-- page of a chat's history at-or-newer than the (after_delay, after_id)
-- cursor, oldest-first, keyset-paginated the same way. Together, the two
-- queries build one "window" of a chat centered on an arbitrary anchor
-- message: this one gets everything from the anchor forward,
-- ListMessagesByRosterBefore gets everything strictly before it - see
-- loadHistoryWindow in history.go, which is the only caller of either.
-- name: ListMessagesByRosterAtOrAfter :many
SELECT
	id,
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	encrypted,
	e2eEncrypted,
	e2eeMethod,
	stanzaType,
	delay,
	replyToIdAttr,
	retracted,
	edited,
	delivered,
	serverAcked,
	sendFailed,
	oobURLs,
	callDirection,
	callOutcome,
	callDurationSecs
FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid)
	AND stanzaType = COALESCE(
		NULLIF(sqlc.arg(stanza_type), ''),
		stanzaType
	)
	AND (
		delay > sqlc.arg(after_delay)
		OR (delay = sqlc.arg(after_delay) AND id >= sqlc.arg(after_id))
	)
ORDER BY delay ASC, id ASC
LIMIT sqlc.arg(page_limit);


-- name: ListAllMessages :many
-- Every message row across every account, oldest first, used by the
-- "export" CLI command. Bodies come back exactly as stored (plaintext or
-- localstore-sealed); the caller decrypts.
SELECT
	accountJID,
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	encrypted,
	e2eEncrypted,
	e2eeMethod,
	originID,
	stanzaType,
	received,
	delay,
	rosterJID,
	archiveID,
	replyToIdAttr,
	retracted,
	edited,
	delivered,
	serverAcked,
	sendFailed,
	oobURLs,
	callDirection,
	callOutcome,
	callDurationSecs
FROM messages
ORDER BY delay ASC, id ASC;

-- name: ListAllReactions :many
-- Every reaction row across every account, used by the "export" CLI
-- command alongside ListAllMessages.
SELECT accountJID, rosterJID, idAttr, fromJID, emoji
FROM messageReactions;

-- name: DeleteReactionsByReactor :exec
DELETE FROM messageReactions
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND fromJID = sqlc.arg(from_jid);


-- name: InsertReaction :exec
INSERT INTO messageReactions (accountJID, rosterJID, idAttr, fromJID, emoji)
VALUES (sqlc.arg(account_jid), sqlc.arg(roster_jid), sqlc.arg(id_attr), sqlc.arg(from_jid), sqlc.arg(emoji))
ON CONFLICT (accountJID, rosterJID, idAttr, fromJID, emoji) DO NOTHING;


-- name: ListReactionsForMessage :many
SELECT fromJID, emoji
FROM messageReactions
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid)
	AND idAttr = sqlc.arg(id_attr);


-- name: InsertEntityCaps :exec
INSERT INTO entityCaps (hash, ver)
VALUES (?, ?)
ON CONFLICT (hash, ver) DO NOTHING;


-- name: GetCapsByJID :one
SELECT
	hash,
	ver
FROM entityCaps AS c
INNER JOIN discoJID AS j
	ON j.caps = c.id
WHERE j.jid = ?;


-- name: GetDiscoIdentitiesByJID :many
SELECT
	cat,
	name,
	typ,
	lang
FROM discoIdentity AS i
INNER JOIN discoIdentityJID AS ij
	ON ij.ident = i.id
INNER JOIN discoJID AS j
	ON j.id = ij.jid
WHERE j.jid = ?;


-- name: GetDiscoFeaturesByJID :many
SELECT
	var
FROM discoFeatures AS f
INNER JOIN discoFeatureJID AS fj
	ON fj.feat = f.id
INNER JOIN discoJID AS j
	ON j.id = fj.jid
WHERE j.jid = ?;


-- name: GetDiscoFormsByJID :one
SELECT forms
FROM discoJID AS j
WHERE j.jid = ?;


-- name: GetServicesByFeature :many
SELECT j.jid
FROM discoJID AS j
INNER JOIN discoFeatureJID AS fj
	ON fj.jid = j.id
INNER JOIN discoFeatures AS f
	ON fj.feat = f.id
WHERE f.var = ?;


-- name: UpsertDiscoJIDCaps :exec
INSERT INTO discoJID (jid, caps)
SELECT
	?,
	entityCaps.id
FROM entityCaps
WHERE entityCaps.ver = ?
ON CONFLICT (jid) DO UPDATE
SET caps = excluded.caps;


-- name: UpsertDiscoJIDCapsWithForms :one
INSERT INTO discoJID (jid, caps, forms)
SELECT
	?,
	entityCaps.id,
	?
FROM entityCaps
WHERE entityCaps.ver = ?
ON CONFLICT (jid) DO UPDATE
SET
	caps = excluded.caps,
	forms = excluded.forms
RETURNING id;


-- name: UpsertDiscoIdentity :one
INSERT INTO discoIdentity (cat, name, typ, lang)
VALUES (?, ?, ?, ?)
ON CONFLICT (cat, name, typ, lang) DO UPDATE
SET
	cat = excluded.cat,
	name = excluded.name,
	typ = excluded.typ,
	lang = excluded.lang
RETURNING id;


-- name: InsertDiscoIdentityJID :exec
INSERT INTO discoIdentityJID (jid, ident)
VALUES (?, ?)
ON CONFLICT (jid, ident) DO NOTHING;


-- name: UpsertDiscoFeature :one
INSERT INTO discoFeatures (var)
VALUES (?)
ON CONFLICT (var) DO UPDATE
SET var = excluded.var
RETURNING id;


-- name: InsertDiscoFeatureJID :exec
INSERT INTO discoFeatureJID (jid, feat)
VALUES (?, ?)
ON CONFLICT(jid, feat) DO NOTHING;


-- name: UpsertPGPPeerKey :exec
INSERT INTO pgpPeerKeys (accountJID, jid, fingerprint)
VALUES (sqlc.arg(account_jid), sqlc.arg(jid), sqlc.arg(fingerprint))
ON CONFLICT (accountJID, jid) DO UPDATE
SET fingerprint = excluded.fingerprint;


-- name: GetPGPPeerKey :one
SELECT fingerprint
FROM pgpPeerKeys
WHERE accountJID = sqlc.arg(account_jid)
	AND jid = sqlc.arg(jid);


-- name: UpsertCallPeerFingerprint :exec
INSERT INTO callPeerFingerprints (accountJID, jid, fingerprint)
VALUES (sqlc.arg(account_jid), sqlc.arg(jid), sqlc.arg(fingerprint))
ON CONFLICT (accountJID, jid) DO UPDATE
SET fingerprint = excluded.fingerprint;


-- name: GetCallPeerFingerprint :one
SELECT fingerprint
FROM callPeerFingerprints
WHERE accountJID = sqlc.arg(account_jid)
	AND jid = sqlc.arg(jid);


-- name: GetLocalKeySalt :one
SELECT salt
FROM localKeySalt
WHERE id = 0;

-- name: SetLocalKeySalt :exec
INSERT INTO localKeySalt (id, salt)
VALUES (FALSE, ?)
ON CONFLICT(id) DO UPDATE
SET salt = excluded.salt;

-- name: GetChatEncryptionMode :one
SELECT mode
FROM chatEncryption
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid);

-- name: SetChatEncryptionMode :exec
INSERT INTO chatEncryption (accountJID, rosterJID, mode)
VALUES (sqlc.arg(account_jid), sqlc.arg(roster_jid), sqlc.arg(mode))
ON CONFLICT (accountJID, rosterJID) DO UPDATE
SET mode = excluded.mode;

-- name: HasGPGChat :one
SELECT EXISTS(
	SELECT 1 FROM chatEncryption
	WHERE accountJID = sqlc.arg(account_jid) AND mode = 'gpg'
);

-- name: IncrementChatUnread :exec
INSERT INTO chatUnread (accountJID, rosterJID, count)
VALUES (sqlc.arg(account_jid), sqlc.arg(roster_jid), sqlc.arg(delta))
ON CONFLICT (accountJID, rosterJID) DO UPDATE
SET count = count + excluded.count;

-- name: ResetChatUnread :exec
INSERT INTO chatUnread (accountJID, rosterJID, count)
VALUES (sqlc.arg(account_jid), sqlc.arg(roster_jid), 0)
ON CONFLICT (accountJID, rosterJID) DO UPDATE
SET count = 0;

-- name: ListChatUnread :many
SELECT rosterJID, count
FROM chatUnread
WHERE accountJID = sqlc.arg(account_jid) AND count > 0;


-- name: SetChatDraft :exec
INSERT INTO chatDraft (accountJID, rosterJID, body, encrypted)
VALUES (sqlc.arg(account_jid), sqlc.arg(roster_jid), sqlc.arg(body), sqlc.arg(encrypted))
ON CONFLICT (accountJID, rosterJID) DO UPDATE
SET body = excluded.body, encrypted = excluded.encrypted;

-- name: DeleteChatDraft :exec
DELETE FROM chatDraft
WHERE accountJID = sqlc.arg(account_jid) AND rosterJID = sqlc.arg(roster_jid);

-- name: ListChatDrafts :many
SELECT rosterJID, body, encrypted
FROM chatDraft
WHERE accountJID = sqlc.arg(account_jid);


-- name: InsertOutboxEntry :one
-- failed/error_text are explicit args (not left to the failed column's
-- schema default) so this single query serves both enqueueOutboxRow (queued
-- while offline: failed=false) and markOutboxFailed (a real send failure,
-- persisted as a durable record: failed=true) - see outbox.failed's doc
-- comment for why a real failure is kept, not just dropped.
INSERT INTO outbox (
	accountJID, localID, toAttr, body, encrypted, filePath,
	replaceID, replyToID, quotedAuthor, quotedBody,
	retractID, reactionTargetID, reactions, oobURLs,
	failed, errorText
)
VALUES (
	sqlc.arg(account_jid), sqlc.arg(local_id), sqlc.arg(to_attr), sqlc.arg(body), sqlc.arg(encrypted), sqlc.arg(file_path),
	sqlc.arg(replace_id), sqlc.arg(reply_to_id), sqlc.arg(quoted_author), sqlc.arg(quoted_body),
	sqlc.arg(retract_id), sqlc.arg(reaction_target_id), sqlc.arg(reactions), sqlc.arg(oob_urls),
	sqlc.arg(failed), sqlc.arg(error_text)
)
RETURNING *;

-- name: ListOutboxByAccount :many
-- Every row, pending and given-up/failed alike, oldest first - used to
-- build the chat view's Pending/Failed placeholders (see
-- pendingOutboxMessagesByPeer), which must show both kinds.
SELECT *
FROM outbox
WHERE accountJID = sqlc.arg(account_jid)
ORDER BY id ASC;

-- name: ListPendingOutboxByAccount :many
-- Same as ListOutboxByAccount but excluding failed=TRUE rows - used by
-- flushOutbox, which must never automatically retry a send the user already
-- saw marked Failed (and, by not deleting it, implicitly accepted rather
-- than discarded).
SELECT *
FROM outbox
WHERE accountJID = sqlc.arg(account_jid) AND failed = FALSE
ORDER BY id ASC;

-- name: DeleteOutboxEntry :exec
DELETE FROM outbox WHERE id = sqlc.arg(id);

-- name: DeleteOutboxEntryByLocalID :one
-- Used for a user-initiated permanent delete of a still-pending send (never
-- sent, unlike a normal message delete which only flags Retracted).
-- RETURNING toAttr both lets the caller broadcast which chat lost a message
-- and, via sql.ErrNoRows, tells "deleted" apart from "already gone" (e.g.
-- lost a race with flushOutbox actually sending it in the meantime).
DELETE FROM outbox
WHERE accountJID = sqlc.arg(account_jid) AND localID = sqlc.arg(local_id)
RETURNING toAttr;


-- name: GetOmemoIdentity :one
SELECT privateKey, deviceID
FROM omemoIdentity
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol);

-- name: SetOmemoIdentity :exec
INSERT INTO omemoIdentity (accountJID, protocol, privateKey, deviceID)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(private_key), sqlc.arg(device_id))
ON CONFLICT (accountJID, protocol) DO UPDATE
SET privateKey = excluded.privateKey, deviceID = excluded.deviceID;


-- name: GetOmemoCurrentSignedPreKey :one
SELECT id, public, private, signature
FROM omemoSignedPreKey
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND stale = FALSE
ORDER BY id DESC
LIMIT 1;

-- name: GetOmemoStaleSignedPreKey :one
SELECT id, public, private, signature
FROM omemoSignedPreKey
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND stale = TRUE
LIMIT 1;

-- name: MarkOmemoSignedPreKeyStale :exec
UPDATE omemoSignedPreKey
SET stale = TRUE
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND stale = FALSE;

-- name: DeleteOmemoStaleSignedPreKey :exec
DELETE FROM omemoSignedPreKey
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND stale = TRUE;

-- name: InsertOmemoSignedPreKey :exec
INSERT INTO omemoSignedPreKey (accountJID, protocol, id, public, private, signature, stale)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(id), sqlc.arg(public), sqlc.arg(private), sqlc.arg(signature), FALSE);


-- name: CountOmemoPreKeys :one
SELECT count(*)
FROM omemoPreKey
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol);

-- name: ListOmemoPreKeys :many
SELECT id, public, private
FROM omemoPreKey
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol);

-- name: InsertOmemoPreKey :exec
INSERT INTO omemoPreKey (accountJID, protocol, id, public, private)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(id), sqlc.arg(public), sqlc.arg(private));

-- name: ConsumeOmemoPreKey :one
DELETE FROM omemoPreKey
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND id = sqlc.arg(id)
RETURNING id, public, private;


-- name: GetOmemoNextPreKeyID :one
SELECT nextID
FROM omemoNextPreKeyID
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol);

-- name: SetOmemoNextPreKeyID :exec
INSERT INTO omemoNextPreKeyID (accountJID, protocol, nextID)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(next_id))
ON CONFLICT (accountJID, protocol) DO UPDATE
SET nextID = excluded.nextID;


-- name: GetOmemoSession :one
SELECT data
FROM omemoSession
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND peerJID = sqlc.arg(peer_jid) AND deviceID = sqlc.arg(device_id);

-- name: PutOmemoSession :exec
INSERT INTO omemoSession (accountJID, protocol, peerJID, deviceID, data)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(peer_jid), sqlc.arg(device_id), sqlc.arg(data))
ON CONFLICT (accountJID, protocol, peerJID, deviceID) DO UPDATE
SET data = excluded.data;

-- name: DeleteOmemoSession :exec
DELETE FROM omemoSession
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND peerJID = sqlc.arg(peer_jid) AND deviceID = sqlc.arg(device_id);


-- name: GetOmemoTrust :one
SELECT state
FROM omemoTrust
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND identityKey = sqlc.arg(identity_key);

-- name: SetOmemoTrust :exec
INSERT INTO omemoTrust (accountJID, protocol, identityKey, state)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(identity_key), sqlc.arg(state))
ON CONFLICT (accountJID, protocol, identityKey) DO UPDATE
SET state = excluded.state;


-- name: ListOmemoDevices :many
SELECT deviceID
FROM omemoDevice
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND peerJID = sqlc.arg(peer_jid);

-- name: DeleteOmemoDevices :exec
DELETE FROM omemoDevice
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND peerJID = sqlc.arg(peer_jid);

-- name: InsertOmemoDevice :exec
INSERT INTO omemoDevice (accountJID, protocol, peerJID, deviceID)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(peer_jid), sqlc.arg(device_id))
ON CONFLICT (accountJID, protocol, peerJID, deviceID) DO NOTHING;


-- name: GetOmemoRemoteIdentity :one
SELECT identityKey
FROM omemoRemoteIdentity
WHERE accountJID = sqlc.arg(account_jid) AND protocol = sqlc.arg(protocol) AND peerJID = sqlc.arg(peer_jid) AND deviceID = sqlc.arg(device_id);

-- name: PutOmemoRemoteIdentity :exec
INSERT INTO omemoRemoteIdentity (accountJID, protocol, peerJID, deviceID, identityKey)
VALUES (sqlc.arg(account_jid), sqlc.arg(protocol), sqlc.arg(peer_jid), sqlc.arg(device_id), sqlc.arg(identity_key))
ON CONFLICT (accountJID, protocol, peerJID, deviceID) DO UPDATE
SET identityKey = excluded.identityKey;


-- name: GetOmemoPeerProtocol :one
SELECT protocol, probedAt
FROM omemoPeerProtocol
WHERE accountJID = sqlc.arg(account_jid) AND peerJID = sqlc.arg(peer_jid);

-- name: SetOmemoPeerProtocol :exec
INSERT INTO omemoPeerProtocol (accountJID, peerJID, protocol, probedAt)
VALUES (sqlc.arg(account_jid), sqlc.arg(peer_jid), sqlc.arg(protocol), sqlc.arg(probed_at))
ON CONFLICT (accountJID, peerJID) DO UPDATE
SET protocol = excluded.protocol, probedAt = excluded.probedAt;


-- name: ListMamSyncCursors :many
SELECT rosterJID, archiveID, lastSentAt
FROM mamSyncCursor
WHERE accountJID = sqlc.arg(account_jid);

-- name: UpsertMamSyncCursor :exec
INSERT INTO mamSyncCursor (accountJID, rosterJID, archiveID, lastSentAt)
VALUES (sqlc.arg(account_jid), sqlc.arg(roster_jid), sqlc.arg(archive_id), sqlc.arg(last_sent_at))
ON CONFLICT (accountJID, rosterJID) DO UPDATE
SET archiveID = excluded.archiveID, lastSentAt = excluded.lastSentAt;


-- name: GetMessageDelayByArchiveID :one
-- Resolves a stored mamSyncCursor.archiveID back to the wall-clock time of
-- the message it points to, so a cursor the server's RSM paging can no
-- longer resolve (see syncArchiveForContact's start-date fallback) can be
-- retried with an XEP-0313 <start> date filter instead of the poisoned id.
SELECT delay
FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND archiveID = sqlc.arg(archive_id)
LIMIT 1;

-- name: MessageExistsByArchiveID :one
-- Checked before decrypting a XEP-0313 (MAM) archive item, so a page
-- re-fetched after a stale cursor doesn't re-run OMEMO decrypt (and its
-- ratchet side effects) on ciphertext already stored.
SELECT EXISTS (
	SELECT 1 FROM messages
	WHERE accountJID = sqlc.arg(account_jid)
		AND archiveID = sqlc.arg(archive_id)
);

-- name: ListEncryptedMessageBodies :many
-- Every locally-encrypted message body across every account, used by the
-- storage-password change flow to decrypt-and-reseal each row inside one
-- transaction. id is the table's rowid, the simplest stable key to update
-- back by (no account/roster scoping needed - the local storage key is one
-- shared secret for the whole database, not per-account).
SELECT id, body
FROM messages
WHERE encrypted = TRUE;

-- name: UpdateMessageBodyByRowID :exec
UPDATE messages
SET body = sqlc.arg(body), encrypted = sqlc.arg(encrypted)
WHERE id = sqlc.arg(id);


-- name: ListEncryptedChatDrafts :many
-- Every locally-encrypted draft across every account, used alongside
-- ListEncryptedMessageBodies by the storage-password change flow.
SELECT accountJID, rosterJID, body
FROM chatDraft
WHERE encrypted = TRUE;


-- name: MessageExistsByIDAttr :one
-- Checked before decrypting a live incoming OMEMO message, so a message
-- already backfilled via MAM (or otherwise already stored) isn't decrypted
-- a second time.
SELECT EXISTS (
	SELECT 1 FROM messages
	WHERE accountJID = sqlc.arg(account_jid)
		AND rosterJID = sqlc.arg(roster_jid)
		AND idAttr = sqlc.arg(id_attr)
);

-- name: InsertAppliedCorrection :exec
-- Records a XEP-0308 correction stanza's own idAttr/archiveID once its
-- decrypted body has been applied to the target message, so a redelivery
-- (stream resumption or a later MAM resync) can be recognized without
-- re-running OMEMO decrypt against an already-consumed ratchet key - see
-- appliedCorrections's doc comment in schema.sql.
INSERT INTO appliedCorrections (accountJID, rosterJID, idAttr, archiveID)
VALUES (sqlc.arg(account_jid), sqlc.arg(roster_jid), sqlc.narg(id_attr), sqlc.narg(archive_id))
ON CONFLICT DO NOTHING;

-- name: CorrectionAppliedByIDAttr :one
SELECT EXISTS (
	SELECT 1 FROM appliedCorrections
	WHERE accountJID = sqlc.arg(account_jid)
		AND rosterJID = sqlc.arg(roster_jid)
		AND idAttr = sqlc.arg(id_attr)
);

-- name: CorrectionAppliedByArchiveID :one
SELECT EXISTS (
	SELECT 1 FROM appliedCorrections
	WHERE accountJID = sqlc.arg(account_jid)
		AND archiveID = sqlc.arg(archive_id)
);
