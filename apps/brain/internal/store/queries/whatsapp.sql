-- Every live team's WhatsApp channel has an account row, so a role can
-- lease it. A channel deleted takes its row with it.
-- name: EnsureWhatsAppAccounts :exec
INSERT INTO whatsapp_accounts (channel_id)
SELECT c.id FROM channels c
JOIN teams t ON t.id = c.team_id AND t.deleted_at IS NULL
WHERE c.provider = 'whatsapp'
ON CONFLICT (channel_id) DO NOTHING;

-- A role takes the accounts nobody holds, or whose holder stopped renewing,
-- and renews its own. Two roles at once each wait on the row's lock, and the
-- second finds it fresh and held. A deleted team's are not taken: its
-- channels take no messages.
-- name: LeaseWhatsAppAccounts :many
UPDATE whatsapp_accounts a
SET held_by = sqlc.arg('holder')::text, heartbeat_at = now()
FROM channels c
JOIN teams t ON t.id = c.team_id AND t.deleted_at IS NULL
WHERE a.channel_id = c.id
  AND (a.held_by = sqlc.arg('holder')::text OR a.held_by IS NULL
       OR a.heartbeat_at < now() - make_interval(secs => sqlc.arg('stale_seconds')::float8))
RETURNING a.channel_id, c.team_id, a.state, a.jid, a.pair_seq, a.pair_seen, a.pair_method, a.pair_phone;

-- A role stopping lets its accounts go, so another takes them at once.
-- name: ReleaseWhatsAppAccounts :exec
UPDATE whatsapp_accounts SET held_by = NULL, heartbeat_at = NULL
WHERE held_by = sqlc.arg('holder')::text;

-- What a pairing shows: the newest QR or code, or why it stopped. Only the
-- holder writes, so a role that lost the lease cannot overwrite another's.
-- name: SetWhatsAppPairing :execrows
UPDATE whatsapp_accounts
SET state = 'pairing', pair_seen = greatest(pair_seen, sqlc.arg('seq')::bigint),
    pair_qr = sqlc.narg('qr'), pair_code = sqlc.narg('code'), pair_error = NULL, updated_at = now()
WHERE channel_id = sqlc.arg('channel_id') AND held_by = sqlc.arg('holder')::text;

-- name: SetWhatsAppLinked :execrows
UPDATE whatsapp_accounts
SET state = 'connected', jid = sqlc.arg('jid'), phone = sqlc.arg('phone'),
    pair_method = NULL, pair_phone = NULL, pair_qr = NULL, pair_code = NULL, pair_error = NULL,
    updated_at = now()
WHERE channel_id = sqlc.arg('channel_id') AND held_by = sqlc.arg('holder')::text;

-- The device is no longer linked, or never was: why, for the person to read.
-- name: SetWhatsAppUnpaired :execrows
UPDATE whatsapp_accounts
SET state = 'unpaired', jid = NULL, phone = NULL,
    pair_method = NULL, pair_phone = NULL, pair_qr = NULL, pair_code = NULL,
    pair_error = sqlc.narg('error'), updated_at = now()
WHERE channel_id = sqlc.arg('channel_id') AND held_by = sqlc.arg('holder')::text;

-- Every device an account names, so one no account names can be let go.
-- name: ListWhatsAppJIDs :many
SELECT jid::text FROM whatsapp_accounts WHERE jid IS NOT NULL;

-- name: WhatsAppPartSent :one
SELECT message_id FROM whatsapp_sent WHERE channel_id = $1 AND part_id = $2;

-- name: RecordWhatsAppPart :exec
INSERT INTO whatsapp_sent (channel_id, part_id, message_id) VALUES ($1, $2, $3)
ON CONFLICT (channel_id, part_id) DO NOTHING;

-- name: PruneWhatsAppSent :exec
DELETE FROM whatsapp_sent WHERE sent_at < now() - interval '1 day';

-- Asking for a pairing: a new request (pair_seq moves on), and what showed
-- before cleared. The role holding the channel takes it at its next renewal.
-- The row is made here if no role has made it yet, so asking works before
-- any role runs.
-- name: RequestWhatsAppPairing :exec
INSERT INTO whatsapp_accounts (channel_id, state, pair_seq, pair_method, pair_phone)
VALUES (sqlc.arg('channel_id'), 'pairing', 1, sqlc.arg('method'), sqlc.narg('phone'))
ON CONFLICT (channel_id) DO UPDATE
SET state = 'pairing', pair_seq = whatsapp_accounts.pair_seq + 1,
    pair_method = EXCLUDED.pair_method, pair_phone = EXCLUDED.pair_phone,
    pair_qr = NULL, pair_code = NULL, pair_error = NULL, updated_at = now();

-- What a person sees of a channel's number: linked or not, the pairing's QR
-- or code, why it stopped, and whether any role holds the channel now.
-- name: GetWhatsAppAccount :one
SELECT state, phone, pair_method, pair_qr, pair_code, pair_error, updated_at,
       coalesce(heartbeat_at > now() - make_interval(secs => sqlc.arg('stale_seconds')::float8), false)::bool AS held
FROM whatsapp_accounts WHERE channel_id = sqlc.arg('channel_id');
