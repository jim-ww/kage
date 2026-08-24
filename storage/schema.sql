-- Things to note if you're not familiar with sqlite3:
--
--  - After connecting always run "PRAGMA foreign_keys = ON" or foreign key
--    constraints will not be honored.
--  - Primary keys may be NULL (and thus always need a NOT NULL constraint)
--  - Tables with primary keys that are not integers and that don't need auto
--    incrementing counters can use WITHOUT ROWID to save some space.
--
-- One database holds every configured account's data, distinguished by an
-- accountJID column on each account-scoped table.

PRAGMA application_id = 0x636f6d6d;

CREATE TABLE IF NOT EXISTS messages (
	id            INTEGER  PRIMARY KEY NOT NULL,
	accountJID    TEXT     NOT NULL,
	sent          BOOLEAN  NOT NULL,
	toAttr        TEXT,
	fromAttr      TEXT,
	idAttr        TEXT,
	body          TEXT,
	encrypted     BOOLEAN  NOT NULL DEFAULT FALSE, -- whether body is AES-sealed (crypto/localstore) or plain text
	e2eEncrypted  BOOLEAN  NOT NULL DEFAULT FALSE, -- whether the message was end-to-end encrypted (OMEMO/GPG) on the wire
	e2eeMethod    TEXT, -- which mechanism did the encrypting: "omemo" or "gpg"; NULL when e2eEncrypted is FALSE
	originID      TEXT,
	stanzaType    TEXT     NOT NULL DEFAULT 'normal', -- RFC 6121 § 5.2.2
	received      BOOLEAN  NOT NULL DEFAULT FALSE,
	delay         INTEGER  NOT NULL DEFAULT (CAST(strftime('%s', 'now') AS INTEGER)),
	rosterJID     TEXT,
	archiveID     TEXT, -- XEP-0313 (MAM) server-assigned archive id, used as the RSM "after" cursor to resume backfill
	replyToIdAttr TEXT, -- XEP-0461: idAttr of the message this one replies to
	retracted     BOOLEAN  NOT NULL DEFAULT FALSE, -- XEP-0424: sender attempted to retract this; content is kept, just flagged
	edited        BOOLEAN  NOT NULL DEFAULT FALSE, -- XEP-0308: this row's body was overwritten by a later correction
	delivered     BOOLEAN  NOT NULL DEFAULT FALSE, -- XEP-0184: peer acknowledged receipt of a message we sent
	serverAcked   BOOLEAN  NOT NULL DEFAULT FALSE, -- our own server confirmed it has this (see account.go's trackForServerAck/confirmPendingAcks) - meaningful only when sent = TRUE; a message we received is never "server acked" in this sense
	sendFailed    BOOLEAN  NOT NULL DEFAULT FALSE, -- confirmPendingAcks's server-ack ping timed out and the connection was declared dead (see account.go) - a message we can no longer credibly claim ever reached the server, distinct from an outbox row that failed before ever being written to the socket
	oobURLs       TEXT, -- XEP-0066: newline-separated URLs the sender explicitly marked as file attachments; NULL/empty means none
	callDirection TEXT, -- call log rows only (stanzaType = 'call'): 'incoming' or 'outgoing'
	callOutcome   TEXT, -- call log rows only: 'answered', 'missed', 'declined', or 'failed'
	callDurationSecs INTEGER, -- call log rows only: meaningful when callOutcome = 'answered'

	UNIQUE (accountJID, originID, fromAttr),
	UNIQUE (accountJID, archiveID)
);

-- A message's stanza id is unique within one conversation on one account, so
-- re-inserting the same idAttr for the same (accountJID, rosterJID) fails
-- instead of adding a duplicate row. Partial (idAttr IS NOT NULL) since some
-- rows predate idAttr being recorded at all.
CREATE UNIQUE INDEX IF NOT EXISTS messagesAccountRosterJIDIdAttr
	ON messages (accountJID, rosterJID, idAttr)
	WHERE idAttr IS NOT NULL;

-- Serves ListMessagesByRosterBefore's keyset pagination (newest-first range
-- scan per chat) without a full table scan on large histories.
CREATE INDEX IF NOT EXISTS messagesAccountRosterJIDDelay
	ON messages (accountJID, rosterJID, delay DESC, id DESC);

-- XEP-0444: each row is one reactor's one emoji on one message. A reactor's
-- full set is replaced (not added-to) whenever a new <reactions/> stanza
-- arrives for them, by deleting their existing rows for that idAttr first.
CREATE TABLE IF NOT EXISTS messageReactions (
	id         INTEGER  PRIMARY KEY NOT NULL,
	accountJID TEXT     NOT NULL,
	rosterJID  TEXT     NOT NULL, -- bare JID of the chat the reacted-to message belongs to; a stanza id is only
	                              -- unique within one conversation, so a reaction must be scoped by it too, same
	                              -- as messages.rosterJID (see messagesAccountRosterJIDIdAttr)
	idAttr     TEXT     NOT NULL, -- idAttr of the message being reacted to
	fromJID    TEXT     NOT NULL, -- bare JID of the reactor ("me" for our own account)
	emoji      TEXT     NOT NULL,

	UNIQUE (accountJID, rosterJID, idAttr, fromJID, emoji)
);

-- XEP-0373: caches a peer's OpenPGP key fingerprint, discovered from their
-- PEP node, so we don't have to query it again on every send.
CREATE TABLE IF NOT EXISTS pgpPeerKeys (
	accountJID  TEXT NOT NULL,
	jid         TEXT NOT NULL,
	fingerprint TEXT NOT NULL,

	PRIMARY KEY (accountJID, jid)
) WITHOUT ROWID;

-- Trust-on-first-use cache of a call peer's DTLS-SRTP certificate
-- fingerprint (XEP-0320, see xmpp.DTLSFingerprint) - remembered per bare JID
-- after a call's fingerprint is first seen, so a later call from the same
-- contact carrying a *different* fingerprint (e.g. their signaling server
-- swapping it in transit) can be flagged instead of silently trusted. See
-- callFingerprintMismatch in callsession.go.
CREATE TABLE IF NOT EXISTS callPeerFingerprints (
	accountJID  TEXT NOT NULL,
	jid         TEXT NOT NULL,
	fingerprint TEXT NOT NULL,

	PRIMARY KEY (accountJID, jid)
) WITHOUT ROWID;

-- The salt for deriving the AES-256 key message bodies are sealed under at
-- rest (crypto/localstore) from the local storage password — one key for
-- the whole database, shared by every account. The salt itself isn't
-- secret; only the password + salt together produce the key, and the key is
-- never persisted anywhere.
CREATE TABLE IF NOT EXISTS localKeySalt (
	id   BOOLEAN PRIMARY KEY DEFAULT FALSE CHECK (id = FALSE),
	salt BLOB    NOT NULL
) WITHOUT ROWID;

-- Per-chat encryption preference (crypto/omemo, crypto/gpg, or none). Absent
-- row means the default: "omemo".
CREATE TABLE IF NOT EXISTS chatEncryption (
	accountJID TEXT NOT NULL,
	rosterJID  TEXT NOT NULL,
	mode       TEXT NOT NULL, -- "omemo" | "gpg" | "none"

	PRIMARY KEY (accountJID, rosterJID)
) WITHOUT ROWID;

-- Local-only unread message count per chat. Never synced to the network —
-- purely a client-side "have I looked at this chat" cursor, reset to zero
-- when the chat is opened in the UI. Absent row means zero unread.
CREATE TABLE IF NOT EXISTS chatUnread (
	accountJID TEXT NOT NULL,
	rosterJID  TEXT NOT NULL,
	count      INTEGER NOT NULL DEFAULT 0,

	PRIMARY KEY (accountJID, rosterJID)
) WITHOUT ROWID;

-- Per-contact MAM backfill cursor (XEP-0313), tracked independently of
-- which archive items actually got a messages row. syncArchiveForContact
-- used to derive its "after" cursor purely from MAX(messages.delay) among
-- stored rows - but an archive item this device can never decrypt (e.g.
-- ErrOwnDeviceKeyMissing: another of our devices sent it without a key for
-- this one) never gets a row, so that cursor never moved past it, and every
-- future sync - now running on every reconnect, not just app restart -
-- re-fetched and re-attempted the same permanently-undecryptable backlog
-- from scratch, forever.
-- lastSentAt is the wall-clock time of the archive item archiveID points at,
-- recorded here rather than looked up from messages when needed: the cursor
-- is deliberately allowed to sit on an item that never got a messages row
-- (see above), so resolving it through that table is guaranteed to fail for
-- exactly the cursors most likely to need recovering. A server that stops
-- resolving a still-valid archiveID (observed live: an empty page forever,
-- no <item-not-found/>) is then recoverable via an XEP-0313 <start> filter
-- from this timestamp. 0 means unknown - a cursor persisted before this
-- column existed.
CREATE TABLE IF NOT EXISTS mamSyncCursor (
	accountJID TEXT    NOT NULL,
	rosterJID  TEXT    NOT NULL,
	archiveID  TEXT    NOT NULL,
	lastSentAt INTEGER NOT NULL DEFAULT 0,

	PRIMARY KEY (accountJID, rosterJID)
) WITHOUT ROWID;

-- Sends attempted while offline (a plain message, reaction, retraction,
-- correction, or staged-attachment upload+send), held here until the
-- account reconnects and adapter.flushOutbox replays them in insertion
-- order, deleting each row only once it's actually been attempted. A
-- crash/restart while offline must not silently lose these - that's the
-- whole point of this table over the in-memory slice it replaced; a queued
-- send now survives exactly as durably as a message that already went out.
CREATE TABLE IF NOT EXISTS outbox (
	id               INTEGER PRIMARY KEY NOT NULL,
	accountJID       TEXT    NOT NULL,
	localID          TEXT    NOT NULL, -- correlates with the in-memory ui.Message.LocalID a live UI may be showing Pending, so flushOutbox's outcome can be reported back to the right placeholder via MessageSendResolvedMsg
	toAttr           TEXT    NOT NULL,
	body             TEXT    NOT NULL,
	encrypted        BOOLEAN NOT NULL DEFAULT FALSE, -- whether body is AES-sealed at rest (crypto/localstore), same convention as chatDraft.encrypted
	filePath         TEXT, -- non-empty for a staged attachment (see queuedSend.filePath): body is the caption text, replayed via upload-then-send instead of a plain send
	replaceID        TEXT,
	replyToID        TEXT,
	quotedAuthor     TEXT,
	quotedBody       TEXT,
	retractID        TEXT,
	reactionTargetID TEXT,
	reactions        TEXT, -- newline-separated emoji, same convention as oobURLs below
	oobURLs          TEXT,
	createdAt        INTEGER NOT NULL DEFAULT (CAST(strftime('%s', 'now') AS INTEGER)), -- shown as the pending/failed message's SentAt when a plain send is surfaced in chat history before it's actually gone out; id alone (monotonic) is enough to preserve replay order, this is purely for display
	failed           BOOLEAN NOT NULL DEFAULT FALSE, -- a real (non-offline) send failure, e.g. "omemo not ready" - kept as a durable record (shown Failed, not Pending) rather than deleted, so it survives a TUI/daemon restart same as everything else here; never retried by flushOutbox (see ListPendingOutboxByAccount)
	errorText        TEXT -- the error adapter.send returned, set only when failed = TRUE
);

-- Serves ListOutboxByAccount/ListPendingOutboxByAccount's per-account scans
-- in insertion order (id is monotonic, so it alone preserves replay order
-- across a restart).
CREATE INDEX IF NOT EXISTS outboxAccountJID ON outbox (accountJID, id);

-- Local-only unsent compose-box text per chat, never synced to the network.
-- A row only exists while a chat has unsent draft text; the row is deleted
-- (not just emptied) once the draft is sent or explicitly cleared, so this
-- table stays proportional to "chats with something unsent" rather than
-- "chats ever opened".
CREATE TABLE IF NOT EXISTS chatDraft (
	accountJID TEXT NOT NULL,
	rosterJID  TEXT NOT NULL,
	body       TEXT NOT NULL,
	encrypted  BOOLEAN NOT NULL DEFAULT FALSE, -- whether body is AES-sealed (crypto/localstore) or plain text, same as messages.encrypted

	PRIMARY KEY (accountJID, rosterJID)
) WITHOUT ROWID;

-- OMEMO storage, backing crypto/omemo's omemo.Store. Every table below is
-- keyed additionally by protocol ("v1" | "v2") since an account runs a
-- separate identity/device pool per OMEMO protocol version - ProtocolV1
-- (legacy eu.siacs.conversations.axolotl) and ProtocolV2 (XEP-0384) never
-- share state, even though their remote peer device IDs could otherwise
-- collide by chance. One row per (account, protocol) for our own identity
-- keypair + device id.
CREATE TABLE IF NOT EXISTS omemoIdentity (
	accountJID TEXT    NOT NULL,
	protocol   TEXT    NOT NULL, -- "v1" | "v2"
	privateKey BLOB    NOT NULL, -- ed25519 (v2) or curve25519 (v1) private key
	deviceID   INTEGER NOT NULL,

	PRIMARY KEY (accountJID, protocol)
) WITHOUT ROWID;

-- Our own signed prekey(s): the current one, and (while rotating) the
-- previous one kept around so in-flight sessions built against it still
-- decrypt.
CREATE TABLE IF NOT EXISTS omemoSignedPreKey (
	accountJID TEXT    NOT NULL,
	protocol   TEXT    NOT NULL,
	id         INTEGER NOT NULL,
	public     BLOB    NOT NULL,
	private    BLOB    NOT NULL,
	signature  BLOB    NOT NULL,
	stale      BOOLEAN NOT NULL DEFAULT FALSE,

	PRIMARY KEY (accountJID, protocol, id)
);

-- Our own one-time prekey pool.
CREATE TABLE IF NOT EXISTS omemoPreKey (
	accountJID TEXT    NOT NULL,
	protocol   TEXT    NOT NULL,
	id         INTEGER NOT NULL,
	public     BLOB    NOT NULL,
	private    BLOB    NOT NULL,

	PRIMARY KEY (accountJID, protocol, id)
);

-- Monotonic prekey id counter per (account, protocol) — ids must never
-- repeat, even once consumed.
CREATE TABLE IF NOT EXISTS omemoNextPreKeyID (
	accountJID TEXT    NOT NULL,
	protocol   TEXT    NOT NULL,
	nextID     INTEGER NOT NULL,

	PRIMARY KEY (accountJID, protocol)
) WITHOUT ROWID;

-- Double Ratchet session state per (account, protocol, peer device).
CREATE TABLE IF NOT EXISTS omemoSession (
	accountJID TEXT    NOT NULL,
	protocol   TEXT    NOT NULL,
	peerJID    TEXT    NOT NULL,
	deviceID   INTEGER NOT NULL,
	data       BLOB    NOT NULL,

	PRIMARY KEY (accountJID, protocol, peerJID, deviceID)
);

-- Trust decision per (account, protocol, peer identity key).
CREATE TABLE IF NOT EXISTS omemoTrust (
	accountJID  TEXT    NOT NULL,
	protocol    TEXT    NOT NULL,
	identityKey BLOB    NOT NULL,
	state       INTEGER NOT NULL,

	PRIMARY KEY (accountJID, protocol, identityKey)
);

-- Cached device list per (account, protocol, peer jid).
CREATE TABLE IF NOT EXISTS omemoDevice (
	accountJID TEXT    NOT NULL,
	protocol   TEXT    NOT NULL,
	peerJID    TEXT    NOT NULL,
	deviceID   INTEGER NOT NULL,

	PRIMARY KEY (accountJID, protocol, peerJID, deviceID)
);

-- Cached remote identity key per (account, protocol, peer device) — the key
-- that device's bundle was last seen publishing.
CREATE TABLE IF NOT EXISTS omemoRemoteIdentity (
	accountJID  TEXT    NOT NULL,
	protocol    TEXT    NOT NULL,
	peerJID     TEXT    NOT NULL,
	deviceID    INTEGER NOT NULL,
	identityKey BLOB    NOT NULL,

	PRIMARY KEY (accountJID, protocol, peerJID, deviceID)
);

-- Cached per-peer OMEMO protocol version negotiation result (auto-detection
-- fallback for legacy stored chat modes): which protocol ("v1" | "v2") to
-- use for a given (account, peer), and when it was last (re-)probed via disco#info/PEP
-- - so account.go's negotiation logic doesn't re-probe on every send. A
-- manual config.toml omemo_peers override always takes precedence over this
-- cache and is never stored here.
CREATE TABLE IF NOT EXISTS omemoPeerProtocol (
	accountJID TEXT    NOT NULL,
	peerJID    TEXT    NOT NULL,
	protocol   TEXT    NOT NULL, -- "v1" | "v2"
	probedAt   INTEGER NOT NULL, -- unix seconds

	PRIMARY KEY (accountJID, peerJID)
) WITHOUT ROWID;


-- Roster storage

CREATE TABLE IF NOT EXISTS rosterVer (
	accountJID TEXT NOT NULL PRIMARY KEY,
	ver        TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS rosterJIDs (
	accountJID TEXT NOT NULL,
	jid        TEXT NOT NULL,
	name       TEXT NOT NULL DEFAULT '',
	subs       TEXT NOT NULL,

	PRIMARY KEY (accountJID, jid)
) WITHOUT ROWID;


CREATE TABLE IF NOT EXISTS rosterGroups (
	id         INTEGER PRIMARY KEY NOT NULL,
	accountJID TEXT                NOT NULL,
	jid        TEXT                NOT NULL,
	name       TEXT                NOT NULL,

	FOREIGN KEY (accountJID, jid) REFERENCES rosterJIDs(accountJID, jid) ON DELETE CASCADE,
	UNIQUE (accountJID, jid, name)
);


-- Service Discovery (disco) and Entity Capabilities (caps) — shared across
-- accounts: these cache what a given external JID advertises, which doesn't
-- vary per local account asking about it.

CREATE TABLE IF NOT EXISTS entityCaps (
	id   INTEGER  PRIMARY KEY NOT NULL,
	hash TEXT                 NOT NULL,
	ver  TEXT                 NOT NULL,

	UNIQUE (hash, ver)
);

CREATE TABLE IF NOT EXISTS discoFeatures (
	id  INTEGER  PRIMARY KEY NOT NULL,
	var TEXT                 NOT NULL,

	UNIQUE (var)
);

CREATE TABLE IF NOT EXISTS discoIdentity (
	id   INTEGER  PRIMARY KEY NOT NULL,
	cat  TEXT                 NOT NULL,
	name TEXT                 NOT NULL,
	typ  TEXT                 NOT NULL,
	lang TEXT                 NOT NULL,

	UNIQUE (cat, name, typ, lang)
);

CREATE TABLE IF NOT EXISTS discoJID  (
	id   INTEGER  PRIMARY KEY NOT NULL,
	jid  TEXT                 NOT NULL,
	caps INTEGER              NOT NULL,

	-- We save forms a bit differently since we don't actually use them right now
	-- except in caps calculations. Instead of saving each individual form and
	-- field, just dump all the forms associated with a JID as an XML blob that
	-- can easily be parsed out into a forms list again later.
	forms TEXT,

	FOREIGN KEY (caps) REFERENCES entityCaps(id) ON DELETE CASCADE,
	UNIQUE (jid)
);

CREATE TABLE IF NOT EXISTS discoFeatureJID (
	id   INTEGER  PRIMARY KEY NOT NULL,
	jid  INTEGER              NOT NULL,
	feat INTEGER              NOT NULL,

	FOREIGN KEY (jid)  REFERENCES discoJID(id)      ON DELETE CASCADE,
	FOREIGN KEY (feat) REFERENCES discoFeatures(id) ON DELETE CASCADE,
	UNIQUE (jid, feat)
);

CREATE TABLE IF NOT EXISTS discoIdentityJID (
	id    INTEGER  PRIMARY KEY NOT NULL,
	jid   INTEGER              NOT NULL,
	ident INTEGER              NOT NULL,

	FOREIGN KEY (jid)   REFERENCES discoJID(id)      ON DELETE CASCADE,
	FOREIGN KEY (ident) REFERENCES discoIdentity(id) ON DELETE CASCADE,
	UNIQUE (jid, ident)
)
