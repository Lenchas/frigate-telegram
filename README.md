<!-- Created by AI Cursor -->
[![Go Report Card](https://goreportcard.com/badge/github.com/OldTyT/frigate-telegram)](https://goreportcard.com/report/OldTyT/frigate-telegram)
[![GolangCI](https://golangci.com/badges/github.com/OldTyT/frigate-telegram.svg)](https://golangci.com/r/github.com/OldTyT/frigate-telegram)

# Frigate telegram (Modified by AI Cursor)

Frigate telegram event notifications. 

## Review notifications (Frigate 0.14+)

Build this source with `docker compose up -d --build` after supplying your own
environment values. The example compose file enables `NOTIFICATION_MODE=reviews`.
Previously published images do not contain this new mode.

Review mode sends one message per **camera review episode**, even when that
episode contains many tracked-object event IDs. It reads reviewed as well as
unreviewed episodes and respects the existing camera, label, severity and zone
filters. Zone names are matched exactly (including hyphens and underscores);
matching any included zone is sufficient.

Completed episodes are sent after a short recording-settle delay. An ongoing
episode is sent once after the maximum wait, so long activity does not prevent
an alert. Its completion does not create a second notification. Videos show the
first bounded fragment, starting up to three seconds before the episode. The
button opens Frigate, where the full recording remains available.

| Setting | Default | Meaning |
| --- | --- | --- |
| `NOTIFICATION_MODE` | `events` | Set `reviews` to enable the new pipeline; legacy event mode is retained. |
| `REVIEW_AFTER` | `0` | Initial Unix-time boundary; zero means first startup. Stored in Redis and reused on restart. |
| `REVIEW_LOOKBACK_SECONDS` | `86400` | Maximum retry/catch-up horizon (24 hours). |
| `REVIEW_SETTLE_SECONDS` | `10` | Wait after completion for recordings to become available. |
| `REVIEW_MAX_WAIT_SECONDS` | `60` | Send an ongoing episode after this wait. |
| `CLIP_MAX_SECONDS` | `30` | Maximum video-fragment duration. |
| `MEDIA_MAX_BYTES` | `25000000` | Maximum downloaded video size; enforced for streaming responses too. |

Media is downloaded sequentially. Partial files and completed temporary files
are removed. An unavailable or oversized video falls back to a photo, then text;
unavailable temporary storage also falls back to text. Polling/API errors are
logged and retried without terminating the bot or sending error-message storms.
These behaviors apply to **review mode**, not the retained legacy pipeline.

Redis stores delivery history per destination chat. Claims are atomic and have
owned five-minute leases, preventing concurrent sends and tight retry loops.
`REDIS_TTL` must exceed the lookback horizon. Keep Redis data persistent; AOF with
`appendfsync everysec` is recommended. When converting an existing RDB-only Redis,
enable AOF online and wait for its initial rewrite before restarting with AOF
enabled, to preserve existing keys. Do not enable AOF on restart against an
existing RDB without first migrating its data.

First startup does not replay older completed episodes. Redis loss resets this
boundary. Telegram has no send idempotency key: a process crash immediately
after Telegram accepts a message but before Redis records it, or an ambiguous
network timeout, can still produce a duplicate. A failed state commit is retried
without resending while the process remains alive.

`/mute` now uses a separate Redis key from `/stop`. Review mode suppresses the
automatic startup Telegram message; operational errors remain in Docker logs.

Validation: `go test ./...` and `go vet ./...`. Set `TEST_REDIS_ADDR` to a disposable
Redis for lease/concurrency integration tests. `TEST_FRIGATE_URL` opts into a
read-only live MP4 download test; it never sends Telegram messages.

---

## Example of work

![alt text](https://raw.githubusercontent.com/OldTyT/frigate-telegram/main/resources/img/telegram_msg.png)

## How to start

1. Install docker
2. Use the `docker-compose.yml` included with this source tree.
3. Change environment variables in docker-compose
4. Deploy:
```bash
docker compose up -d --build
```
5. Profit!

### Environment variables

| Variable | Default value | Description |
| ----------- | ----------- | ----------- |
| `TELEGRAM_BOT_TOKEN` | `""`| Token for telegram bot. |
| `FRIGATE_URL` | `http://localhost:5000` | Internal link in frigate. |
| `FRIGATE_EVENT_LIMIT` | `20`| 	Limit the number of events returned. |
| `DEBUG` | `False` | Debug mode. |
| `TELEGRAM_CHAT_ID` | `0` | Telegram chat id. |
| `SLEEP_TIME`| `5` | Sleep time after cycle, in second. |
| `FRIGATE_EXTERNAL_URL` | `http://localhost:5000` | External link in frigate(need for generate link in message). |
| `TZ` | `""` | Timezone |
| `REDIS_ADDR` | `localhost:6379` | IP and port redis |
| `REDIS_PASSWORD` | `""` | Redis password |
| `REDIS_DB` | `0` | Redis DB |
| `REDIS_PROTOCOL` | `3` | Redis protocol |
| `REDIS_TTL` | `1209600` | Redis TTL for key event(in seconds) |
| `TIME_WAIT_SAVE` | `30` | Wait for fully video event created(in seconds) |
| `WATCH_DOG_SLEEP_TIME` | `3` | Sleep watch dog goroutine seconds |
| `EVENT_BEFORE_SECONDS` | `300` | Send event before seconds |
| `SEND_TEXT_EVENT` | `False` | Send text event without media |
| `FRIGATE_EXCLUDE_CAMERA` | `None` | List exclude frigate camera, separate `,` |
| `FRIGATE_INCLUDE_CAMERA` | `All` | List Include frigate camera, separate `,` |
| `FRIGATE_EXCLUDE_LABEL` | `None` | List exclude frigate event, separate `,` |
| `FRIGATE_INCLUDE_LABEL` | `All` | List Include frigate event, separate `,` |
| `FRIGATE_EXCLUDE_ZONE` | `None` | List exclude frigate zone, separate `,` |
| `FRIGATE_INCLUDE_ZONE` | `All` | List Include frigate zone, separate `,` |
| `FRIGATE_INCLUDE_SEVERITY` | `All` | List Include frigate severity (alert, detection), separate `,` |
| `REST_API_ENABLE` | `False` | Enabling the http rest API |
| `REST_API_LISTEN_ADDR` | `:8080` | Rest API listen addr |
| `SHORT_EVENT_MESSAGE_FORMAT` | `False` | Short event message format |
| `INCLUDE_THUMBNAIL_EVENT` | `True` | Include thumbnail from event to messsage |


## Features

### Rest API

First the API needs to be enabled in the ENV. The docker-compose.yml has the ENV already but set to "False" per default.
REST_API_ENABLE: True

The Full URL: http://IP-OF-DOCKER-HOST:8080/api/v1/COMMAND

Possible Commands:
- /mute
- /ping
- /resume
- /status
- /stop
- /unmute

For more details Swagger aviaible on: `http://localhost:8080/docs/index.html`

### Mute/unmute events messages

You can enable or disable notifications for event messages (data is stored in Redis, ensuring persistence across restarts).

Commands:
* `/mute`
* `/unmute`

> [!WARNING]
> For security reasons, commands only work in the TelegramChatID chat.

### Stop/resume send events messages

You can pause or resume sending notifications for event messages (data is stored in Redis, ensuring persistence across restarts).

Commands:
* `/stop`
* `/resume`

> [!WARNING]
> For security reasons, commands only work in the TelegramChatID chat.
