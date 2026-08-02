-- name: TruncateRoster :exec
DELETE FROM rosterJIDs;


-- name: DeleteRosterByJID :exec
DELETE FROM rosterJIDs
WHERE jid = ?;


-- name: UpsertRoster :exec
INSERT INTO rosterJIDs (jid, name, subs)
VALUES (?, ?, ?)
ON CONFLICT(jid) DO UPDATE
SET
	name = excluded.name,
	subs = excluded.subs;


-- name: InsertRosterGroup :exec
INSERT INTO rosterGroups (jid, name)
VALUES (?, ?)
ON CONFLICT DO NOTHING;


-- name: UpsertRosterVersion :exec
INSERT INTO rosterVer (id, ver)
VALUES (FALSE, ?)
ON CONFLICT(id) DO UPDATE
SET ver = excluded.ver;


-- name: GetRosterVersion :one
SELECT ver
FROM rosterVer
WHERE id = 0;


-- name: ListRoster :many
SELECT
	jid,
	name,
	subs
FROM rosterJIDs;


-- name: InsertMessage :one
INSERT INTO messages (
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	stanzaType,
	originID,
	delay,
	rosterJID,
	archiveID
)
VALUES (
	sqlc.arg(sent),
	sqlc.arg(to_attr),
	sqlc.arg(from_attr),
	sqlc.arg(id_attr),
	sqlc.arg(body),
	sqlc.arg(stanza_type),
	sqlc.arg(origin_id),
	IFNULL(
		NULLIF(sqlc.arg(delay), 0),
		CAST(strftime('%s', 'now') AS INTEGER)
	),
	sqlc.arg(roster_jid),
	sqlc.arg(archive_id)
)
ON CONFLICT (originID, fromAttr) DO UPDATE
SET archiveID = excluded.archiveID
RETURNING id;


-- name: MarkMessagesReceived :exec
UPDATE messages
SET received = TRUE
WHERE sent = TRUE
	AND (
		idAttr = sqlc.arg(message_id)
		OR originID = sqlc.arg(message_id)
	);


-- name: ListMessagesByRoster :many
SELECT
	sent,
	toAttr,
	fromAttr,
	idAttr,
	body,
	stanzaType,
	delay
FROM messages
WHERE rosterJID = ?
	AND stanzaType = COALESCE(
		NULLIF(sqlc.arg(stanza_type), ''),
		stanzaType
	)
ORDER BY delay ASC;


-- name: ListLatestArchiveIDs :many
SELECT
	j.jid,
	m.archiveID,
	MAX(m.delay)
FROM messages AS m
INNER JOIN rosterJIDs AS j
	ON m.rosterJID = j.jid
GROUP BY j.jid;


-- name: ListEarliestArchiveIDs :many
SELECT
	archiveID,
	MIN(delay) AS mindelay
FROM messages
WHERE rosterJID = ?
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
