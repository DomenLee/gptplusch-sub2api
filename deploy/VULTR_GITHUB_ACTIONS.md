# GitHub Actions → Vultr 自动部署

`main` 是唯一正式集成及生产发布分支。所有改动先通过 Pull Request 合并到 `main`，再由 GitHub Actions 执行版本校验、CI、GHCR 镜像构建和 Vultr 部署。生产只部署 `ghcr.io/domenlee/sub2api:<main 完整提交 SHA>`，不使用 `latest` 部署，也不从 `custom-ui` 发布。

禁止使用本机二进制、工作区打包、SCP 覆盖应用或服务器现场源码构建代替 GitHub 构建部署。`workflow_dispatch` 手动重跑也必须选择 `main`。服务器部署入口再次检查 GitHub 远端 main 最新 SHA，不接受其他分支或已过期的发布提交。

## 版本规范

- `backend/cmd/server/VERSION` 保存标准稳定版本，例如 `0.2.18`；页面展示为 `v0.2.18`。
- 版本必须与本次升级的上游稳定 Release 一致。上游 tag 对应源码的 VERSION 可能滞后一版，合并后应显式校正，不能只依赖构建 checkout 恰好落在 tag 上。
- GitHub 在构建前验证版本只包含 `X.Y.Z`，显式注入 Docker build args `VERSION`、`COMMIT`、`DATE`。
- 功能名、时间戳、分支名、构建号及提交 SHA 不得追加到展示版本；追踪信息保存在独立构建字段与 OCI 标签中。
- `deploy/check-production-release.sh` 和对应测试负责拒绝非 main 来源、版本后缀、标签版本冲突及 checkout SHA 不一致。

## GitHub 仓库配置

仓库 Variables：

| 名称 | 当前值 |
| --- | --- |
| `VULTR_HOST` | `45.77.27.100` |
| `VULTR_PORT` | `22` |
| `VULTR_USER` | `sub2api-deploy` |

仓库 Secrets：

- `VULTR_SSH_PRIVATE_KEY`：专用 Ed25519 部署私钥。
- `VULTR_SSH_KNOWN_HOSTS`：通过现有可信管理连接取得的服务器公钥，格式为 `45.77.27.100 ssh-ed25519 <服务器公钥>`。使用严格主机校验，不在每次部署时临时信任 `ssh-keyscan` 的结果。

私钥仅在 Runner 临时目录中落盘，权限由 `umask 077` 限制；作业结束时删除临时凭据。应用 JWT、TOTP、数据库和 Redis 密钥继续保留在 Vultr 的生产配置中。

## Vultr 部署入口

- 运维目录：`/opt/sub2api`，实际指向 `/opt/sub2api-trial`。
- 生产配置：`compose.production.json`，保留迁移后的数据库、挂载、网络及密钥。
- 专用系统账号：`sub2api-deploy`，无 Docker 组权限。
- SSH 强制命令：`/usr/local/bin/sub2api-deploy-command`，来自 `deploy/vultr-ssh-command.sh`。
- root 部署脚本：`/usr/local/sbin/sub2api-vultr-deploy`，来自 `deploy/vultr-ssh-deploy.sh`。
- 公钥通过 `restrict,command="/usr/local/bin/sub2api-deploy-command"` 禁止交互终端、端口转发及任意远程命令。
- sudo 仅允许执行上述 root 部署脚本；它只接受本仓库镜像和完整提交 SHA。

服务器脚本由管理员安装，工作流不从 SSH 输入或远程 URL 下载并执行任意脚本。修改部署脚本时，需要同时更新服务器上的对应文件；修改应用代码无需此操作。

## 每次发布行为

1. 核验目标是 GitHub 远端 main 最新提交，获取独占发布锁，拉取指定提交镜像。
2. 从当前应用读取实际数据库名，备份 PostgreSQL、生产 Compose 和当前容器配置至 `release-backups/<时间>-<SHA>/`。
3. 仅替换生产 JSON 中的 `services.sub2api.image`，保留全部其他运行配置。
4. 仅重建应用容器，PostgreSQL、Redis、Caddy、CDK 和审核服务保持运行。
5. 等待健康状态，检查实际镜像、数据库连通性和启动日志。
6. 失败时恢复旧 Compose 和旧应用镜像。数据库备份保留，恢复数据需要人工处理，避免覆盖新写入。

## 发布验收与失败处理

- PR 的目标分支必须为 main，CI 检查通过后再合并；禁止使用 `[skip ci]` 跳过发布检查。
- main 的 `Build Docker Image` 工作流全部成功后，核对运行容器镜像 tag 与本次 main SHA 相同。
- 检查 `/health`、容器健康、数据库连通性、启动迁移日志、`/api/v1/admin/system/version` 的版本，以及镜像和二进制中的提交信息。
- 升级至 v0.2.18 会执行上游迁移 246。混合分组若提供 Gemini 生图，需确认其「允许当前分组生图」开关；不要自动覆盖业务配置。
- 失败时使用工作流和服务器 release-backups 中的日志定位；服务器脚本自动回滚应用，数据库恢复需要单独确认，不能覆盖新写入。
- 新功能仍默认关闭；上线不等于获准启用 GPT 自动分组或更改既有功能配置。
- 旧 AWS 应用和旧部署锁继续保留，不应解除旧锁或恢复旧站写入。
