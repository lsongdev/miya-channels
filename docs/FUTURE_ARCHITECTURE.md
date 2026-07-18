# miya-channels Future Architecture

本文是面向长期演进的架构基线。重构已经开始，文中的分层与数据模型同时作为后续实现的约束；尚未完成的能力会在“实现状态”中明确列出。

## 实现状态

截至 2026-07-18，已经完成：

- `ChannelInstance` 与唯一的 `channels[]` 配置模型，支持多个同类型实例。
- `IncomingEvent`、`Attachment`、`AgentEvent`、`DeliveryItem` 四层模型。
- source key 使用 channel instance、conversation 和 sender。
- 静态多 Agent registry、channel 默认 agent、来源级 agent binding 与 `/agent` 切换。
- 每个来源在每个 agent 上保留独立 ACP session，统一使用 version 2 route store。
- `/new`、`/reset`、`/stop`、`/agent`、`/mode`、`/detail`、`/help` 命令路由。
- simple/normal/verbose/debug delivery policy，raw thought 默认不外发。
- channel-neutral chunk 合并与节流，支持 streaming/finalOnly。
- ACP session 失效后创建新 session 并重试一次。
- WeChat 图片、语音、文件和视频输入转换为 ACP content blocks。

仍需继续完善：

- Telegram、Feishu 和 WeCom 的入站附件受当前平台 SDK 能力限制，尚未全部接通。
- 群聊 mention/thread 策略和角色权限仍仅保留数据边界。
- Desktop conversation ownership 上移尚未实施。
- 平台富投递能力仍以现有 writer/file API 为主。

## 目标

`miya-channels` 应该成为外部聊天平台和 ACP agent 之间的网关：

```text
Channel Adapter
  -> normalized incoming event
  -> command/router/session resolver
  -> ACP agent prompt
  -> normalized agent event stream
  -> channel delivery policy
  -> Channel Adapter writer
```

这个网关要支持：

- 文本、图片、文件等多模态输入。
- ACP 流式输出，包括 message、thought、tool call、plan、usage、file 等事件。
- 不同渠道、不同会话、不同用户有不同的可见性和投递策略。
- 多个同类型渠道实例，例如多个 Telegram bot、多个 WeChat 账号、多个企业工作区。
- 后续支持多 agent、channel 绑定 agent、用户按命令切换 agent。
- 独立运行和被 `miya-desktop` 托管运行两种模式。

## 非目标

- 不让每个 channel adapter 直接理解完整 ACP 协议。
- 不把 Telegram、WeChat 等平台限制写进 agent 层。
- 不在第一阶段实现复杂的权限、计费、多租户后台，但数据模型要为它们留位置。
- 不引入"人"（user profile）聚合层——不把多个平台账号汇聚到同一个用户实体共享 memory。每个 source 独立，后续如果需要再加。

## 关键设计判断

### 应支持多个相同类型的渠道

推荐支持多个同类型 channel。原因是这个能力在长期上很有价值：

- 同一个人可能有私人 Telegram bot、团队 Telegram bot、测试 bot。
- WeChat/WeCom/Feishu 很可能按账号、企业、应用、部门隔离。
- 不同渠道实例可能绑定不同 agent、不同提示词、不同可见性策略。
- 发布、灰度、测试时可以让一个实例跑新 agent，另一个实例保持稳定。

`channels` 只接受数组，`id` 和 `type` 必须分开：

```json
{
  "channels": [
    {
      "id": "tg-personal",
      "type": "telegram",
      "enabled": true,
      "agent": "miya",
      "delivery": { "visibility": "simple" },
      "config": {
        "token": "..."
      }
    },
    {
      "id": "tg-lab",
      "type": "telegram",
      "enabled": true,
      "agent": "research",
      "delivery": { "visibility": "debug" },
      "config": {
        "token": "..."
      }
    }
  ]
}
```

旧的 channel object 配置不再接受；配置错误会在启动时直接返回。

### Channel instance 是路由主键

后续所有状态都应该使用 `channelInstanceID`，而不是 `channelType`：

```text
source = channelInstanceID + externalConversationID + externalUserID
```

例如：

```text
tg-personal:chat:12345:user:12345
tg-lab:chat:12345:user:12345
```

它们即使来自同一个 Telegram chat ID，也应该是两条不同来源，因为背后的 bot、配置、agent、权限可能完全不同。

## 分层架构

### 1. Channel Adapter

每个平台 adapter 只负责平台协议：

- 接收平台消息。
- 下载或解析平台附件。
- 把平台事件转换成 `IncomingEvent`。
- 根据 `DeliveryItem` 发送、编辑、上传文件或展示状态。
- 处理平台限流、消息长度、Markdown/HTML 转换、文件上传差异。

adapter 不应该决定：

- 该调用哪个 agent。
- 一个来源对应哪个 ACP session。
- 是否暴露 thought/tool call。
- `/new` 是创建 ACP session 还是切换本地 thread。

### 2. Normalized Event Model

现有 `IncomingMessage` 是文本优先：

```go
type IncomingMessage struct {
    From    string
    Who     string
    ReplyTo string
    Content string
}
```

长期建议改成更通用的事件：

```go
type IncomingEvent struct {
    ChannelID      string
    ChannelType    string
    ConversationID string
    SenderID       string
    MessageID      string
    ReplyTo        string
    Text           string
    Attachments    []Attachment
    Raw            json.RawMessage
    ReceivedAt     time.Time
}

type Attachment struct {
    Type     string // image, audio, video, file
    Name     string
    MimeType string
    URL      string
    Data     []byte
    Size     int64
}
```

`ConversationID` 表示平台里的会话或聊天室，`SenderID` 表示发送者。私聊时两者可能相同，群聊时必须区分。

### 3. Command Router

命令处理应该在 adapter 之后、agent prompt 之前。理由是 `/new`、`/reset`、`/agent` 是 miya 网关语义，不是 Telegram 专属语义。

命令建议分为三类：

- Gateway command：`/new`、`/reset`、`/stop`、`/help`、`/agent`。
- Agent command：ACP `available_commands_update` 暴露的 agent 能力，或直接透传给 agent。
- Platform command：Telegram bot command menu、Feishu slash command 等平台 UI 能力。

长期可以定义：

```text
/new              create a new session for current source
/reset            alias of /new, or reset current session state
/stop             cancel active prompt or close current session
/agent            list available agents
/agent <id>       switch source binding to an agent
/mode             list current agent modes if ACP exposes modes
/mode <id>        switch mode if supported
/detail simple    show only final assistant text
/detail normal    show message plus selected progress
/detail debug     show thought/tool/usage events
```

Telegram 的 command menu 只是这些命令的一种平台展示方式。WeChat 等没有 slash command 体验的平台，也可以用同样的文本命令或管理 UI。

### 4. Routing And Session Resolver

建议把“来源到 agent/session”的关系抽成独立服务：

```text
SourceIdentity
  -> RoutePolicy
  -> AgentEndpoint
  -> ConversationSession
```

核心映射：

```text
channelID + conversationID + senderID -> binding

binding:
  agentID
  acpSessionID
  cwd
  visibility
  createdAt
  updatedAt
```

群聊策略需要单独配置：

- `per_user`：同一个群里每个用户独立 session。
- `per_conversation`：整个群共用一个 session。
- `mention_only`：只有 @bot 或回复 bot 时才触发。
- `threaded`：如果平台支持 thread，则每个 thread 一个 session。

默认建议：

- 私聊：`per_conversation`。
- 群聊：`mention_only + per_user`，避免一个群里的多人互相污染上下文。

### 5. Agent Registry

当前 `DefaultAgent` 选择第一个可用 ACP agent。长期建议引入 agent registry：

```text
agentID -> ACP client or ACP endpoint
```

不同 channel instance 可以有默认 agent：

```json
{
  "id": "tg-lab",
  "type": "telegram",
  "agent": "research"
}
```

同时允许来源级覆盖：

```text
tg-lab:chat:123:user:456 -> agent: "coding"
```

是否允许用户切换 agent 应由 policy 控制：

```json
{
  "commands": {
    "agentSwitch": true,
    "allowedAgents": ["miya", "research", "coding"]
  }
}
```

### 6. ACP Event Normalizer

ACP 通知应在 gateway 内部先转换为统一的 `AgentEvent`，再由 delivery policy 决定如何投递。

```go
type AgentEvent struct {
    SessionID string
    Type      string // message_delta, thought_delta, tool_start, tool_update, plan, usage, file, done, error
    Text      string
    Content   []ContentBlock
    Tool      *ToolEvent
    Plan      *PlanEvent
    Usage     *UsageEvent
    Raw       json.RawMessage
}
```

这样做的好处是：

- channel adapter 不需要依赖 ACP 的全部类型。
- 将来接入非 ACP agent 或 desktop conversation service 时，channel 层不必重写。
- 可以集中处理敏感事件过滤，例如 thought 不一定允许外发。

### 7. Delivery Policy

不同渠道需要不同响应级别。建议将输出分成几个 profile：

```text
simple:
  send assistant message only
  hide thought/tool/plan/usage

normal:
  send assistant message
  optionally send compact progress such as "Working..." or "Using tool: read_file"
  hide raw thought

verbose:
  send assistant message
  send tool start/done summaries
  send plan updates when useful
  hide raw thought by default

debug:
  send everything allowed by security policy
  include thought/tool args/raw errors/usage
```

推荐默认值：

- Telegram 私人 bot：`normal`。
- 企业群、微信群：`simple` 或 `normal`。
- 测试/开发 channel：`debug`。
- desktop：完整事件流，不通过聊天文本降级。

`thought` 要谨慎处理。即使 ACP 暴露 `agent_thought_chunk`，外部 IM 渠道默认也不应发送原始 thought，除非用户明确打开 debug，并且 agent/policy 允许。

### 8. Delivery Renderer

Delivery policy 产出平台无关的投递项：

```go
type DeliveryItem struct {
    Kind      string // text, status, edit, file, reaction
    Text      string
    File      *Attachment
    Format    string // markdown, plain, html
    Final     bool
    Sensitive bool
}
```

然后 channel adapter 根据自身能力处理：

- Telegram：可以编辑同一条消息、支持 HTML/Markdown、支持 action typing。
- WeChat：可能更适合少量最终消息，不适合频繁编辑。
- Feishu/WeCom：可能支持富文本卡片、thread、文件上传。

因此 streaming 不应等同于“每个 chunk 都发出去”。网关应有节流和合并层：

```text
ACP chunks -> coalescer -> renderer -> channel writer
```

每个 channel instance 可配置：

```json
{
  "delivery": {
    "visibility": "normal",
    "streaming": true,
    "editIntervalMs": 800,
    "maxMessageChars": 3900,
    "finalOnly": false
  }
}
```

## 输入多模态策略

文本、图片和文件进入 ACP 时都应变成 `[]acp.ContentBlock`：

```text
IncomingEvent.Text        -> ContentBlock{Type: "text"}
Attachment image/*        -> ContentBlock{Type: "image"}
Attachment audio/*        -> ContentBlock{Type: "audio"}
Attachment file/resource  -> ContentBlock{Type: "resource"}
```

附件可以有两种处理方式：

- 小文件：下载到本地或内联 base64，再传给 agent。
- 大文件：保存到 gateway storage，传 `file://` 或可访问 URL。

需要注意：

- 外部 URL 可能过期，prompt 前应尽量转换成稳定本地资源。
- 文件名、MIME、大小要保留，agent 工具或模型可能需要这些信息。
- 不同平台文件下载需要权限，adapter 应负责拿到可读内容或返回清晰错误。

## 状态持久化

当前已有 `channel:user -> sessionID` 的持久化。长期建议把 session store 扩展为 route store：

```json
{
  "bindings": {
    "tg-personal:private:12345:user:12345": {
      "channelId": "tg-personal",
      "conversationId": "12345",
      "senderId": "12345",
      "agentId": "miya",
      "sessionId": "sess_...",
      "cwd": "...",
      "visibility": "normal",
      "updatedAt": "..."
    }
  }
}
```

后续如果由 `miya-desktop` 托管，session ownership 可以迁移到 desktop conversation service，`miya-channels` 只保留 channel connector 和 delivery adapter。

## 并发模型

当前 worker 是单队列顺序处理。长期需要按 source 或 session 做并发控制：

- 同一个 source 同时只允许一个 active prompt，避免上下文交错。
- 不同 source 可以并行。
- `/stop` 应取消当前 source 的 active prompt，而不一定关闭 session。
- ACP notification handler 仍应是 client 级单例，通过 `sessionID -> active route` 分发。

推荐模型：

```text
IncomingEvent
  -> source queue
  -> source worker
  -> prompt controller
  -> active session route
```

如果多个 agent endpoint 各自有 ACP client，则 notification routing 还需要包含 `agentID`：

```text
agentID + sessionID -> active delivery stream
```

## 权限和安全

外部聊天渠道比 desktop 更容易误触发敏感操作，因此 policy 需要先设计出来：

- 哪些 channel/user 可以使用哪些 agent。
- 哪些 channel/user 可以看到 tool call、thought、usage。
- 是否允许 agent 执行写文件、命令、网络访问等高风险工具。
- 群聊里是否允许执行有副作用的工具。
- 文件下载、保存、传给 agent 前是否需要大小限制和类型限制。

短期可以只做 allowlist：

```json
{
  "access": {
    "allowUsers": ["12345"],
    "allowGroups": ["67890"]
  }
}
```

长期应把权限下沉到 route policy，并与 agent/tool permission 体系对齐。

## 建议的演进步骤

### Phase 1: 数据模型准备

- 引入 `ChannelInstance`，将 `id` 与 `type` 拆开。
- `channels[]` 是唯一配置模型，不提供旧 map 迁移。
- 把 `IncomingMessage.From` 语义从 channel type 迁移为 channel instance ID。
- 扩展 source key，至少包含 `channelID`、`conversationID`、`senderID`。

### Phase 2: 事件和命令解耦

- 用 `IncomingEvent` 替代文本优先的 `IncomingMessage`。
- 增加 command router，统一处理 `/new`、`/reset`、`/stop`。
- Telegram command menu 由 adapter 注册，但命令语义由 gateway 执行。

### Phase 3: 多 agent 路由

- 引入 agent registry。
- 支持 channel 默认 agent。
- 支持 source 级 agent binding。
- 增加 `/agent` 查询和切换能力，并受 policy 控制。

### Phase 4: ACP event delivery policy

- 把 ACP update 转成 `AgentEvent`。
- 增加 `simple`、`normal`、`verbose`、`debug` visibility profile。
- 增加 channel-neutral coalescer 和 renderer。
- 保持 channel adapter 只处理平台格式和发送能力。

### Phase 5: 多模态和富投递

- 支持图片、文件、音频输入到 ACP `ContentBlock`。
- 支持 agent 输出图片、文件、resource link 到各渠道。
- 为不同平台实现文件上传、卡片、thread、reaction 等能力映射。

### Phase 6: Desktop integration

- `miya-desktop` 通过 API 创建一个 gateway 实例，传入完整 config。
- Gateway 生命周期由 desktop 管理，desktop 启动时创建，关闭时销毁。
- Desktop 拥有 session/conversation ownership，负责 agent 路由、权限和展示决策。
- `miya-channels` 在托管模式下不需要自己读配置，完全由 desktop 控制。

## 已确认的设计决策

### 配置模型

- miya-channels 扩展 miya-agents 的配置，合并为一个 `config.json`。主要增加 `channels[]` 描述多个渠道实例，读取 `agents[]` 识别多个 ACP 服务器，以及 `logging` 日志配置。
- 独立运行时：miya-channels 自己读取完整配置文件，配置中必须包含 agents 的 ACP endpoint 信息。
- Desktop 嵌入时：desktop 创建一个 gateway 实例，管理所有 channels 和 agents。gateway 通过 API 接收 config 创建，不自己读文件。

### Desktop 集成模式

- Desktop 只创建一个 gateway 实例，内部管理所有 channel 和 agent 连接。
- Gateway 的生命周期由 desktop 管理（desktop 启动时创建，关闭时销毁）。
- Desktop 拥有 session/conversation ownership，外部 channel 事件先进入 gateway，再由 desktop 决定路由和展示。

### ACP Session 失效处理

- 不做主动 health check，prompt 失败时如果 ACP 返回 session 不存在，自动创建新 session 重试。

### 多 Agent 切换

- Agent 是顶层实例，每个 agent 有自己的 ACP client，session 绑定在 agent 上。
- `/agent` 切换本质是 source 绑定的 agent ID 变了，session 随 agent 走。
- 切回原 agent 时，原来的 session 仍在，不需要跨 agent 迁移上下文。
- Route store 里只需更新 `agentID`，然后用新 agent 的 ACP client 创建或恢复 session。

### Agent Registry

- 静态配置：配置文件中列出所有可用 ACP 服务器的 endpoint，gateway 启动时加载，不做动态发现。

### 多模态文件处理

- 暂不考虑 gateway 级文件 storage，聊天客户端的消息直接发给 ACP agent。
- 文件存储策略在需要时再设计。

### 群聊权限控制

- 长期需要支持用户角色控制（如管理员可切换 agent，普通用户只能发消息）。
- 短期不做，数据模型为后续扩展预留。

### 命令语义

- `/new`：创建全新 session，旧 session 丢弃或归档。
- `/reset`：重置当前 session 状态（清空上下文但保留 session ID），暂不实现。

### Delivery 可见性

- 支持多级可见性（simple/normal/verbose/debug），具体默认级别不重要，只要设计上支持。

### 群聊路由策略

- 不暴露过多路由配置项，gateway 内部决定消息怎么分，外部只关心最终结果：这条消息对应哪个 agent & session。
- 具体策略（per_user、per_conversation、mention_only 等）在 gateway 内部实现，可按平台类型分别配置，但对外接口简化为 source -> (agentID, sessionID)。

### 错误处理

- 当前阶段不做重试、兼容或 dead letter queue，所有错误直接暴露给用户。
- 不隐藏任何潜在错误，方便开发阶段定位问题。

### 并发模型

- 单 prompt 模型：一个 source 同一时间只允许一个 active prompt，source 级排队。
- 不支持 ACP session 并发 prompt，避免 session.messages 乱序风险。

### 可观测性

- 当前阶段使用日志，不做链路追踪。

### WeChat 适配

- WeChat Bot 使用官方协议，已实现客户端 [wechatbot-go](https://github.com/lsongdev/wechatbot-go)，可直接修改维护。
- 不需要特殊健康检查或重登录状态机。

## 开放问题

（暂无）

## 推荐结论

长期方向建议是：

1. 支持多个同类型 channel，且用 `channelID` 作为实例主键。
2. channel adapter 只做平台协议，ACP 和 agent 细节放在 gateway core。
3. 引入统一 incoming event、agent event、delivery item 三层模型。
4. 命令由 gateway 统一处理，Telegram command 只是平台展示。
5. 默认每个 channel instance 绑定一个 agent，同时允许 source 级覆盖和受控切换。
6. 对外部 IM 默认隐藏 raw thought，只暴露 final message 和必要进度。
7. 为 desktop integration 预留边界：未来 session/conversation ownership 可以上移到 desktop。
