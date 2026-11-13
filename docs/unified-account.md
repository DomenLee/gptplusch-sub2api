# 通用账号改造（本地测试分支）

本项目为商城与 AI 站提供共同的邮箱/密码凭据。两站登录时效、会话、JWT/刷新令牌、角色和业务资产独立。详细需求在商城项目 `docs/account-unification-requirements.md`；测试记录在 `docs/account-unification-local-test.md`。

## AI 项目范围

- 新增 `internal/identity`：邮箱验证码、密码、版本和唯一别名、历史账号关联、改密/找回/换邮箱、审计和限流。
- 统一账号与 AI 业务用户通过映射关联，不把商城注册直接变成 AI 用户。首次 AI 登录仍执行注册开关、邮箱规则、邀请码及权益初始化。
- 新增内部服务接口，使用 `X-Unified-Key`；用户接口继续使用 AI 自己的 JWT 和 TOTP。
- 用户仓储加载映射后的当前邮箱与独立 AI 会话版本；密码变化不修改另一站的会话版本。
- 新建 AI 用户必须走已验证流程；已有 OAuth 登录保留，绑定账号仍需已有认证流程。已映射账号的旧密码/邮箱写接口不能绕过通用设置。
- 注册/邮箱验证页增加 challenge/request ID；个人设置提供关联、改密、换邮箱、退出本站全部设备；独立找回页面保留旧找回入口。
- 换邮箱通过受保护的商城邮箱检查接口确认无历史账号冲突；失败保留原邮箱。

## 服务端配置

默认不启用。两站都完成兼容改造后，服务端设置 `UNIFIED_ACCOUNT_ENABLED=true`，并配置一致的 `UNIFIED_ACCOUNT_KEY`（至少 32 字符）。AI 另外设置 `UNIFIED_STORE_URL` 为商城 API 所在服务的基础地址；内部路径为 `/api/auth/unified/email-check`，非本机必须 HTTPS。商城配置 `UNIFIED_ACCOUNT_URL` 指向 AI 服务。不要把共同服务密钥放进 VITE 环境变量或前端。

原 JWT、数据库、Redis、TOTP 和邮件服务配置继续各自保留。安全通知复用已有邮件队列；邮件发送不改变另一站登录状态。启用后若有 AI 映射，关闭开关会触发启动保护，避免意外恢复旧凭据。

## 数据库变更

**新增迁移：`backend/migrations/247_unified_accounts.sql`、`backend/migrations/248_unified_account_operations.sql`。** 适用于 PostgreSQL，由现有启动迁移器按文件名顺序执行。`247` 新增统一账号、站点映射、验证码、限流与审计结构；`248` 新增跨站改密/找回/换邮箱请求的幂等状态表。旧用户/余额/订单表不做批量合并或资产迁移。

正式执行前备份数据库与配置；先迁移 AI，再迁移商城，确认两站代码及服务间配置后启用开关。`247` 基础迁移曾在专用本地测试库执行；`248` 尚未执行，生产库未操作。迁移检查包括统一邮箱/别名唯一、每站一个本地账号对应一个统一身份、验证码和会话版本隔离。

未关联任何账号时可以关开关并保留新增结构；一旦关联，不可直接恢复旧凭据或删除映射。回退应保留兼容代码，或经确认后在维护窗口协调恢复两站一致备份。

## 本地验证

工作分支 `codex/account-unification-test`，目录 `/Users/lifengdem1pro/Documents/students/gptplusch-sub2api-account-test`。未提交、未推送、未部署。

前端：`pnpm run build` 和现有认证/个人设置 Vitest。后端：`CGO_ENABLED=0 GOMAXPROCS=4 go build -o /tmp/gptplusch-account-ai-final ./cmd/server`，以及认证相关 service/repository/handler 测试。`internal/service/unified_account_test.go` 覆盖商城占用检查成功、冲突、错误响应和安全配置。

完整跨站 HTTP/SMTP/数据库验收脚本位于商城项目 `scripts/account-test/flows.mjs`。只使用固定本机端口及专用可丢弃 Docker 容器，不允许用于生产。真实移动端键盘、第三方 OAuth 网络和生产邮件送达仍需对应环境验收。
