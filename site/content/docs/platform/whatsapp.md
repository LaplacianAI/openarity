---
title: WhatsApp
---

A `whatsapp` channel is a WhatsApp number linked to the brain as a device, the
way WhatsApp Web is linked to a phone. The agent reads the number's chats and
answers from it. There is no Meta Business account, no app review and no
per-message fee: you scan a QR code once, and the number is connected.

That is also its one real cost. A linked device is not an official API, so read
[the risk](#the-risk-a-number-can-be-banned) before you link a number you
care about.

Everything [Channels](channels.md) says holds here too: who is let in, where a
channel answers, what a conversation remembers and how replies are sent. This
page covers what is particular to WhatsApp.

## The risk: a number can be banned

WhatsApp does not offer linked devices to software. The brain uses
[whatsmeow](https://github.com/tulir/whatsmeow), the same library many
WhatsApp bridges use, and WhatsApp can tell a number that behaves like a bot
from one that behaves like a person. A number it decides is a bot can be
banned, for a while or for good, and there is no one to appeal to.

The brain does what it can to look like a person. It cannot promise that is
enough. So:

- **Give the bot a number of its own** when the number matters to anyone. A
  spare SIM or a second number is the safe choice.
- **Keep the replies quiet.** Leave **While it works** on the answer alone (see
  [how it sends](#how-it-sends)). Every step sent as its own message is the
  pattern that looks most like a bot.
- **Don't add the number to large groups** it was never invited to by people.

If a number is banned, the role logs `whatsapp: the number is banned for a
while`, and nothing is sent until WhatsApp lifts it.

## The bot's own number, or yours

When you connect a WhatsApp channel, you say whose number it is. The choice sets
sensible defaults; each one can be changed afterwards.

| | The bot's own | Yours |
| --- | --- | --- |
| Who is on it | A number only the team uses | Your own WhatsApp, with your friends and family |
| What asks the agent | Every direct chat, and groups where the number is @-mentioned | Only a message holding your [trigger word](#your-own-number), such as `@Fduv` |
| Your own messages | — | Dropped, except a message holding the word, in any chat |
| Strangers | Told once that they are waiting | Never told anything; they wait in Approvals |
| The answer | From the bot's number | **From your number, in your name**, with typing shown |

On your own number the agent speaks as you. Everyone in the chat sees your name
on its answer.

## Turning it on

The WhatsApp role is off unless the deployment asks for it. It is one more
brain process, `brain whatsapp`, that holds each WhatsApp channel's connection,
and it runs the same image and configuration as the brain.

With Docker Compose, set this in `deployment/.env`, on a line of its own:

```sh
WHATSAPP_ENABLED=1
```

Then start the stack as usual (`make up`). The Makefile and the staging deploy
read the flag and add the `whatsapp` service. Check it is running:

```sh
docker compose --profile whatsapp ps whatsapp
```

Anywhere else, run `brain whatsapp` with the brain's own configuration. One
replica is enough; two are safe, since each channel is leased to exactly one of
them. The Kubernetes manifests do not run it yet.

Without the role, a WhatsApp channel can be created but never connects. The
channel's sheet on the dashboard says so: "This deployment runs no WhatsApp
role, so nothing pairs and nothing is sent."

## Connecting a channel

On the dashboard, choose **Connect channel**, then **WhatsApp (linked device)**,
and say whose number it is. A WhatsApp channel asks for no token or secret:
nothing is posted to a webhook.

```sh
oa channels create platform support --provider whatsapp
```

For your own number, give it the word that calls the agent:

```sh
oa channels create platform mine --provider whatsapp \
  --trigger-word @Fduv --no-notify-strangers
```

## Linking the number

A channel is linked once, from the phone that has the number.

- **By QR code.** On the channel's sheet choose **Show a QR code**, then on the
  phone open WhatsApp, **Settings**, **Linked devices**, **Link a device**, and
  scan it. The code changes about every 20 seconds; the sheet keeps up.
- **By a pairing code**, when the phone cannot scan the screen. Choose **Use a
  pairing code**, type the number with its country code, and enter the code
  the sheet shows under **Link with phone number instead** on the phone.

From a terminal, `oa channels pair <team> <channel>` draws the QR code (add
`--invert` on a light terminal), or with `--code --phone <number>` shows a
pairing code, and waits until the phone has linked.

Once linked, the sheet shows the number. It stays linked: a deploy or a restart
reconnects it without asking again. **Link another number** logs the old one
out first.

The phone keeps control. Removing the device under **Linked devices** on the
phone unlinks the channel at once, and its sheet says to pair it again.

### What it keeps from before

When a number is linked, WhatsApp sends the device some of each chat's recent
history. The brain keeps the newest 50 messages of each chat, from people
already let in, so a conversation starts with its context. They are never
answered, and their files are not kept. Your own messages from before are
never kept.

## What asks the agent

On the bot's own number, as on any channel: every direct chat, and a group
message that @-mentions the number. A mention means picking the number from
WhatsApp's `@` list; typing the name as text, or replying to one of its
messages, is not one. [Where the channel answers](channels.md#when-a-message-is-answered)
(`answers_in`) can narrow that to groups or to direct chats.

### Your own number

Two things change when the number is yours.

**A trigger word.** With a `trigger_word`, a message asks the agent only when it
holds the word, in groups and direct chats alike, and an @-mention of your
number no longer does. So a friend tagging you is answered by you, not by the
agent. The word is matched in any case and as a word of its own: `@fduv, now?`
holds `@Fduv`; `@Fduvx` and `mail@Fduv` do not. It is one word of at most 32
characters.

You call the agent from any chat by typing the word:

```text
You, in a group:        @Fduv what did we decide on Friday?
The agent, as you:      On Friday you agreed to ...
```

The answer goes to the chat you typed in: the group, or the person a direct
chat is with. Your messages without the word stay private; they are not even
stored.

**Message yourself.** WhatsApp's **Message yourself** chat is your own line to
the agent, whatever `answers_in` says. With a trigger word it needs the word
like every other chat, so a note to yourself stays a note: `buy milk` is
neither answered nor stored, `@Fduv summarise today` is answered. With no word,
every message there asks the agent.

In both cases you are a sender like anyone else: the first time, you are queued
in **Approvals**, and you let yourself in once. The agent's own answers, which
WhatsApp shows as yours too, are never taken as questions.

Set or change the word on the channel's sheet (**Trigger word**), or:

```sh
oa channels update platform mine --trigger-word @Fduv
oa channels update platform mine --trigger-word ""   # take it away
```

## Letting people in

As on every channel, a sender is answered only once an admin or approver has
let them in, under **Approvals**. Someone not let in who asks the agent is
queued there; on the bot's own number they are told once that they are waiting,
and told again when they are let in. On your own number they are told nothing.

On your own number, your friends are not team members. Let each in as
**someone new, as a guest** (`oa channels senders approve … --new`): they get
an identity of their own, who can ask the agent from your chats and change
nothing in the team. Binding them to a member instead would make their
messages that member's, with everything that member may do.

In a group, only messages from people let in are stored. A stranger's chatter
in a group the number is in is never kept.

## How it sends

A reply goes out through the number's own connection, paced as a person would
type it:

- two seconds at least between messages to one chat;
- at most 30 messages a minute, and 600 a day, from one number;
- typing shown first, at about 25 characters a second, never more than six
  seconds.

A reply over the limit waits and is sent later; it is never dropped. A long
answer is split into messages of at most 4,000 characters, and a code block cut
by a split is closed and reopened. The agent's Markdown is turned into
WhatsApp's own: `**bold**` becomes `*bold*`, headings become bold lines, and
code stays in backticks. An answer quotes the message it answers, when the
connection still remembers it.

**While it works**, on the channel's sheet, can send each step as its own
message. On WhatsApp that is several typed-out messages for one question, and
it is the busiest pattern a number can show. Keep it on the answer alone.

## Where the keys are kept

A linked device is a set of keys, and whoever holds them can read and send as
that number. The brain keeps them in its own Postgres database, in the
`whatsmeow` schema, next to everything else it stores.

So:

- **Treat database access as access to every linked number.** Anyone who can
  read the database, or a backup of it, can act as each number.
- **Encrypt and restrict your backups** as you would a password store.
- **Unlinking deletes the keys.** Removing the device on the phone, linking
  another number, or deleting the channel logs the device out and deletes its
  keys. A device no channel names any more is logged out within a minute or so.

The keys never appear in the API, the dashboard or the logs.

## When something is wrong

| You see | What it means |
| --- | --- |
| "This deployment runs no WhatsApp role" | The `whatsapp` service is not running: `WHATSAPP_ENABLED=1` is missing from `.env`, or the stack was not restarted after setting it. |
| "Waiting for WhatsApp…" that never ends | The same: no role has taken the request for a QR code. It is shown as soon as one does. |
| "WhatsApp logged this device out. Pair the number again." | The device was removed on the phone, or WhatsApp logged it out. Link the number again. |
| A message that gets no answer | The sender is waiting in Approvals; or, on your own number, the message did not hold the trigger word; or it is outside where the channel answers. |
| `whatsapp: the number is banned for a while` in the role's logs | WhatsApp has banned the number. Nothing is sent until it is lifted. |
