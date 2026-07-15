module github.com/lsongdev/miya-channels

go 1.26.1

require (
	github.com/lsongdev/feishu-go v0.0.0-20260319163837-39674c462916
	github.com/lsongdev/miya-agents v0.0.0-20260612032442-0e7c5fff6788
	github.com/lsongdev/telegram-go v0.0.0-20260320025417-8942b286b9e9
	github.com/lsongdev/wechatbot-go v0.0.0-20260324073854-535a867c66ad
	github.com/lsongdev/wecom-go v0.0.0-20260319113224-a7efceeaed8a
)

require (
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/lsongdev/jsonrpc-go v0.0.0-20260311082853-00cacf7253d3 // indirect
	github.com/yuin/goldmark v1.7.16 // indirect
)

replace github.com/lsongdev/miya-agents => ../miya-agents
