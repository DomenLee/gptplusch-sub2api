# 首服模式

首服模式是 OpenAI 单账号的上游会话标识与代理组合轮换策略，支持 HTTP/SSE 流式请求及 Responses WebSocket。HTTP 支持 `/v1/responses`（含透传）、`/v1/chat/completions` 和 `/v1/messages`，客户端不需要改用 WebSocket。

## 开启

1. 在代理管理中准备至少两个可用、出口 IP 不同的代理，加入同一代理组。不同代理记录不保证出口 IP 不同，请核对代理检测结果。
2. 新建或编辑目标 OpenAI 账号，在 **WS mode → 首服模式** 下的专属配置卡片选择代理组、参数和允许的代理，保存。编辑页在同一卡片内显示运行状态。
3. HTTP/SSE 不需要全局 WebSocket 开关，也允许账号启用强制 HTTP。客户端使用 WebSocket 时，仍需启用 `gateway.openai_ws.enabled`、相应账号类型的 `oauth_enabled` / `apikey_enabled`、`responses_websockets_v2` 和 `mode_router_v2_enabled`；WS HTTP 桥接不执行 WS 首服策略。
4. 使用此账号发起流式请求。账号编辑页会在请求开始后显示组合，标注 HTTP / SSE 或 WebSocket，以及代理、组合 ID、首 token 延迟、到期时间和切换原因，页面每 10 秒刷新。非流式、压缩请求不展示在首服状态中。

## 账号级配置

配置保存在 `accounts.extra.openai_first_serve`，不需要数据库迁移。旧账号缺少配置时继续使用原默认值。仅选中首服模式时显示配置卡片。

| 配置项 | 默认值 | 可配置范围 |
| --- | --- | --- |
| HTTP 组合复用范围 | 按会话独立 | 按会话独立 / 同账号所有会话共享 |
| 组合使用时长 | 30 分钟 | 1–1440 分钟，整数 |
| 首 token 阈值 | 15 秒 | 1–300 秒，整数 |
| 连续切换上限 | 3 次 | 1–20 次，整数 |
| 冷却时间 | 60 秒 | 1–3600 秒，整数 |
| 允许代理 | 全组可用代理 | 全组 / 仅指定代理；指定模式至少选 2 个，最多 1000 个 |

代理列表展示名称、代理地址和已检测的出口 IP，不展示代理密码。指定模式以现有代理 ID 保存，并限制在账号绑定的代理组内；首次连接和后续轮换都遵守此范围，停用、过期或已删除的节点不参与新连接。修改代理组不会自动清空白名单或扩大范围，必须明确重新选择。出口 IP 为已有检测结果，不代表永久固定。

“连续切换上限”和“冷却时间”放在高级设置中；首 token 达标后重置连续切换次数。暂时没有其他可用代理时也使用同一冷却时间。

API 配置示例（`extra` 中的一个字段）：

```json
{
  "openai_first_serve": {
    "reuse_scope": "account",
    "ttl_minutes": 30,
    "ttft_seconds": 15,
    "max_switches": 3,
    "cooldown_seconds": 60,
    "proxy_mode": "selected",
    "proxy_ids": [101, 102]
  }
}
```

使用全组时设 `proxy_mode: "all"` 和 `proxy_ids: []`；`selected` 的空列表会被拒绝，不会回退到全组。新增/编辑/复制账号时校验指定代理归属，运行时数据库再次限制组成员及可用状态。

## 运行规则

- 首 token **严格超过账号配置的阈值**时标记待切换；等于阈值继续使用。默认阈值为 15000 毫秒。采用全局首 token 统计口径，忽略连接建立和响应创建等元数据事件。
- 组合从建立时起按配置时长到期，默认 **30 分钟**，正常请求不续期。
- 在下一轮请求边界选择另一个可用代理并更新上游会话标识（WebSocket 同时新建连接），当前输出不被中断。没有下一轮请求时不主动发起探测或生成请求。
- WebSocket 新连接通过完整历史建立新响应链，后续 `previous_response_id` 仍逐轮推进。不会把某个响应 ID 固定为所有请求的父节点。
- 会话上下文与连接独立管理；不会在同账号的不同用户之间共享历史。重连能命中原连接时继续使用该连接的期限和历史。
- 达到配置的连续切换上限仍偏慢时进入冷却，再在后续请求重试（默认 3 次 / 60 秒）；没有其他可用代理时保留当前组合，冷却后再尝试选择。
- WebSocket 历史不完整、工具调用缺少对应上下文、存在无法恢复的引用/推理项，或本地完整历史超过 8 MiB 时延后轮换，保持续聊。页面明确提示处理方式。

首 token 延迟采用现有网关统计口径：WebSocket 从本轮写入上游请求开始统计，不包含建连和排队时间；HTTP 从转发处理开始统计，包含请求准备及上游建连时间。压缩请求不参与首 token 阈值判断。普通 Responses、透传及 Responses 兼容转换路径在识别首 token 时即更新状态；其他上游协议在本次请求结束后更新。未记录首 token 的请求不标记达标，失败会提示检查账号与代理。

## 同账号共享组合

在 **账号编辑 → 首服模式配置 → HTTP 组合复用范围** 选择 **同账号所有会话共享组合** 并保存。API 字段为 `reuse_scope: "account"`；旧配置缺少该字段时仍按 `session` 独立复用，避免自动改变现有账号行为。仅影响首服账号的 HTTP 请求，WebSocket 连接与历史继续独立管理。

共享模式中，同一上游账号的不同 API key、用户分组、会话及无标识请求，共用当前进程中的一个路由 ID（`session_id` / Responses `prompt_cache_key`）和代理。30 分钟到期或首 token 超阈值时，依照原有阈值、冷却和白名单规则切换；其他上游账号保持独立。状态页展示一条共享组合及该组合接收的流式请求次数。修改配置后开始新的组合，旧诊断记录可能继续显示一段时间。

共享的是路由亲和信息，不合并任何请求的输入或工具历史，也不把 `previous_response_id` 固定成同一个值。`conversation_id` / 线程标识仍按账号、API key、用户分组和原始会话隔离；缺少原始会话标识时生成独立对话标识。出站 `x-codex-turn-state` 回显会被清除，避免客户端旧路由状态覆盖共享组合。上游是否按这些标识复用资源由上游决定，不能保证延迟下降。

## HTTP / SSE 会话识别（按会话模式）

- 用账号、API key、用户分组、客户端会话标识和配置指纹隔离组合，不跨账号或租户复用。
- 优先识别客户端 `thread-id`、Codex 线程元数据，其次 `session_id` / `session-id` 请求头或请求体 `prompt_cache_key`；也识别 `client_metadata.session_id` 和 Claude Code 的 `metadata.user_id` 内会话 ID。同一对话每轮需携带相同标识；不同对话使用不同标识。不要用 API key 或统一常量作为所有对话的会话标识。
- 无稳定标识时，每个请求创建独立组合并显示提醒，不根据相同提问推断两个请求属于同一会话。这种情况下不能实现跨请求连续复用 30 分钟，需先让客户端携带上述标识。
- HTTP 固定的是代理及出站 `session_id` / `conversation_id`；Responses 的 `prompt_cache_key` 和已有 session 元数据随组合更新。它们是路由与缓存亲和标识，不是固定的响应 ID，也不保证第三方上游采用这些标识。
- HTTP 不缓存对话内容。若请求仍携带 `previous_response_id`，已有组合到期或偏慢时延后切换，直到客户端发来不依赖该 ID 的完整请求。首服请求改写不删除该 ID、工具结果或输入历史；账号原有的上游协议规范化规则仍适用。
- Messages 兼容转换在首服模式下发送完整客户端历史，不自动缩减为上一响应 ID 的增量续接，保证轮换后仍可继续工具调用。
- 同一会话允许并发流式请求。轮换不会中断旧流，旧组合返回的耗时不能覆盖新组合的状态。
- 内存最多保留 4096 个 HTTP 会话，空闲超过 24 小时按新请求清理；容量不足时优先淘汰空闲项，不启动额外后台定时器。被淘汰或重启后，下次请求重新建立组合。

## 如何解读“未记录首 token 耗时”

首 token 耗时是时间指标，不是返回 token 数。非流式 JSON 返回和上下文压缩请求即使返回了内容及 usage，也可能没有 `FirstTokenMs`；旧页面把这些情况统一标为 `no_token`，容易误解为无返回。旧前端遇到新后端的状态键时可能显示完整翻译键，部署后需刷新页面加载新资源。

首服状态页只展示流式请求，显示最近完成的流式请求的输入/输出 token 用量、总耗时和可用的上游请求 ID。非流式和压缩请求不占用状态列表，不计入流式请求次数，也不会覆盖已有流式请求摘要和健康计时；它们仍正常转发、计费并记录到使用记录。流式返回但未记录计时会明确提示“未记录首 token 耗时”；失败有独立提示。用量为网关已解析到的上游 usage，不能用缺少耗时判断没有返回内容。

## 使用记录中的首服标识

使用记录的请求类型旁会为实际复用了首服上游路由 ID 与代理组合的流式请求显示 **首服生效中**。首次建立组合、轮换后的首个请求、非流式、压缩请求和未启用首服的请求不打标。HTTP 内部重试只算同一次请求；并发请求各自保存使用组合时的复用结果，旧流晚完成不会被新组合状态覆盖。

标识以 `usage_logs.first_serve_active` 保存请求当时的快照，不根据账号当前配置或内存会话列表推断。升级会自动执行 `241_add_usage_log_first_serve_active.sql`，历史记录默认不打标；关闭首服或重启服务不会改写已有记录的标识。此标识说明网关复用了组合，不保证上游缓存命中或首 token 必然低于阈值。

## 边界与回退

组合轮换不能保证提速。上游负载、模型和思考强度都会影响首 token；换连接也可能损失缓存。比较效果时应保持模型、思考强度和输入规模相近，同时观察错误率与输入 token 消耗。

运行状态和连接历史存放在当前进程内。多节点部署应保持客户端连接落在原节点；状态页面显示处理该次查询的节点数据。进程重启或连接关闭后不能保证恢复旧连接的内存历史，客户端需要提供完整上下文或开启新会话。

账号改回 **上下文池** 即关闭该模式，HTTP 后续请求及新客户端连接使用原行为。HTTP 参数保存后下次请求生效，已经建立的客户端 WebSocket 保留建立时的配置。参数或代理组变化后不会复用旧策略的连接；若客户端携带旧 `previous_response_id` 重连并提示连接不可用，需要新建会话或发送完整历史。运行状态提示使用每个会话实际生效的参数。账号配置无需迁移；使用记录标识通过上述自动迁移新增字段，无新增依赖。

## 开发验证

```bash
cd backend
go test -race ./internal/service -run 'TestFirstServe|TestProxyGroupService|TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient|TestOpenAIWSProtocolResolver' -count=1
```

```bash
cd frontend
pnpm exec vitest run src/components/account/__tests__/FirstServeSettings.spec.ts src/components/account/__tests__/FirstServeStatus.spec.ts src/utils/__tests__/openaiWsMode.spec.ts src/components/account/__tests__/EditAccountModal.spec.ts src/components/account/__tests__/CreateAccountModal.spec.ts
pnpm run build
```
