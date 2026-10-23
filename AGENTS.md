# 部署与服务关系

## 本项目（Sub2API）

- 负责账号池、分组、调度、额度和用户权限；账号由现有管理接口接收，不内嵌 toSub2 登录页面或新增专用业务接口。
- 生产服务器：Vultr 东京 `45.77.27.100`，实例 `sub2api-tokyo`。运维目录 `/opt/sub2api` 指向 `/opt/sub2api-trial`，Compose 配置为 `/opt/sub2api/compose.production.json`。
- 本项目与 `DomenLee/toSub2` 均以各自的 `main` 为唯一正式集成分支；本项目生产发布规则见下文。

## 版本号规范

- 生产展示版本必须是标准稳定版本 `X.Y.Z`，页面可统一加 `v` 前缀，例如 `v0.2.18`。
- 版本唯一来源为 `backend/cmd/server/VERSION`，升级时先核实上游最新稳定 Release，不把上游 main 未发布代码当作最新稳定版。
- 不得把功能名、时间戳、提交哈希、分支名或构建流水号拼进 `main.Version`，不得再出现 `0.2.16+openai-auto-group-...` 一类展示版本。
- 提交追踪使用 `main.Commit`、`main.Date`、OCI 镜像标签和完整 SHA 镜像 tag；版本号与发布标识分开。
- GitHub 构建必须显式注入校验后的版本、当前完整提交 SHA 和 UTC 构建时间；上线后核对真实运行版本与提交。

## 分支与发布规范

- 业务改动、修复及上游升级必须通过 Pull Request 合并到本仓库 `main`；不得以 `custom-ui` 或其他分支作为生产发布分支，不直接推送 main 绕过 PR。
- 开发分支使用 `codex/` 前缀；PR 只允许以 main 为目标，合并前通过发布策略、后端、前端、部署脚本及安全检查。
- 生产发布唯一链路为：PR → main → GitHub Actions CI → GitHub 构建并推送 GHCR → GitHub Actions 部署 Vultr → 健康及版本验收。
- 仅 main 可触发生产构建和部署；手动重跑 GitHub 工作流也必须选择 main。服务器部署入口会再次核验目标 SHA 是远端 main 最新提交。
- 生产镜像必须为 `ghcr.io/domenlee/sub2api:<main 完整提交 SHA>`，不得用 latest 作为实际部署标识。
- 禁止用本机编译二进制、未提交工作区、临时补丁镜像、SCP 覆盖应用或服务器现场源码构建替代 GitHub 发布；不得用 `[skip ci]` 跳过生产发布检查。
- 发布前保存数据库、Compose 和旧镜像信息；仅重建主应用，不顺带重启数据库、Redis、充值、审核或独立 toSub2 服务。
- 健康、版本、镜像或启动检查失败必须保留日志并回滚旧应用配置和镜像；不得自动恢复数据库覆盖新写入。
- 服务器部署守卫等运维基础设施的安装需单独核验，不得将其描述为一次应用发布。
- 新增行为默认关闭，发布不擅自启用功能或改动密钥；工作区其他任务改动不得未经确认混入发布。

## toSub2（独立服务）

- 源码：`https://github.com/DomenLee/toSub2` 的 `main`；本机独立目录 `/Users/lifengdem1pro/Documents/students/toSub2`。
- 部署：同服务器独立 Docker Compose 项目 `tosub2`，目录 `/opt/tosub2`。容器端口仅绑定 `127.0.0.1:4399`，Caddy 提供 HTTPS 公网入口 `https://tosub.gptplusch.store`，整个站点需要独立登录认证。
- DNS：Cloudflare 的 `tosub` A 记录指向 `45.77.27.100`，开启代理。
- 用户在 toSub2 完成登录、验证码/2FA 和重新授权；每次成功后通过 Docker 内网调用本项目现有账号接口，按默认配置新增或更新对应账号。
- 已有账号只更新凭据并清除登录错误，不覆盖分组、代理、额度等管理配置；失败时在 toSub2 页面显示账号、任务和重试入口。
- 访问密码、加密密钥和本项目集成密钥仅保存在服务器 `/opt/tosub2/secrets`，不写入仓库或浏览器本地存储。首次发布以固定的 `main` 提交构建，更新前必须通过安全与同步测试。
- 首版已上线（2026-10-23）：`main` 提交 `8548397e89bf80e508eda64a87e0d556d43abc84`，镜像 `tosub2:8548397-server-v1`。HTTPS、访问认证、内网接口及容器健康已验证；真实 OpenAI 登录推送尚待验收。
- 登录名 `admin`，入口密码保存在本机 `/Users/lifengdem1pro/.codex/credentials/tosub2/console.password`，不写入本文件。详细部署说明见 `/Users/lifengdem1pro/Documents/students/toSub2/deploy/server/README.md`。
- GitHub Actions 自动发布尚未启用，不得把首版手工部署记成已实现自动发布。

## 运维连接

- 本机入口：`/Users/lifengdem1pro/.codex/credentials/sub2api-vultr/ssh.sh '<命令>'`，使用 root、22 端口、严格主机校验，并绑定 `en0` 避免本机代理链路断开。
- 用户授权保存的 SSH 密码位于该脚本同目录的 `root.password`，权限 0600；仅由 askpass 使用，禁止输出或提交。
- 修改服务前核对实时镜像和配置，避免覆盖其他任务的部署；不重置现有凭据，不整体发布带其他任务改动的工作区。
