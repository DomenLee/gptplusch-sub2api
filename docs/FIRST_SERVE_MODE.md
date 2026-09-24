# 首服模式

首服模式是 OpenAI 单账号的 WebSocket 模式，适用于客户端通过 Responses WebSocket 连续请求的场景。普通 HTTP 请求不启用这套组合轮换策略。

## 开启

1. 在代理管理中准备至少两个可用、出口 IP 不同的代理，加入同一代理组。不同代理记录不保证出口 IP 不同，请核对代理检测结果。
2. 新建或编辑目标 OpenAI 账号，在 **WS mode → 首服模式** 下的专属配置卡片选择代理组、参数和允许的代理，保存。编辑页在同一卡片内显示运行状态。
3. 网关需启用 `gateway.openai_ws.enabled`、相应账号类型的 `oauth_enabled` / `apikey_enabled`、`responses_websockets_v2` 和 `mode_router_v2_enabled`。强制 HTTP、插件 HTTP 桥接不能运行首服模式。
4. 通过客户端 WebSocket 发起会话。账号编辑页可查看当前网关节点最近 20 个会话的代理、连接、首 token 延迟、到期时间和切换原因，页面每 10 秒刷新。

## 账号级配置

配置保存在 `accounts.extra.openai_first_serve`，不需要数据库迁移。旧账号缺少配置时继续使用原默认值。仅选中首服模式时显示配置卡片。

| 配置项 | 默认值 | 可配置范围 |
| --- | --- | --- |
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
- 在下一轮请求边界选择另一个可用代理并新建上游连接，当前输出不被中断。没有下一轮请求时不主动发起探测或生成请求。
- 新连接通过完整历史建立新响应链，后续 `previous_response_id` 仍逐轮推进。不会把某个响应 ID 固定为所有请求的父节点。
- 会话上下文与连接独立管理；不会在同账号的不同用户之间共享历史。重连能命中原连接时继续使用该连接的期限和历史。
- 达到配置的连续切换上限仍偏慢时进入冷却，再在后续请求重试（默认 3 次 / 60 秒）；没有其他可用代理时保留当前组合，冷却后再尝试选择。
- 历史不完整、工具调用缺少对应上下文、存在无法恢复的引用/推理项，或本地完整历史超过 8 MiB 时延后轮换，保持续聊。页面明确提示处理方式。

首 token 延迟从本轮写入上游请求开始统计；连接建立、网关排队时间不包含在该 WebSocket 指标内。没有识别到 token 的终止事件不会被误记为首 token。

## 边界与回退

组合轮换不能保证提速。上游负载、模型和思考强度都会影响首 token；换连接也可能损失缓存。比较效果时应保持模型、思考强度和输入规模相近，同时观察错误率与输入 token 消耗。

运行状态和连接历史存放在当前进程内。多节点部署应保持客户端连接落在原节点；状态页面显示处理该次查询的节点数据。进程重启或连接关闭后不能保证恢复旧连接的内存历史，客户端需要提供完整上下文或开启新会话。

账号改回 **上下文池** 即关闭该模式，后续新客户端连接使用原行为。参数保存后用于新建会话，已经建立的客户端 WebSocket 保留建立时的配置。参数或代理组变化后不会复用旧策略的连接；若客户端携带旧 `previous_response_id` 重连并提示连接不可用，需要新建会话或发送完整历史。运行状态提示使用每个会话实际生效的参数。无需数据库迁移或新增依赖。

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
