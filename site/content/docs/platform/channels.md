---
title: Channels
---

A channel is a team's connection to one place messages come from. A message
that arrives on it is stored in a conversation, and, when it is addressed to
the team and the sender has been let in, it starts a run of the team's
[hub](hub.md). The hub's answer is posted back to the channel.

Each channel belongs to one team and one provider. There are four:
[`slack`](#slack), a Slack app; [`telegram`](#telegram), a Telegram bot;
[`whatsapp`](whatsapp.md), a WhatsApp number linked as a device; and `custom`,
a generic signed webhook for anything else. A Discord adapter is not written.

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

## Slack

Create a Slack app, give its bot the scopes it needs to read and post in the
places it is added, and install it. Then connect the channel with the app's
**Signing Secret** (Basic Information) and its **Bot User OAuth Token**
(`xoxb-…`). The token goes in on stdin and the secret from a file, as there
is one stdin:

```sh
printf %s "$SLACK_BOT_TOKEN" | oa channels create platform acme \
  --provider slack --credential-stdin bot_token \
  --secret-file <(printf %s "$SLACK_SIGNING_SECRET")
```

Slack posts to two addresses of the channel's webhook URL, and the app must be
told both. Each is verified with the same signing secret. With the channel's
address `<webhook URL>` (`oa channels list -o json`, or the channel's sheet in
the dashboard):

| App setting | Value |
| --- | --- |
| **Event Subscriptions → Request URL** | `<webhook URL>` (`/hooks/slack/<channel id>`) |
| **Interactivity & Shortcuts → Interactivity → Request URL** | `<webhook URL>/interactivity` (`/hooks/slack/<channel id>/interactivity`) |

Turn **Interactivity** on and paste the second address. Without it the app
still works, but an approval's buttons do nothing: Slack has nowhere to send a
tap. A run that needs its person's approval in a Slack thread then shows
**Approve**, **Approve for this run** and **Decline…** under the request.
**Decline…** opens a form with an optional reason. A tap is answered only if the
person who tapped may answer that request, as when they reply in words; anyone
else is told so, privately, and nothing changes. Once the request is decided its
message is edited to the outcome, so an old prompt cannot be tapped again.

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

Edits and channel posts are not read. A button press is read only on the
keyboard of an approval prompt (see [Approving in the chat](#approving-in-the-chat)).

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

A person with the right to approve says who the sender is, in one of two ways:

- **A member of the team.** The sender's messages are that member's, and run
  as them. Right for a colleague who also uses the dashboard.
- **Someone new, with a role.** The brain makes the sender a user of their own,
  who never logs in: they exist only through this channel. Right for a friend
  messaging your own number. `guest` is the usual role: a guest talks to the
  team's agents and changes nothing in the team.

```sh
oa channels senders pending platform support
oa channels senders approve platform support u-17 erik@example.com
oa channels senders approve platform support u-18 --new --name Ravi   # a guest
oa channels senders list platform support
oa channels senders remove platform support u-17
```

An approval can be changed without removing it. On the dashboard, **Edit**
beside a sender under **Let in**; from a terminal:

```sh
oa channels senders update platform support u-18 --role member    # in place
oa channels senders update platform support u-18 --user erik@example.com
oa teams members update platform erik@example.com --role approver
```

Moving a sender to another member is approving them again, with the same
checks. A role, or a new name for someone let in as new, is a change to the
user they speak as: it needs `membership:write`, and the team's last admin
keeps their role. Moving a sender off a user of their own, or removing it,
deletes that user, with anything they own; the dashboard says so first.

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
- **Approve, move or remove a sender**: `sender:approve`. The `admin` and
  `approver` roles hold it; `member` and `guest` do not.
- **Change a role, or a name**: `membership:write`. Only `admin` holds it.
- **Connect, change or disconnect a channel**: `channel:write`. Only `admin`
  holds it, so an approver can let people in without rewiring a channel or
  touching its secret.

The person approving must also hold every permission of the member they bind
the sender to, or of the role they give someone new, and of whoever the sender
is bound to now. An approver can let a stranger speak as a member or as another
approver, or in as a guest or a member, never as an admin.

## When a message is answered

A message starts a run only when it is addressed to the team:

| Conversation | Answered when |
| ------------ | ------------- |
| `direct`     | Always |
| `group`, `thread` | The message mentions the bot (`is_us` on a mention; for Telegram, see [above](#telegram)) |

A channel can narrow that with where it answers (`answers_in`), set when it
is connected or changed later, on the dashboard or with `oa channels update
--answers-in`:

| `answers_in` | Answers |
| ------------ | ------- |
| `everywhere` (the default) | Direct chats, and groups where it is mentioned |
| `groups` | Only groups, where it is mentioned |
| `direct` | Only direct chats |

Outside it a message asks nothing: it is kept when its sender has been let in,
and nobody is queued or told they are waiting. For a WhatsApp number that is
also your own, `groups` with stranger notices off leaves your direct chats
alone; the dashboard sets both when you say the number is yours.

What you type on a linked WhatsApp number is yours, not a question for the
agent, and is not kept, with two exceptions. With a trigger word, a message of
yours that holds the word calls the agent in the chat you typed it in,
WhatsApp's **Message yourself** chat included. With no word, everything you
type in **Message yourself** asks the agent. Either way `answers_in` does not
apply to **Message yourself**. You are let in from Approvals once, like anyone, and the
agent's answers, which arrive as yours too, are never taken as questions.

### A trigger word

A WhatsApp channel can be given a `trigger_word`, such as `@Fduv`, when it is
connected or later. With one, a message asks the agent only when it holds the
word, in any case and as a word of its own: `@fduv, what now?` does, `@Fduvx`
does not. That holds in groups and direct chats alike, and an @-mention of the
number no longer asks anything, so a friend tagging you is not answered by the
agent in your name. On your own number you call it from any chat by typing
the word; the answer goes to that chat. `answers_in` still applies.

```sh
oa channels update <team> <channel> --trigger-word @Fduv
oa channels update <team> <channel> --trigger-word ""   # take it away
```

On the dashboard, choosing **Mine** when you connect a WhatsApp number asks
for the word.

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

### A conversation remembers

A conversation is one transcript. Each run in it carries on from where the last
one stopped: the questions, the answers, and what the tools returned, so a
follow-up such as "what did that page say about Windows?" reads the page the
last run fetched rather than fetching it again. As the transcript grows it is
compacted: older turns are folded into a summary, and recent ones are kept
word for word.

In a group, most messages are not addressed to the bot and start no run. The
next run is shown those that came since the last one finished, at most 20 of
them and 8 KiB, before the message that asked it, each with the sender's name.

To go further back, the run reads the conversation with its `history` tool:
what people wrote and what the bot answered, oldest first, a page at a time
(30 messages unless it asks for more, at most 100). It reads only the
conversation it is in, and only the run answering it has the tool, not an
agent it hands work to. What people wrote is given to it as what they said,
never as instructions.

Each chat is its own conversation: a direct message with the bot, a group, and
each topic of a group that has topics. A run started with `oa runs start` or
`POST /runs` cannot join a channel's conversation; its answer would never reach
the chat.

### The dashboard is a channel

Every team has a **Dashboard** channel, made with the team. A conversation
started on the dashboard is a direct session on it, and what you type there is
a message, stored and answered as a message on any channel is: the hub answers
it, and a message sent while a run works is sent to that run. Only the person a
dashboard conversation is with writes in it. The channel cannot be deleted, and
it takes no reply URL: its answers are read where they are asked.

```sh
curl -X POST "$BRAIN/teams/$TEAM/sessions" -H "Authorization: Bearer $TOKEN" \
  -d '{"text": "Why did the deploy fail last night?", "key": "k-1"}'
curl -X POST "$BRAIN/teams/$TEAM/sessions/$SESSION/messages" -H "Authorization: Bearer $TOKEN" \
  -d '{"text": "Check staging too.", "key": "k-2"}'
```

`key` is yours for each send: a retry with the same key is the same message,
stored once.

### Following a conversation

`GET /teams/{id}/sessions/{sessionID}/events` streams a conversation, on any
channel, as server-sent events: what people said, and what every run in it
did, in one order. The events are `sdk/stream`'s, with AG-UI's names. Each
frame's `id` is its place in the conversation; reconnect with it as
`Last-Event-ID` and nothing is missed. A message that asks is `pending` until a
run takes it, which `message_consumed` says.

A run's writing arrives as it happens, in two kinds. `text_message_chunk` is
what it says; `reasoning_message_chunk` is what it thinks: the model's own
reasoning, when its provider sends it, and a loop's plan or critique. Each
names the model call it came from in `message_id`. Show the reasoning folded
away, the text as it arrives, and `run_finished`'s output as the answer once the
run ends. A run another run handed work to reports through its parent, so its
writing is not streamed. A file a run hands the person is `file_sent`, naming
the file as a message's file is; read its bytes from the conversation's
attachment route.

After upgrading, run `brain backfill session-seq` once to number what
conversations held before, so their streams replay from the start. Until then
a conversation's stream starts at what was said after the upgrade.

## Approving in the chat

When an agent asks before a call (see the team's **Tool approvals** settings),
the run waits and the conversation it came from is told, in the agent's own
words, with lines from the brain under them for each request: the tool, how to
answer and how long it waits, then the call's own arguments (its first fields,
cut short). The agent's words can misdescribe the call; the arguments are what
it will run with.

- **The person the run works for, when they may approve their own** (a
  `member`, `approver` or `admin`), is asked right there: buttons in Slack
  (**Approve**, **Approve for this run**, **Decline…**, which asks for an
  optional reason), a keyboard in Telegram, a poll on WhatsApp. Typing
  `yes 7K2F` or `no 7K2F` works on every channel, in a group too without
  mentioning the bot (where the channel answers in groups at all). Only a code
  decides: a bare `yes` or `no` while a request waits decides nothing and is
  answered with the code to reply with, since it may be answering the agent's
  own question rather than the request. A code names only the sender's own
  requests, the waiting one first: `yes` and a word that is none of them is
  read as what it says. Another bot's message never answers.
- **Anyone else** (a `guest`, or a request a rule wants someone else to
  answer) is told it went to an approver. No code is shown, and nothing typed
  in the chat decides it: approvers answer on the dashboard's **Tool
  approvals** page or with `oa approvals`.

Only the whole message is an answer: `yes 7K2F` inside a sentence is a
sentence. A code answers only the request it was given for, so an old `yes`
never approves a newer one. A tap from anyone but the person the request is
for is told so privately and changes nothing, and once a request is decided its
buttons are taken away. Once decided, a request stays decided: a poll vote
changed afterwards, or a second tap, changes nothing, and a vote that decides
nothing is not kept at all. An answer, tapped or typed, is never read as a new
message: it does not steer the run, it does not give the request up the way
writing something else does, and it is left out of the conversation the agent
reads back.

A Telegram bot connected before this is pointed at its channel again when the
brain starts, so it is sent the taps on a prompt's keyboard too.

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

`--no-reply-url` stops replies, and gives up any that were still waiting. A
`custom` channel with no reply URL takes messages in only: its runs still
answer, in the conversation, but nothing is written to send, so no reply is
ever given up for want of an address. In
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

### Each step, or the answer alone

By default a conversation is sent the run's answer alone. Set a channel to
send each step first, and a person can follow the run as it works:

```text
person  why did the deploy fail?
bot     Let me check the logs.
bot     Using `read_logs`…
bot     Asking `billing`…
bot     It timed out because the job waited on the billing lock.
```

```sh
oa channels update platform support --reply-mode events
oa channels update platform support --reply-mode final
```

In the dashboard, it is **While it works** in the channel's sheet. Both need
`channel:write`.

Each step is a message of its own, and it stays in the conversation. A step is
sent event by event, never word by word: **Using** a tool, or **Asking**
another agent for help. What the model writes on its way to a call is not sent:
it is as often a note to itself as words to you. The run's
own bookkeeping (finding its tools, planning, reading the files it was sent)
is not sent, nor is what a tool returned, nor anything the agent asked does.
The answer comes last, and only it quotes the message it answers.

`can_show_events` on a channel says whether its provider can send steps:
Telegram and `custom` can. The dashboard channel cannot, and needs none: its
conversation already shows every step as it happens. `reply_mode` is `final`
or `events`, and is refused on a channel that cannot show events.

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

A run that waits for a person sends a reply of `kind` `approval` (its `id` is
`<run id>:<n>`, like a step's), with the agent's words and the brain's footer in
`text`, and the requests in `approval`, for a receiver that draws its own
buttons:

```json
"approval": {
  "requests": [
    { "id": "…", "code": "7K2F", "tool": "github__create_issue",
      "summary": "title: Disk full", "expires_at": "2026-11-19T13:00:00Z", "ask": true }
  ]
}
```

A request with `ask` set is the person's to answer in the chat: post `yes 7K2F`
or `no 7K2F` back as a message. One without has gone to an approver and carries
no code. The brain's short word on an answer given in the chat is a reply of
`kind` `notice`, quoting the message it is about.

A step is a reply of `kind` `event`, with no `in_reply_to`. Its `id` is the
run's and the step's, `<run id>:<n>`, so dropping a repeat by `id` drops only
that step.

A file the agent hands the person (with `send_file`) is a reply of `kind`
`file`, sent before the run's answer, with no `in_reply_to`. `text` is the
file's name, and `file` carries the file itself, its bytes in base64:

```json
"file": {
  "filename": "report.pdf",
  "media_type": "application/pdf",
  "size": 48213,
  "caption": "The Q3 numbers",
  "data": "JVBERi0xLjcK…"
}
```

`media_type` is what the bytes are, as the brain sniffed them when the file was
kept; `caption` is left out when the agent wrote none. A file is 25 MiB at
most, so a reply is about 34 MB at most: read it before you time it out. Its
`id` is `<run id>:file:<reply id>`, the same on every attempt, so drop a repeat
by it, as every reply may arrive twice. A receiver written before this kind
existed gets it too, so show a `kind` it does not know as nothing rather than
as an answer.

Only the run that answers the conversation sends files, ten at most, each with
a caption of 1024 characters at most, and a `custom` channel only when it has
a reply URL. A file is tried twice, a minute apart, then given up, so it never
holds the answer behind it for long. Telegram sends the file as a document and
WhatsApp as a document message; Telegram drops no repeat, so a file whose
answer was lost on the way can arrive twice there. On Slack, an agent cannot
send files yet. On the dashboard the file shows above the run's answer, and its
bytes are `GET /teams/{id}/sessions/{sessionID}/attachments/{attachmentID}`.

It carries the same two headers as a message, signed under a different prefix:
`X-Openarity-Signature` is `v1=` and the hex HMAC-SHA256 of
`reply:v1:<timestamp>:<body>`. The prefix keeps a captured reply from verifying
as a message if it is posted back to the webhook.

`send_file` is behind the team's approvals like the web tools: a read when the
person who asked is there, so the `writes` mode asks only for a guest's run or
one nobody is watching, such as a schedule's. A rule on `send_file` (**Always
ask** or **Never allow**, under **Built-in tools** in the approval rules) holds
on every channel at once.

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

A step is tried twice, a minute apart, and then given up, so a broken step
never holds back the answer behind it for long.

Replies in one conversation are sent in order, steps before their answer: a
reply waiting to be retried
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
