# miya-channels

Multi-platform messaging gateway that bridges AI agents to chat platforms via ACP (Agent Communication Protocol).

## Overview

miya-channels connects AI agents to multiple instant messaging platforms. It receives messages from chat channels, forwards them to an AI agent using ACP, and streams responses back in real-time.

## Supported Channels

| Platform | Chat ID | Status |
|----------|---------|--------|
| Telegram | `telegram` | ✅ |
| Feishu / Lark | `feishu` | ✅ |
| WeChat (微信) | `wechat` | ✅ |
| WeCom (企业微信) | `wecom` | ✅ |

## Architecture

```
Telegram ─┐
Feishu    ─┤
WeChat    ─┤──→ Channel Manager ──→ ACP Client ──→ AI Agent
WeCom     ─┘         │
                     └──← Streaming Response ──────┘
```

## Configuration

Configuration file: `~/.miya/config.json`

```json
{
  "agents": [{
    "id": "miya",
    "name": "Miya Agents",
    "type": "stdio",
    "command": "miya-agents",
    "args": []
  }],
  "channels": {
    "telegram": {
      "token": "your-bot-token"
    },
    "feishu": {
      "app_id": "cli_xxx",
      "app_secret": "xxx"
    },
    "wechat": {
      "storage_path": "./storage"
    },
    "wecom": {
      "corp_id": "xxx",
      "token": "xxx",
      "encoding_aes_key": "xxx"
    }
  }
}
```

## Building

```bash
make build
```

Cross-compiles for darwin (arm64/amd64), linux (amd64), and windows (amd64).

## Running

```bash
./bin/miya-channels-darwin-arm64
```

## Docker

```bash
make docker
make docker-run
```

## Commands

While chatting with the agent through any channel:

- `/new` — Start a new session
- `/stop` — Stop the current session

## Session Management

miya-channels maintains one ACP session per user per channel. Sessions persist across messages for conversational continuity. Use `/new` to reset and `/stop` to terminate a session.

## License

MIT
