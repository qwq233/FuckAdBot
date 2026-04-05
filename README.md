# FuckAd - Telegram Anti-Spam Bot

面向 Telegram **频道评论区**场景的反广告机器人。

## 功能

- **黑名单检测**: 自动匹配用户 username / 姓名 / bio（best-effort）中的黑名单关键词，命中则删除消息并 ban 用户
- **Cloudflare Turnstile 验证**: 未验证用户发言时消息被删除，收到含三枚按钮的临时提醒（上方验证、下方左批准右拒绝）。提醒会至少保留完整的验证窗口，并回复到该评论所属帖子的根消息。
- **渐进式处罚**: 每次提醒后有 5 分钟验证窗口，超时未验证计为一次未验证发言，3 次后自动 ban
- **管理员命令**: 热添加/删除黑名单、批准/拒绝用户验证
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
|------|------|
| `/addblocklist <词汇>` | 添加黑名单关键词 |
| `/delblocklist <词汇>` | 移除黑名单关键词 |
| `/listblocklist` | 查看所有黑名单词汇 |
| `/approve <uid>` | 批准用户验证（也可回复消息使用） |
| `/reject <uid>` | 拒绝用户验证，后续消息静默删除 |
| `/unreject <uid>` | 撤销拒绝，允许用户重新验证 |
| `/stats` | 查看统计信息 |

提醒消息上的按钮布局：第一行是被审核用户自己的验证按钮；第二行左侧是管理员批准按钮，右侧是管理员拒绝按钮。非管理员点击审批按钮会被直接拒绝。

## 消息处理流程

```
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
  ├─ 5分钟验证窗口内 → 静默删除
  │
  └─ 无活跃窗口 →
      ├─ 未验证次数 < 3 →
      │   ├─ 删消息 + 发提醒(含验证链接, 30s自删)
      │   └─ 5分钟后检查: 未验证 → 计数+1 → 达到3次 → ban
      │
      └─ 未验证次数 ≥ 3 → 删消息 + ban
```

## License

AGPL-3.0-or-later
