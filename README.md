# FuckAd - Telegram Anti-Spam Bot

面向 Telegram **频道评论区**场景的反广告机器人。

## 功能

- **黑名单检测**: 自动匹配用户 username / 姓名 / bio（best-effort）中的黑名单关键词，命中则删除消息并 ban 用户
- **Cloudflare Turnstile 验证**: 未验证用户首次发言时，机器人会直接回复该用户的消息发送验证提示；在验证窗口内该用户后续消息会被立即删除。若在 `original_message_ttl` 内仍未完成验证，则首条待验证消息会被删除。提醒会至少保留完整的验证窗口。
- **渐进式处罚**: 每次提醒后有 5 分钟验证窗口，超时未验证计为一次未验证发言，3 次后自动 ban
- **管理员命令**: 热添加/删除黑名单、批准/拒绝用户验证，以及仅 bot 超级管理员可见的运行态 `/health` `/stats`
- **频道评论区适配**: 支持非群成员通过频道评论的场景

## 快速开始

### 1. 编译

```bash
go build -o fuckad ./cmd/bot/
```

### 2. 配置

复制并编辑配置文件:

```bash
cp config-example.toml config.toml
# 编辑 config.toml，填入 bot token、Turnstile keys 等
```

PowerShell:

```powershell
Copy-Item config-example.toml config.toml
```

`[store]` 支持 3 种模式：

- `type = "sqlite"`: 单库模式，主库固定写入 `<data_path>/fuckad.db`
- `type = "redis"`: Redis 主库模式
- `type = "sqlite"` 且 `dual_write_enabled = true`: SQLite 主库 + Redis 读缓存双写，重试队列固定写入 `<data_path>/redis-sync-queue.db`

双写的 flush/batch/queue 深度等调优参数已固化在代码常量中，不再作为运行时配置暴露。

### Benchmark / Loadtest Redis

复制 `testing-example.toml` 为 `testing.toml` 后，`internal/store` 的 benchmark 和 `cmd/loadtest` 在 `redis` / `dual-write` 模式下会优先使用 `[redis]` 里配置的真实 Redis 实例；如果 `testing.toml` 不存在，则继续自动回退到内置 `miniredis`。

`testing.toml` 只用于本地测试，不应提交到仓库；代码会为每次 benchmark / loadtest 生成独立的 key prefix，并在 `cleanup = true` 时自动清理测试键。

### 3. 运行

```bash
./fuckad
```

## Bot 设置

1. 通过 @BotFather 创建 Bot，获取 token
2. 将 Bot 添加到频道关联的讨论群，设为**管理员**，需要以下权限:
   - `can_delete_messages` (删除消息)
   - `can_restrict_members` (封禁用户)
3. 在 [Cloudflare Dashboard](https://dash.cloudflare.com/) 创建 Turnstile widget，获取 site key 和 secret key
4. `turnstile.domain` 必须填写一个专用域名，例如 `verify.example.com`。程序固定使用 [internal/config/config.go](internal/config/config.go#L13) 和 [internal/config/config.go](internal/config/config.go#L14) 定义的 `/verify` 与 `/verify/callback` 路径，不再接受自定义回调路径。

## 管理员命令

| 命令 | 说明 |
| ---- | ---- |
| `/addblocklist <词汇>` | 添加黑名单关键词 |
| `/delblocklist <词汇>` | 移除黑名单关键词 |
| `/listblocklist` | 查看所有黑名单词汇 |
| `/approve <uid>` | 批准用户验证（也可回复消息使用） |
| `/reject <uid>` | 拒绝用户验证，后续消息静默删除 |
| `/unreject <uid>` | 撤销拒绝，允许用户重新验证 |
| `/resetverify <uid>` | 清空用户在所有聊天室中的验证状态，仅超级管理员可用 |
| `/health` | 查看简要运行健康状态，仅 `bot.admins` 可用 |
| `/stats` | 查看详细运行统计，仅 `bot.admins` 可用 |

提醒消息上的按钮布局：第一行是被审核用户自己的验证按钮；第二行左侧是管理员批准按钮，右侧是管理员拒绝按钮。非管理员点击审批按钮会被直接拒绝。

## 运行时加固

- 验证窗口恢复改为单个 sweeper 周期扫描，不再为每条 pending 记录恢复多组 timer
- 群管理员检查带 TTL 缓存，减少热路径 `GetChatMember` 请求
- Captcha HTTP 服务默认启用读写超时、Header 限制和 1 MiB 表单体积限制
- `/health` 与 `/stats` 会输出 store 模式、pending backlog、dual-write 队列、admin cache 命中情况和 captcha 成败计数

## 消息处理流程

```text
用户发评论
  │
  ├─ 频道自动转发/匿名管理员 → 放行
  │
  ├─ 黑名单匹配(username+name+bio)
  │   └─ 命中 → 删消息 + ban
  │
  ├─ 已验证 → 放行
  │
  ├─ 已被管理员拒绝 → 静默删除
  │
  ├─ 5分钟验证窗口内 → 静默删除后续消息
  │
  └─ 无活跃窗口 →
      ├─ 未验证次数 < 3 →
       │   ├─ 保留首条消息 + 回复式提醒(含验证链接)
       │   ├─ 1分钟后仍未验证 → 删除首条待验证消息
       │   └─ 5分钟后检查: 未验证 → 计数+1 → 达到3次 → ban
      │
      └─ 未验证次数 ≥ 3 → 删消息 + ban
```

## License

AGPL-3.0-or-later
