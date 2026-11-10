---
title: Channels
---

A channel is a team's connection to one place messages come from. A message
that arrives on it is stored in a conversation, and, when it is addressed to
the team and the sender has been let in, it starts a run of the team's
[hub](hub.md). The hub's answer is posted back to the channel.

Each channel belongs to one team and one provider. There are two:
[`telegram`](#telegram), a Telegram bot, and `custom`, a generic signed webhook
for anything else. Slack and Discord adapters are not written.

## Connecting a channel

```sh
oa channels create platform support --provider custom \
  --reply-url https://support.example.com/openarity/replies
```

Needs `channel:write` in the team. The name is unique in the team, ignoring
case. The brain generates the signing secret and prints it once; it is never
shown again, and it is kept in the secret store rather than in Postgres. If the
provider issues its own secret, pipe it in with `--secret-stdin`. `--reply-url`
is optional; see [Replies](#replies).

The dashboard does the same under **Channels → Connect channel**.

## Telegram

Make a bot with [@BotFather](https://t.me/BotFather) (`/newbot`) and copy the
token it gives you. The token is a credential, so it goes in on stdin, never as
a flag:

```sh
printf %s "$BOT_TOKEN" |
  oa channels create platform bot --provider telegram --credential-stdin bot_token
```

In the dashboard, choose **Telegram** under **Connect channel** and paste the
token.

Before the channel exists, the brain asks Telegram which bot the token belongs
to (`getMe`). A wrong token is refused at once, and the bot's id and username
are filled in for you. The token, the bot's identity and a generated secret are
kept together in the secret store.

The brain then points the bot at the channel (`setWebhook`), so Telegram posts
each new message to the channel's webhook with that secret. Telegram posts
only to `https`, so this needs the brain's public webhook address
(`OPENARITY_WEBHOOK_PUBLIC_URL`) to be an `https` one. Without it, the channel is
made and `create` prints the `setWebhook` command to run yourself. If Telegram
refuses, or cannot be reached, the channel is not made and you see why. Deleting the
channel lets the bot go (`deleteWebhook`).

A Telegram message is answered:

- **in a private chat** with the bot, always;
- **in a group**, only when it names the bot (`@yourbot`), sends it a command
  (`/status@yourbot`), or replies to one of its messages. A forum topic is a
  conversation of its own.

Edits, channel posts and button presses are not read.

A new bot has **privacy mode** on, and then Telegram sends it, in a group,
only commands to it and replies to it: a message that just names
`@yourbot` never arrives. When you connect a bot with privacy mode on, the
brain says so (in the dashboard, in `oa channels create`, and as `warnings` in
the response). To fix it, send `/setprivacy` to @BotFather, choose the bot,
pick **Disable**, then remove the bot from each group and add it back.
Telegram applies the change only to groups the bot joins afterwards.

With privacy mode off the bot is sent everything said in a group, but only
someone who asks it (by name, command or reply) is queued for approval, so a
busy group does not fill the queue.

The answer goes back into the same chat, quoting the message it answers, as a
Telegram rich message, so headings, lists, tables and code read as written. An
answer longer than one message holds (32768 characters or 500 blocks) is split
between blocks into several; only the first quotes. Images in an answer are
sent as links and raw HTML is shown as text, so an answer cannot make Telegram
fetch an address on its own. A Telegram channel takes no reply URL.

## Sending a message in

The webhook listener (`OPENARITY_WEBHOOK_BIND`) takes a `POST` at the channel's
webhook URL: `/hooks/custom/<channel id>`, under the brain's public webhook
address (`OPENARITY_WEBHOOK_PUBLIC_URL`). `oa channels list -o json` and the channel's
sheet in the dashboard show the address; with no public address set, the
dashboard shows only the path. The channel id is a routing key, not a secret. What proves a
message is its signature.

Two headers go with the body:

| Header | Value |
| ------------------------ | ------------------------------------------------ |
| `X-Openarity-Timestamp`  | Unix time in seconds, when the message was signed |
| `X-Openarity-Signature`  | `v1=` and the hex HMAC-SHA256 of `v1:<timestamp>:<body>`, keyed by the signing secret |

The signature covers the raw bytes of the body, so sign exactly what you send.
A timestamp more than five minutes from the brain's clock, either way, is
refused. The body is JSON:

```json
{
  "id": "ticket-42-msg-7",
  "text": "Reconcile payment REF-9 against our open invoices.",
  "sent_at": "2026-11-08T09:30:00Z",
  "author": { "ref": "u-17", "display_name": "Erik Lund", "is_bot": false },
  "session": { "ref": "ticket-42", "kind": "direct" },
  "mentions": [{ "sender_ref": "bot", "display_name": "Openarity", "is_us": true }],
  "reply_to": "",
  "attachments": []
}
```

`id` must be unique within the channel: a repeat is dropped as a replay.
`session.ref` names the conversation, and `session.kind` is `direct`, `group`
or `thread`. `sent_at` defaults to the time of delivery. An attachment is
`{ "id", "filename", "media_type", "content_base64" }`.

The listener answers 200 for a message it delivered and for one it dropped on
purpose, such as one from a sender nobody has let in, since a retry would bring
the same thing back. A bad signature, an unknown or deleted channel and a
channel whose team is deleted all answer 403, with nothing to tell them apart.
A body over the size limit is 413. A failure on the brain's side is 500 or 503,
and the sender should retry.

## Letting senders in

The first time someone asks the bot something on a channel, the brain does
not know who they are. It records the sender's id and display name in a queue
of up to 50 per channel and drops the message: nothing they wrote is stored, and nothing
reaches an agent. Their display name is text they chose, so it is shown as
data.

On a Telegram channel the sender is told so, in the chat: "I can't answer you
yet: someone on the team has to let you in first. I'll tell you here when they
have." It is said once, the first time they ask the bot (in a private chat,
always; in a group, by naming it, replying to it or sending it a command), and
quotes their message. In a group it tags them by name, so nobody else takes it
as theirs. Their message is still not stored.

A person with the right to approve binds the sender to a member of the team.
From then on the sender's messages are theirs, and run as that member.

```sh
oa channels senders pending platform support
oa channels senders approve platform support u-17 erik@example.com
oa channels senders list platform support
oa channels senders remove platform support u-17
```

When they are let in, the bot says so in the same place and asks them to send
their message again, since the first was never kept. A sender who was never
told they were waiting is not told they are in. Both messages are retried like
a reply, and both are off with `oa channels update <team> <channel>
--no-notify-strangers`, or **Waiting senders** in the channel's sheet. Needs
`channel:write`. `custom` channels send neither.

In the dashboard this is **Approvals**: the people waiting across every
channel, the longest first, each with a **Let in** action, and a **Let in** tab
for who is already bound. The sidebar shows how many are waiting. There is no
reject. `remove` takes a binding away or dismisses a waiting sender, and is not
a block: their next message queues them again.

Who may do what:

- **See the queue and the approved list**: `channel:read`.
- **Approve or remove a sender**: `sender:approve`. The `admin` and `approver`
  roles hold it; `member` does not.
- **Connect, change or disconnect a channel**: `channel:write`. Only `admin`
  holds it, so an approver can let people in without rewiring a channel or
  touching its secret.

The person approving must also hold every permission of the member they bind
the sender to, and of whoever the sender is bound to now. An approver can let a
stranger speak as a member or as another approver, never as an admin.

## When a message is answered

A message starts a run only when it is addressed to the team:

| Conversation | Answered when |
| ------------ | ------------- |
| `direct`     | Always |
| `group`, `thread` | The message mentions the bot (`is_us` on a mention; for Telegram, see [above](#telegram)) |

A message from a bot, ours or anyone's, is never answered. A message with no
text is not answered. Neither is one from a sender whose member may not start
runs (`run:write`), or when the team has no model set, so its hub cannot run.
These are logged on the brain; nothing is sent to the channel.

An answered message goes to the team's hub, with the sender as the run's
user. If the conversation already has a run going, the message does not start a
second one: it is added to the running run as steering, in the form
`Display name: text`, and the run reads it between its steps.
Otherwise it starts a new run, and the run is in the conversation's list of
runs like any other.

A message that arrives after the run has made its last request to the model is
too late for it. It starts a follow-up run when the first finishes, so it gets
an answer of its own. Each reply quotes the newest message its own run read.

## Replies

When a run started from a channel finishes, its answer is written to an outbox
and sent to the channel: for `custom`, posted to its reply URL, as below; for
Telegram, into the chat. A run that failed, or ended with nothing
to say, sends a short apology instead. The apology never carries the error.

Set the address when connecting, or later:

```sh
oa channels update platform support --reply-url https://support.example.com/openarity/replies
oa channels update platform support --no-reply-url
```

`--no-reply-url` stops replies, and gives up any that were still waiting. In
the dashboard, open the channel and use **Set where replies go**; the sheet
shows it as **Replies to**. Both need `channel:write`.

The address must be `http` or `https` with a host, and carry no credentials,
query or fragment, because every member can read it. A literal private,
loopback or link-local address is refused when it is set, and a name that
resolves to one is refused when the reply is sent. A reply follows no redirect.

`can_reply` on a channel says whether its provider sends replies at all.
`takes_reply_url` says whether it sends them to a reply URL: true for `custom`,
false for `telegram`, which answers in the chat. A reply URL is refused on a
channel that takes none.

### What a reply looks like

A `POST` with `Content-Type: application/json`:

```json
{
  "id": "0192f5c4-7d1e-7c3a-9b2e-4f6a8d0c1e23",
  "session": { "ref": "ticket-42", "kind": "direct" },
  "in_reply_to": "ticket-42-msg-7",
  "kind": "answer",
  "text": "Payment REF-9 matches invoice INV-310."
}
```

`id` is the id of the run. `kind` is `answer` or `apology`. `in_reply_to` is the
`id` of the incoming message it answers, and is left out when there is none.

It carries the same two headers as a message, signed under a different prefix:
`X-Openarity-Signature` is `v1=` and the hex HMAC-SHA256 of
`reply:v1:<timestamp>:<body>`. The prefix keeps a captured reply from verifying
as a message if it is posted back to the webhook.

### Verifying a reply

Check the reply before you act on it. The signature is the only thing that
proves it came from the brain.

1. Read the raw body, before parsing it.
2. Refuse a timestamp more than five minutes from your clock, either way.
3. Compute the signature over `reply:v1:<timestamp>:<body>` and compare it with
   the header in constant time.
4. Drop a reply whose `id` you have already handled.

```python
import hashlib, hmac, json, time

def verify_reply(secret: bytes, headers, body: bytes, seen: set) -> bool:
    ts = headers["X-Openarity-Timestamp"]
    if abs(time.time() - int(ts)) > 300:
        return False
    mac = hmac.new(secret, f"reply:v1:{ts}:".encode() + body, hashlib.sha256)
    if not hmac.compare_digest("v1=" + mac.hexdigest(), headers["X-Openarity-Signature"]):
        return False
    reply_id = json.loads(body)["id"]
    if reply_id in seen:
        return True  # already handled; answer 2xx so it is not sent again
    seen.add(reply_id)
    return True
```

Answer with any `2xx` once you have the reply. In production keep `seen` in a
store, not in memory.

### Retries and order

Any other answer, a connection that fails, or a signing secret that cannot be
read counts as a failed attempt. A failed reply is tried again after 1, 5 and
30 minutes, then hourly, six attempts in all, which is about two and a half
hours. Each attempt is signed afresh, so its timestamp is current. After the
last, the reply is given up. A reply that cannot be built at all, such as one
for a channel with no reply URL, is given up at once.

Replies in one conversation are sent in order: a reply waiting to be retried
holds back the later ones in the same conversation, and nothing else. Delivery
is at least once. If your server took a reply and the answer was lost, the same
reply arrives again with the same `id`, which is why step 4 above exists.

Telegram is retried the same way. An answer sent in several messages records
each one as it goes, so a retry resumes at the first message not sent.

### What the dashboard and the API show

A run started from a channel carries a `reply` with its `kind`, `status`,
`attempts`, `last_error`, `sent_at` and `next_attempt_at`. It is null for a run
that answers no channel, such as one started from the dashboard, or a delegate.

| `status`  | Meaning |
| --------- | ------- |
| `pending` | Not yet taken by the channel; `last_error` and `next_attempt_at` say why and when it is tried next |
| `sent`    | The reply URL answered 2xx |
| `failed`  | Given up: the channel has no reply URL, or every attempt failed |

In a conversation, the dashboard puts a line under each answer: that it was
sent, when; that it is being sent; that it has not reached the channel yet and
when it is tried again; or that it was not sent and why. The page keeps reading
while a reply is pending.
