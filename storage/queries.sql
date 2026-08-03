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
	stanzaType,
	originID,
	delay,
	rosterJID,
	archiveID,
	replyToIdAttr
)
VALUES (
	sqlc.arg(account_jid),
	sqlc.arg(sent),
	sqlc.arg(to_attr),
	sqlc.arg(from_attr),
	sqlc.arg(id_attr),
	sqlc.arg(body),
	sqlc.arg(encrypted),
	sqlc.arg(stanza_type),
	sqlc.arg(origin_id),
	IFNULL(
		NULLIF(sqlc.arg(delay), 0),
		CAST(strftime('%s', 'now') AS INTEGER)
	),
	sqlc.arg(roster_jid),
	sqlc.arg(archive_id),
	sqlc.arg(reply_to_id_attr)
)
ON CONFLICT (accountJID, originID, fromAttr) DO UPDATE
SET archiveID = excluded.archiveID
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
	encrypted = sqlc.arg(encrypted)
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);


-- name: MarkMessageRetracted :execrows
UPDATE messages
SET retracted = TRUE
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);


-- name: DeleteMessageByID :execrows
DELETE FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND rosterJID = sqlc.arg(roster_jid);


-- name: ListMessagesByRoster :many
SELECT
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	encrypted,
	stanzaType,
	delay,
	replyToIdAttr,
	retracted
FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid)
	AND stanzaType = COALESCE(
		NULLIF(sqlc.arg(stanza_type), ''),
		stanzaType
	)
ORDER BY delay ASC;


-- name: DeleteReactionsByReactor :exec
DELETE FROM messageReactions
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr)
	AND fromJID = sqlc.arg(from_jid);


-- name: InsertReaction :exec
INSERT INTO messageReactions (accountJID, idAttr, fromJID, emoji)
VALUES (sqlc.arg(account_jid), sqlc.arg(id_attr), sqlc.arg(from_jid), sqlc.arg(emoji))
ON CONFLICT (accountJID, idAttr, fromJID, emoji) DO NOTHING;


-- name: ListReactionsForMessage :many
SELECT fromJID, emoji
FROM messageReactions
WHERE accountJID = sqlc.arg(account_jid)
	AND idAttr = sqlc.arg(id_attr);


-- name: ListLatestArchiveIDs :many
SELECT
	m.rosterJID,
	m.archiveID,
	MAX(m.delay)
FROM messages AS m
WHERE m.accountJID = sqlc.arg(account_jid)
	AND m.archiveID IS NOT NULL
GROUP BY m.rosterJID;


-- name: ListEarliestArchiveIDs :many
SELECT
	archiveID,
	MIN(delay) AS mindelay
FROM messages
WHERE accountJID = sqlc.arg(account_jid)
	AND rosterJID = sqlc.arg(roster_jid)
GROUP BY delay
HAVING mindelay NOT NULL;


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


-- name: GetLocalKeySalt :one
SELECT salt
FROM localKeySalt
WHERE id = 0;

-- name: SetLocalKeySalt :exec
INSERT INTO localKeySalt (id, salt)
VALUES (FALSE, ?)
ON CONFLICT(id) DO UPDATE
SET salt = excluded.salt;
