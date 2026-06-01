# FastDiscordGotifyBridge

> ⚠️ This is 100% vibe-coded, not a single line was written by me (except this one)

Forward messages from **specific Discord channels** to **Gotify** push
notifications, in real time — often faster than Discord's own mobile push.

It connects to the Discord Gateway over a persistent WebSocket using your
**user token**, listens for messages in the channels you choose, and POSTs them
to your Gotify server. Because the path is `Gateway event → your machine →
Gotify`, it skips Apple/Google's push infrastructure entirely. Put Gotify on
your LAN and latency can be sub-second.

> ⚠️ **User tokens & Discord ToS.** This is a "selfbot". Passive listening is
> low-risk and common in the homelab community, but it technically violates
> Discord's Terms of Service. Use at your own risk. A proper bot account is the
> compliant alternative if you can add one to the server.

## How it works

- Persistent Gateway (v10) WebSocket with heartbeat, auto-resume and
  exponential-backoff reconnect.
- `MESSAGE_CREATE` events filtered by channel ID.
- Async worker queue so Gotify HTTP latency never stalls the event loop.
- Skips your own messages by default; can ignore bots.

## Configuration

Two interchangeable sources. **Environment variables override the file**, so
you can use `config.toml` locally and pure env vars in Docker.

### Local: `config.toml`

```sh
cp config.example.toml config.toml
# edit config.toml, then:
go run .                 # uses ./config.toml by default
# or
go run . -config /path/to/config.toml
```

See [`config.example.toml`](config.example.toml) for all fields.

### Docker / Compose: environment variables

| Variable                  | Required | Description                                        |
| ------------------------- | -------- | -------------------------------------------------- |
| `DISCORD_USER_TOKEN`      | yes      | Your Discord account token.                        |
| `GOTIFY_URL`              | yes      | Gotify base URL (no trailing `/message`).          |
| `GOTIFY_TOKEN`            | yes      | Gotify **application** token.                      |
| `WATCH`                   | yes\*    | Channels to watch (format below).                  |
| `GOTIFY_DEFAULT_PRIORITY` | no       | Default priority (default `5`).                    |
| `DISCORD_NOTIFY_OWN`      | no       | `true` to also forward your own messages.          |
| `DISCORD_IGNORE_BOTS`     | no       | `true` to drop bot/webhook messages.               |
| `CONFIG_PATH`             | no       | Override the config file path.                     |

\* Required unless provided via a mounted `config.toml`.

**`WATCH` format** — entries separated by `;`, each is
`label|channelid,channelid,...[|priority]`:

```
WATCH="My Server #alerts|222222222222222222,333333333333333333|8; Other|444444444444444444"
```

## Running with Docker

The image is a 2-stage build whose final stage is `scratch` — it contains
**only the binary** (~6.6 MB). The Mozilla CA bundle is fetched at build time
and embedded into the binary (via the `embedcerts` build tag), so TLS works
with no cert files in the image.

```sh
docker build -t fastdiscordgotifybridge:latest .
```

### docker compose (recommended)

```sh
cp .env.example .env      # fill in your tokens
# edit the WATCH line in docker-compose.yml
docker compose up -d
docker compose logs -f
```

### plain docker run

```sh
docker run -d --name discord-gotify-bridge --restart unless-stopped \
  -e DISCORD_USER_TOKEN="..." \
  -e GOTIFY_URL="https://gotify.example.com" \
  -e GOTIFY_TOKEN="..." \
  -e WATCH="Alerts|222,333|8" \
  fastdiscordgotifybridge:latest
```

Prefer a file in Docker? Mount it: `-v ./config.toml:/config.toml:ro`.

## Getting the IDs and token

- **Channel / server IDs:** Discord → User Settings → Advanced → enable
  *Developer Mode*, then right-click a channel/server → *Copy ID*.
- **Gotify application token:** Gotify web UI → *Apps* → create an application →
  copy its token.
- **Discord user token:** open Discord in a browser, DevTools → Network tab,
  filter for any API request, and read the `Authorization` request header.
  Treat it like a password.

## Local build

```sh
go build -o bridge .     # system CA pool, no embedded certs
./bridge -config config.toml
```
