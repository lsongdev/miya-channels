# miya-channels Architecture Plan

## Role

`miya-channels` is a standalone chat gateway. It receives messages from external chat platforms and forwards them to an ACP agent. It should remain independently runnable, but it is also intended to become a Channel Connector for `miya-desktop`.

## Current Direction

The long-term shape should be:

```text
Chat Platform
  -> Channel Receive
  -> Conversation Resolver
  -> ACP Prompt
  -> ACP session/update router
  -> Channel Writer
```

When integrated with `miya-desktop`, the middle of that flow should become:

```text
Channel IncomingMessage
  -> desktop ConversationService.GetOrCreateConversation(source)
  -> desktop ConversationService.SendMessage()
  -> conversation events
  -> Channel Writer
```

## Message Size Strategy

Do not split rendered Telegram HTML. Agents produce Markdown, and Telegram receives HTML after `tgmd.Convert`. Splitting already-rendered HTML can cut tags or entities and corrupt the whole message.

The correct order is:

1. Accumulate Markdown chunks from ACP.
2. Split Markdown into safe parts.
3. Ensure every part is independently renderable.
4. Convert each part to Telegram HTML.
5. Send/edit Telegram messages.

For fenced code blocks, if a split happens inside the block:

- close the fence at the end of the current part
- reopen the same fence at the beginning of the next part

This keeps each converted HTML message valid.

## ACP Notification Routing

There should be one ACP notification handler per ACP client, registered at startup. Per-message handlers are unsafe because each request can replace or duplicate routing behavior.

The worker should maintain:

```text
sessionID -> active channel writer
channel:user -> ACP session
```

Only `agent_message_chunk` text should be streamed to simple chat platforms. Desktop can show richer events such as thoughts, tool calls, plans, and usage.

## Near-Term Work

- Persist `channel:user -> sessionID` if long-lived continuity is needed across process restarts.
- Add a platform-neutral chunking policy for Feishu, WeChat, and WeCom.
- Add rate limiting and edit throttling per channel.
- Add structured channel event logs for debugging delivery failures.
- Move conversation/session ownership into `miya-desktop` when running in integrated mode.
