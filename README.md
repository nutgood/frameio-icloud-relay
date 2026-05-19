# frameio-icloud

Self-hosted relay that drops Frame.io Camera-to-Cloud uploads into a Mac's Photos library — and from there into iCloud Photos via the system's normal sync — then deletes the source from Frame.io so the free-tier quota stays clean.

Designed to run unattended on a Mac mini. One Go binary, one LaunchAgent, one Pushover trickle of notifications.

```
Camera ──upload──▶ Frame.io cloud ──webhook──▶ this relay ──▶ Photos.app ──iCloud sync──▶ iCloud Photos
                                                     │
                                                     └── delete from Frame.io
```

## Why

Fujifilm cameras (X-H2S and friends) can upload natively to Frame.io C2C over Wi-Fi, but they can't be pointed at a self-hosted endpoint directly (the camera-side TLS is certificate-pinned to Frame.io). Solution: let Frame.io be the transit point, run this relay on a Mac to drain every upload into the host's Photos library — and let iCloud Photos sync do the actual cloud upload.

## What's in the box

- **Webhook-driven.** Registers a `file.upload.completed` webhook with Frame.io on first run. Sub-second from camera-finished-upload to "imported into Photos.app". HMAC signature verified on every delivery (`v0:{ts}:{body}`, SHA-256).
- **Polling fallback.** A reconcile pass every minute (configurable) walks the Frame.io folder and catches anything missed.
- **OAuth 2.0 via Adobe IMS** with automatic refresh-token rotation. One interactive setup, headless after.
- **Crash-safe handoffs.** Local disk is the source of truth for "downloaded"; a state file tracks "imported into Photos but not yet deleted from Frame.io". Restarting mid-flight does the right thing.
- **Pushover notifications** with burst coalescing — one "Received webhook…" push when a burst starts, one summary push 30 s after it ends. Errors get their own immediate push.
- **macOS LaunchAgent.** `frameio-icloud install` writes the plist and bootstraps the agent into your gui domain so it starts on every login and on Mac mini reboots (assuming auto-login is on).
- **Single binary + subcommands.** Same `frameio-icloud` is the service, the installer, the CLI, the status query, the log tail.

## Requirements

- macOS host with Photos.app, signed in to iCloud, with **iCloud Photos** enabled in System Settings → Apple ID → iCloud → Photos.
- A Frame.io account with a Camera-to-Cloud-enabled project (free tier is fine).
- An Adobe Developer Console project with a Frame.io V4 OAuth Web App credential.
- **Optional:** a publicly-reachable HTTPS URL routing to port 9000 of the host (Tailscale Funnel, Cloudflare Tunnel, etc.). Without this the relay still works, just with up-to-one-poll-interval (default 60 s) of latency.
- **Optional:** a Pushover account + application token if you want notifications.

## Install

```sh
# Build
make build

# Authenticate against Frame.io (one-time interactive browser flow).
./bin/frameio-icloud auth -client-id <CLIENT_ID> -client-secret <CLIENT_SECRET>

# (Optional) configure Pushover for notifications.
./bin/frameio-icloud config set pushover.token <APP_TOKEN>
./bin/frameio-icloud config set pushover.user_key <USER_KEY>

# (Optional) configure webhook delivery. Without this the relay polls.
./bin/frameio-icloud config set public_url https://your-tunnel.example.com/webhook

# Install the LaunchAgent — copies the binary to ~/.local/bin and
# loads sh.leca.frameio-icloud into your gui domain.
./bin/frameio-icloud install

# Confirm it's running.
./bin/frameio-icloud status
```

The first photo to arrive will cause macOS to prompt for **Automation permission** for the binary to drive Photos.app. Approve it. (Subsequent imports are silent.)

## Adobe OAuth setup

1. https://developer.adobe.com/console → **Create new project**.
2. **Add API → Frame.io V4 API**.
3. Credential type: **OAuth Web App**.
4. **Default redirect URI**: `https://localhost:12345/callback`
5. **Redirect URI pattern**: `https://localhost:12345/.*`
6. Use the Client ID + Client Secret with `frameio-icloud auth`.

`frameio-icloud auth` runs an HTTPS listener on `https://localhost:12345` with an ephemeral self-signed cert (Adobe rejects raw IPs and plaintext loopback). Click past the browser warning the first time.

## CLI reference

```
frameio-icloud serve           Run the relay (LaunchAgent invokes this).
frameio-icloud auth            Interactive OAuth login.
frameio-icloud auth -discover  Print Frame.io account/workspace/project hierarchy.
frameio-icloud install         Install + start the LaunchAgent.
frameio-icloud uninstall       Remove the LaunchAgent (keeps config + tokens).
frameio-icloud start|stop|restart  Control the running agent.
frameio-icloud status          Live snapshot of the running service.
frameio-icloud logs            tail -f the service stdout log.
frameio-icloud logs -err       tail -f stderr.
frameio-icloud config list     Print all config (with secrets redacted).
frameio-icloud config get <key>
frameio-icloud config set <key> <value>
frameio-icloud config unset <key>
frameio-icloud test-pushover   Send a one-off Pushover notification.
frameio-icloud test-photos     Import a 1×1 PNG to verify Photos.app integration.
```

## Config keys

| Key | Description |
|---|---|
| `frameio.account`   | Frame.io V4 account UUID. Auto-discovered if exactly one. |
| `frameio.workspace` | Workspace UUID. Auto-discovered if exactly one. |
| `frameio.folder`    | Project root folder UUID (what reconcile walks). Auto-discovered if exactly one project. |
| `public_url`        | Public HTTPS URL routing to the webhook listener. Empty ⇒ polling-only. |
| `webhook_addr`      | Local bind address. Default `:9000`. |
| `poll_interval`     | Go duration; default `60s`. |
| `stuck_timeout`     | Delete non-ready Frame.io files older than this. Default unset (disabled). Useful: `6h`. |
| `pushover.token`    | Pushover application API token. |
| `pushover.user_key` | Pushover user key. |

## What lives where

| What | Path |
|---|---|
| Config | `~/Library/Application Support/frameio-icloud/config.json` |
| Tokens | `~/Library/Application Support/frameio-icloud/tokens.json` (0600) |
| Service state | `~/Library/Application Support/frameio-icloud/state.json` |
| Status socket | `~/Library/Application Support/frameio-icloud/status.sock` |
| Download buffer | `~/Library/Application Support/frameio-icloud/downloads/` |
| Logs | `~/Library/Logs/frameio-icloud/{frameio-icloud.log,frameio-icloud.err}` |
| LaunchAgent | `~/Library/LaunchAgents/sh.leca.frameio-icloud.plist` |
| Installed binary | `~/.local/bin/frameio-icloud` |

`uninstall` removes the LaunchAgent and the installed binary but **not** the Application Support / Logs directories. Tokens and config survive across upgrades. `rm -rf "$HOME/Library/Application Support/frameio-icloud"` for a clean wipe.

## Notifications

Pushover events are coalesced into bursts:

1. First webhook of a quiet period → one immediate push: **"Received webhook, importing photos…"**
2. Subsequent webhooks/imports during the burst increment counters but don't notify.
3. 30 s after the last activity → one summary push: **"Imported N pictures"** (or `"Imported N pictures; K failed"`).
4. Operator-visible errors (auth dead, Photos.app permission denied) bypass the burst and notify immediately.

`test-pushover` will verify credentials work end-to-end.

## Troubleshooting

**`Automation permission denied` from Photos.** macOS gates AppleScript→Photos via System Settings → Privacy & Security → Automation. Allow the entry for `frameio-icloud` → `Photos`. The first import attempt is what causes the prompt; if you dismissed it without approving, toggle it manually in System Settings.

**LaunchAgent loaded but service not responding.** Check `frameio-icloud logs -err`. Most common: tokens are missing or stale → re-run `frameio-icloud auth`.

**`invalid_scope` at OAuth time.** Adobe scope names: `openid email profile offline_access additional_info.roles`. No Frame.io-specific scope is needed.

**Webhook signature verification fails.** A stale webhook from a previous install may exist on the Frame.io side with a different secret. Inspect via `curl -H "Authorization: Bearer $ACCESS" https://api.frame.io/v4/accounts/$ACCT/workspaces/$WS/webhooks` and delete with `DELETE /v4/accounts/$ACCT/webhooks/$ID`. Re-running the relay registers a fresh one.

**Frame.io files stuck in `status: created`.** Bytes never finished uploading from the camera. Frame.io doesn't garbage-collect these. Set `stuck_timeout=6h` (or whatever is comfortably longer than a slow upload) and the reconcile pass will reap them.

## License

MIT.
