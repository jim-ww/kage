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
	originID      TEXT,
	stanzaType    TEXT     NOT NULL DEFAULT 'normal', -- RFC 6121 § 5.2.2
	received      BOOLEAN  NOT NULL DEFAULT FALSE,
	delay         INTEGER  NOT NULL DEFAULT (CAST(strftime('%s', 'now') AS INTEGER)),
	rosterJID     TEXT,
	archiveID     TEXT, -- XEP-0313 (MAM) server-assigned archive id, used as the RSM "after" cursor to resume backfill
	replyToIdAttr TEXT, -- XEP-0461: idAttr of the message this one replies to
	retracted     BOOLEAN  NOT NULL DEFAULT FALSE, -- XEP-0424: sender attempted to retract this; content is kept, just flagged

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

-- XEP-0444: each row is one reactor's one emoji on one message. A reactor's
-- full set is replaced (not added-to) whenever a new <reactions/> stanza
-- arrives for them, by deleting their existing rows for that idAttr first.
CREATE TABLE IF NOT EXISTS messageReactions (
	id         INTEGER  PRIMARY KEY NOT NULL,
	accountJID TEXT     NOT NULL,
	idAttr     TEXT     NOT NULL, -- idAttr of the message being reacted to
	fromJID    TEXT     NOT NULL, -- bare JID of the reactor ("me" for our own account)
	emoji      TEXT     NOT NULL,

	UNIQUE (accountJID, idAttr, fromJID, emoji)
);

-- XEP-0373: caches a peer's OpenPGP key fingerprint, discovered from their
-- PEP node, so we don't have to query it again on every send.
CREATE TABLE IF NOT EXISTS pgpPeerKeys (
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

-- OMEMO (XEP-0384) storage, backing crypto/omemo's omemo.Store. One row per
-- account for our own identity keypair + device id.
CREATE TABLE IF NOT EXISTS omemoIdentity (
	accountJID TEXT   NOT NULL PRIMARY KEY,
	privateKey BLOB   NOT NULL, -- ed25519 private key
	deviceID   INTEGER NOT NULL
) WITHOUT ROWID;

-- Our own signed prekey(s): the current one, and (while rotating) the
-- previous one kept around so in-flight sessions built against it still
-- decrypt.
CREATE TABLE IF NOT EXISTS omemoSignedPreKey (
	accountJID TEXT    NOT NULL,
	id         INTEGER NOT NULL,
	public     BLOB    NOT NULL,
	private    BLOB    NOT NULL,
	signature  BLOB    NOT NULL,
	stale      BOOLEAN NOT NULL DEFAULT FALSE,

	PRIMARY KEY (accountJID, id)
);

-- Our own one-time prekey pool.
CREATE TABLE IF NOT EXISTS omemoPreKey (
	accountJID TEXT    NOT NULL,
	id         INTEGER NOT NULL,
	public     BLOB    NOT NULL,
	private    BLOB    NOT NULL,

	PRIMARY KEY (accountJID, id)
);

-- Monotonic prekey id counter per account — ids must never repeat, even
-- once consumed.
CREATE TABLE IF NOT EXISTS omemoNextPreKeyID (
	accountJID TEXT    NOT NULL PRIMARY KEY,
	nextID     INTEGER NOT NULL
) WITHOUT ROWID;

-- Double Ratchet session state per (account, peer device).
CREATE TABLE IF NOT EXISTS omemoSession (
	accountJID TEXT    NOT NULL,
	peerJID    TEXT    NOT NULL,
	deviceID   INTEGER NOT NULL,
	data       BLOB    NOT NULL,

	PRIMARY KEY (accountJID, peerJID, deviceID)
);

-- Trust decision per (account, peer identity key).
CREATE TABLE IF NOT EXISTS omemoTrust (
	accountJID  TEXT    NOT NULL,
	identityKey BLOB    NOT NULL,
	state       INTEGER NOT NULL,

	PRIMARY KEY (accountJID, identityKey)
);

-- Cached device list per (account, peer jid).
CREATE TABLE IF NOT EXISTS omemoDevice (
	accountJID TEXT    NOT NULL,
	peerJID    TEXT    NOT NULL,
	deviceID   INTEGER NOT NULL,

	PRIMARY KEY (accountJID, peerJID, deviceID)
);

-- Cached remote identity key per (account, peer device) — the key that
-- device's bundle was last seen publishing.
CREATE TABLE IF NOT EXISTS omemoRemoteIdentity (
	accountJID  TEXT    NOT NULL,
	peerJID     TEXT    NOT NULL,
	deviceID    INTEGER NOT NULL,
	identityKey BLOB    NOT NULL,

	PRIMARY KEY (accountJID, peerJID, deviceID)
);


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
