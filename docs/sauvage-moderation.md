# Sauvage moderation

This fork adds deterministic forum-topic moderation and a local Ornith conduct classifier for
the Sauvage Telegram group (`-1003672565710`).

## Topic routing

| Topic | `message_thread_id` | Behavior |
|---|---:|---|
| General | 1 | Normal pipeline and Ornith conduct moderation |
| Preguntas | 2 | Normal pipeline and Ornith conduct moderation |
| Presentaciones | 3 | One persistent presentation per Telegram user |
| Calendario | 5 | Normal pipeline and Ornith conduct moderation |
| Concurso | 6 | One entry/album per user and active contest id |

Presentation and contest decisions happen before any LLM. Telegram sends album photos as separate
updates; Shield treats messages sharing a `media_group_id` as one entry. A single photo receives a
synthetic entry key based on its message id.

Presentation state is keyed by tenant, chat, and numeric Telegram user id and is not removed when
the original message is deleted or the member leaves. Contest state is keyed by tenant, contest id,
and user id. Change `COMMUNITY_CONTEST_ID` for every new contest.

An allowed topic decision, or any topic decision while shadow mode is active, continues through the
normal Ornith conduct classifier. Topic quotas therefore do not bypass the rules against abuse,
coercion, disclosure of another person's private life, scams, or unwanted advertising.

## Actions and shadow rollout

The first live violation deletes the message and sends a warning in the same topic. Repetition of
the same rule restricts the member for 1 hour, then 24 hours, then bans the member. Telegram actions
are disabled unless `COMMUNITY_APPLY_ACTIONS=true`. Shadow observations are kept in the event
ledger but do not preload the live strike counter.

Start with the checked-in `.env.sauvage.example`: `DRY=true` keeps Ornith and every other action in
global shadow mode, while `COMMUNITY_APPLY_ACTIONS=false` independently shadows topic rules. Keep
both for at least 7 days. Inspect `community_rule_events` by rule, action, user, and topic and review
normal moderation incidents. Validate false positives manually, then set `DRY=false`; enable topic
actions separately with `COMMUNITY_APPLY_ACTIONS=true`. Either switch can be returned to shadow
without deleting the accumulated audit events.

Run exactly one polling replica. The current transport queue is in-process; Telegram redelivery can
reclaim an unfinished ingress row after a restart, but two simultaneous pollers are unsupported.

## Ornith behavior

Set the OpenAI-compatible endpoint and model:

```env
OPENAI_API_BASE=http://litellm.litellm.svc.cluster.local:4000/v1
OPENAI_MODEL=ornith-1.0
LLM_MODE=always
CAS_API=
```

The endpoint is deployment-specific. The example value is the in-cluster service name and must be
replaced when Shield runs outside that cluster. Keep the LiteLLM key in the deployment secret
manager.

The deployment template uses `PARANOID=true` so established members are still reviewed rather than
being permanently pre-approved after their first clean message. It also starts with the broad
legacy sample corpus, similarity classifier, and emoji threshold disabled; this avoids treating
ordinary adult-group language or expressive emoji use as spam when Ornith is unavailable.

The built-in prompt treats member messages and history as untrusted data. It allows consensual adult
conversation, explicit language, flirting, and non-targeted profanity. It flags targeted insults,
harassment, threats, coercion, blackmail, non-consensual pressure, exposure of another person's
private life, doxxing, scams, illegal solicitations, commercial spam, and repeated unwanted ads.
Ambiguous cases fail open for automatic punishment and should use the existing `/report` workflow.

The response parser requires exactly `spam`, `reason`, and `confidence`; rejects extra or missing
fields and wrappers; accepts confidence only from 1 to 100; and refuses a spam decision at 80 or
below. Invalid output creates an error signal and never an automatic sanction.

## Telegram permissions

Grant only:

- delete messages;
- restrict members / ban users.

Do not grant permission to add administrators, change group information, create invitation links,
pin messages, or manage topics. Disable BotFather Privacy Mode so the bot receives ordinary group
messages. Keep the admin chat private and configure `SUPER_USER` with numeric Telegram IDs.

## Data and recovery

The new SQL tables store no raw message body:

- `community_presentations`;
- `community_contest_entries`;
- `community_violations`;
- `community_rule_events`.

Ingress records include topic and media-group identifiers for traceability. Retention now uses the
real timestamp columns and propagates SQL errors instead of silently reporting success. Back up the
database before changing the active contest or enabling actions, and test restore before rollout.

## Operational dashboard and SSO

Enable the built-in dashboard with `SERVER_ENABLED=true`. The Sauvage deployment uses Keycloak
Google authentication at the Traefik layer and Shield validates the forwarded identity again:

```env
SERVER_FORWARD_AUTH_HEADER=X-Auth-Request-Email
SERVER_FORWARD_AUTH_EMAILS=me@e-dani.com
SERVER_FORWARD_AUTH_PROXY_CIDRS=10.42.0.0/16,100.107.21.89/32,100.71.117.127/32,100.75.189.75/32,100.109.183.9/32
```

Never expose the Shield service directly. NetworkPolicy must only admit Traefik Edge and Traefik
LAN. The `/32` addresses are the current host-network Traefik Edge nodes; update this allowlist if
those ingress nodes change. The dashboard provides summary totals, deterministic rule events, the durable Telegram action
journal, presentations, contest entries, live strike counters, CSV export, and runtime state. The
daily digest contains counts only and never includes member message bodies.

`RETENTION_COMMUNITY_EVENTS_TTL` controls the audit-event lifetime independently from persistent
presentation claims and per-contest entry claims.
