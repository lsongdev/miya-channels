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
    "enabled": true,
    "type": "stdio",
    "command": "miya-agents",
    "args": []
  }],
  "channels": [
    {
      "id": "tg-personal",
      "type": "telegram",
      "agent": "miya",
      "commands": {
        "agentSwitch": true,
        "allowedAgents": ["miya", "research"]
      },
      "delivery": {
        "visibility": "normal",
        "streaming": true,
        "editIntervalMs": 800
      },
      "config": {
        "token": "your-bot-token"
      }
    }
  ]
}
```

`channels` must be an array. Each instance has its own stable ID, agent binding,
credentials, and delivery policy.

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

- `/new` - Start a new session
- `/reset` - Reserved for in-place session reset; currently directs users to `/new`
- `/stop` - Cancel the active prompt without deleting the session
- `/agent` and `/agent <id>` - Inspect or switch agents when enabled for the channel
- `/mode` and `/mode <id>` - Inspect or switch ACP session modes
- `/detail <simple|normal|verbose|debug>` - Change the source visibility profile
- `/help` - Show gateway commands

## Session Management

miya-channels persists a route binding for each channel instance, conversation,
and sender. A binding keeps one session per agent, so switching away from an
agent and back can resume its previous context. Use `/new` to replace the
current agent session and `/stop` to cancel only the active prompt.

## License

MIT
