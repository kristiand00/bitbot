# BitBot

A Discord bot written in Go that pairs a conversational AI assistant with practical server tooling. It uses [Regolo.ai](https://regolo.ai) (an OpenAI-compatible, green AI platform) for chat and tool-calling, and embeds [PocketBase](https://pocketbase.io) for storage and an admin dashboard.

Talk to it in natural language and it can look up crypto prices, manage reminders, and — for authorized admins — generate SSH keys and run commands on remote servers, all through AI tool-calling. Slash commands are available for the same features.

## Features

- **AI chat** — Mention the bot or DM it and it replies using a Regolo model (defaults to `gpt-oss-120b`). The model can call tools to actually perform actions, not just describe them.
- **Cryptocurrency prices** — `/cry` fetches live prices via [CryptoCompare](https://min-api.cryptocompare.com).
- **Reminders** — Add, list, and delete reminders in natural language (`/remind add`, `/remind list`, `/remind delete`).
- **SSH management** (admin only) — Generate/rotate an SSH key pair, connect to remote servers, execute commands, list saved servers, and disconnect — via slash commands or by asking the AI.
- **Event organizer** — `/createevent` opens a modal to organize an Ava dungeon raid event.
- **Help** — `/help` lists available commands by category.
- **PocketBase backend** — Saved servers, reminders, and users are stored in an embedded PocketBase instance with a web admin UI.

## Slash commands

| Command | Description |
| --- | --- |
| `/cry` | Get cryptocurrency price information |
| `/remind add\|list\|delete` | Manage reminders |
| `/genkey` | Generate and save an SSH key pair *(admin)* |
| `/regenkey` | Regenerate the SSH key pair *(admin)* |
| `/showkey` | Show the public SSH key *(admin)* |
| `/ssh` | Connect to a server via SSH *(admin)* |
| `/exe` | Execute a command on the connected server *(admin)* |
| `/exit` | Close the SSH connection *(admin)* |
| `/list` | List saved servers *(admin)* |
| `/createevent` | Organize an Ava dungeon raid event |
| `/help` | List available commands by category |

The same SSH and reminder actions are also exposed to the AI as callable tools, so you can just ask the bot in plain language.

## AI backend (Regolo.ai)

BitBot calls Regolo's OpenAI-compatible chat completions endpoint at `https://api.regolo.ai/v1/chat/completions`. Because the API is OpenAI-compatible, standard chat, tools, and streaming semantics apply.

- **API reference:** https://docs.api.regolo.ai/regolo-api.json
- **Default model:** `gpt-oss-120b` (override with `REGOLO_MODEL`)
- Beyond chat, the Regolo API also offers models listing, embeddings, image generation, text-to-speech / transcription, reranking, and assistants — see the reference above.

Get an API key from your Regolo.ai account and set it via `REGOLO_API_KEY`.

## Extended tools (toolbelt & MCP)

Beyond the built-in reminder tools, the bot exposes a **toolbelt**: the model sees two meta-tools (`find_tools` and `call_tool`) and reaches everything else through them, so the per-request tool list stays small no matter how many tools are registered. SSH management is registered locally; remote tools come from **MCP servers**.

MCP servers are managed from Discord with the admin-only **`/mcp`** command:

- `/mcp add name:<name> url:<url> [token:<token>] [admin_only:<true|false>]` — add and connect a server (`url` is a Streamable-HTTP MCP endpoint; `token` is an optional bearer token; `admin_only` defaults to `true` — set `false` to let non-admins use the server's tools)
- `/mcp remove name:<name>` — disconnect a server and remove its tools
- `/mcp list` — show configured servers and their connection status / tool counts
- `/mcp reload` — re-sync immediately

Configuration is stored in the PocketBase **`mcp_servers`** collection (also editable via the admin UI at `/_/` if preferred). The bot connects to each enabled server, registers its tools into the toolbelt, and re-syncs periodically — so changes take effect without a restart. Tools flagged **destructive** by the server require an admin to approve a Confirm/Cancel button before they run.

For convenience, `BAKI_MCP_URL` / `BAKI_MCP_TOKEN` (if set) are seeded once into `mcp_servers` on startup; after that, the collection is the source of truth.

## Configuration

Configuration is read from environment variables (loaded from a `.env` file in non-production environments). Copy `.env_example` to `.env` and fill in the values:

| Variable | Required | Description |
| --- | --- | --- |
| `BOT_TOKEN` | yes | Discord bot token |
| `APP_ID` | yes | Discord application ID |
| `ADMIN_DISCORD_ID` | yes | Discord user ID allowed to use admin/SSH features |
| `REGOLO_API_KEY` | yes | Regolo.ai API key |
| `CRYPTO_TOKEN` | yes | CryptoCompare API key |
| `REGOLO_MODEL` | no | Regolo model name (defaults to `gpt-oss-120b`) |
| `ENV` | no | Set to `production` to skip loading `.env` |

## Getting started

### Prerequisites

- Go 1.21+ (see `go.mod` for the exact version)
- A Discord application with a bot token ([Discord Developer Portal](https://discord.com/developers/applications))
- A Regolo.ai API key and a CryptoCompare API key

### Run locally

```bash
git clone https://github.com/kristiand00/bitbot.git
cd bitbot
cp .env_example .env   # then fill in your tokens
go mod download
go run .
```

### Run modes

BitBot has three run modes, selected by the first argument:

```bash
go run .                  # bot only
go run . serve            # PocketBase admin UI only (http://localhost:8090/_/)
go run . serve-with-bot   # bot + PocketBase admin UI together
```

The PocketBase admin UI is served on `0.0.0.0:8090` at `/_/`.

## Docker

The included `Dockerfile` builds a small Alpine image and runs `serve-with-bot` (bot + PocketBase) by default, exposing the admin UI on port `8090`.

```bash
docker build -t bitbot .
docker run --rm \
  --env-file .env \
  -p 8090:8090 \
  -v bitbot_pbdata:/app/pb_data \
  bitbot
```

PocketBase data is persisted in the `/app/pb_data` volume. The image is pinned to the `Europe/Zagreb` timezone; adjust the `TZ` and timezone lines in the `Dockerfile` if you need a different one.

## Project layout

```
main.go              Entry point and run-mode selection
bot/                 Discord bot: commands, AI chat, tools
  bot.go             Command registration and interaction routing
  chat.go            AI chat loop and tool-call handling
  regolo.go          Regolo.ai (OpenAI-compatible) client
  genai_tools.go     Tool definitions exposed to the model
  command-crypto.go  Cryptocurrency price command
  reminder.go        Reminder feature
  sshclient.go       SSH commands
  ssh_core.go        SSH connection logic
pb/                  Embedded PocketBase setup
pb_data/             PocketBase data (gitignored)
spike/               Experiments and prototypes
```

## Notes

- `pb_data/` (the PocketBase database) is intentionally gitignored — do not commit it, as it may contain credentials and runtime data.
- SSH and other admin actions are restricted to the Discord user set in `ADMIN_DISCORD_ID`.
