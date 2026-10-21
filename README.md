<p align="center">
  <b>Loomy2API Panel</b>
</p>

<h1 align="center">Loomy2API</h1>

<p align="center">
  <b>把讯飞 Loomy 账号变成 OpenAI / Anthropic 兼容 API 的多账号网关 · 附 Web 管理面板</b><br>
  账号池轮转 · 熔断与冷却 · 会话粘性 · 流式 / 非流式 · 零外部依赖 · 单二进制
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.23-00ADD8?logo=go&logoColor=white&style=flat-square">
  <img alt="API" src="https://img.shields.io/badge/API-OpenAI%20%7C%20Anthropic-412991?style=flat-square">
  <img alt="Deps" src="https://img.shields.io/badge/Dependencies-zero-brightgreen?style=flat-square">
  <img alt="Tests" src="https://img.shields.io/badge/Tests-207%20passing-brightgreen?style=flat-square">
</p>

---

## 这是什么

Loomy2API 是一个自托管的 **反向代理网关**，把讯飞 Loomy（`loomyad.xunfei.cn`）的账号
包装成统一的、兼容 OpenAI 与 Anthropic 双协议的 API 服务。

- 官方不提供 OpenAI 形态的开放 API。本项目通过**手机号 + 密码服务端登录**拿到登录态
  （`session`），并用它作为上游 Bearer token；
- 面向**个人多账号**场景：多号共享、单号故障自动换号、冷却 / 熔断防雪崩、会话粘性保证多轮不跳号；
- 对客户端暴露 `chat_completions`、`codex_responses` 与 `anthropic_messages`，
  模型名原样交给上游。

> ⚠️ 本项目是**非官方**网关，仅限本人授权账号、本机 / 私有环境自用。
> 使用者需自行遵守上游服务条款。详见[安全与合规](#安全与合规)。

协议细节见 **[docs/PROTOCOL.md](docs/PROTOCOL.md)**。

## 核心能力

| 能力 | 说明 |
|---|---|
| 🔑 **密码自动登录** | 存手机号 + 密码，网关自己走 RSA + HMAC-SHA1 签名登录，14 天登录态到期自动重登 |
| 📅 **模拟签到领积分** | 复刻官方客户端启动即调的 `points/first-login`（幂等领每日 5000）：面板单号/全量签到按钮、登录成功自动签到、定时额度刷新顺带跨日自动续领 |
| 🎁 **新手任务直领** | 上游 8 个新手任务合计 **10000 积分**，服务端**不校验前置行为**（不用真的发对话 / 生成 PPT / 装技能），网关纯 API 直领、零 token 消耗；登录 / 签到 / 定时刷额度都会顺带补领，面板有单号与全量按钮 |
| 🚫 **失效模型台账** | 上游对「不在目录 / 已下架」的模型回 404，这与账号无关（换号、等冷却都不会好）。网关把它记成**模型级**台账并从 `/v1/models` 隐藏、请求直接 404，**不罚账号**（连失败数都不涨），30 天 TTL 自愈 + 面板可手动解除 |
| 🔒 **严格模型校验** | Loomy 对**认不出的模型名**不报错，而是静默用默认模型执行并回 200（实测 `no-such-model-xyz` → 响应 `model` 变成 `DeepSeek-V4.1-Flash`，按默认模型扣分）。网关因此在目录层面直接 404，客户端不会拿到"成功"的错答案 |
| 🆓 **模型限免识别** | 上游 `/models` 没有结构化倍率字段，倍率与「限时免费」都写在展示名里（`Spark-X2.6-preview(限时免费)(x0.0)`）——网关解析出倍率/限免标记，面板出「限免」标签、限免数统计与「限免优先」排序 |
| 🖼️ **图像模型分道** | 上游 `type=image` 的模型只能走 `/v1/images/generations`，对 `/chat/completions` 上游一律回 404。网关在目录里标注 `endpoint` / `supports_chat=false`，并在对话端点上直接 400 指路，不让一个选错模型的请求白烧上游、连带把账号打进 404 短冷却 |
| 🔄 **多账号池** | 余额加权 + 成本分层 + 加权随机选号，Top-5 候选短名单 + 防惊群；**每日赠送额度优先**（按自然日清零的那份先花，永久积分垫后） |
| 🛡️ **熔断与冷却** | 429 软冷却指数退避（封顶可配）、404 短冷却、额度耗尽硬冷却到次日 04:00、连败熔断、单号在途租约 |
| 🧲 **会话粘性** | 同一会话尽量绑定同一账号，TTL 滚动续期，失败自动解绑 |
| ⚡ **双协议** | OpenAI `chat_completions` + `responses`，Anthropic `messages`（流式、工具调用、思考链全支持） |
| 🧠 **思考链透传** | 上游 `reasoning_content` → Anthropic `thinking` 块 / Responses `reasoning` 项，不静默丢弃 |
| 🕐 **长任务不掐断** | 非流式长请求内部转流式取数再聚合，绕开上游 ~60s 的 504 |
| 📊 **可观测** | 每请求一行表格日志（TTFB / token 速率）、`/status`、`/healthz`、`/metrics`、请求台账落盘 |
| 💾 **状态持久化** | 池状态原子落盘（含额度快照：积分/每日周期/签到状态重启不丢）；用量台账 JSONL 追加，重启不清零 |
| 🖥️ **Web 管理面板** | 内嵌单页（明暗主题），六个视图与参考面板同构：账号池（签到/余额/暂停选号/解冻/模型锁池）、用量（KPI + SVG 时序图 + 按账号/模型明细）、积分构成（到期分布 + 包明细）、模型档位（限免标签 + 筛选）、配置（热生效标注）、日志（请求记录 / 错误汇总 / 积分历史） |
| 📦 **零依赖** | 全部标准库，`go build` 即可，无 cgo、无 npm、无外部构建步骤 |

## 快速开始

### 方式一：直接跑二进制

```bash
# 构建
go build -o loo2api ./cmd/server

# 启动（首次会自动生成 config.json，并在日志里打印随机 api_key）
./loo2api -config config.json
```

打开 `http://127.0.0.1:17890/panel/`，用日志里打印的 `api_key` 进入面板，
在「账号池 → 添加账号」填手机号与密码，保存即会自动登录。

### 方式二：Docker

```bash
docker compose up -d
docker compose logs -f    # 看首启生成的 api_key
```

### 客户端接入

```bash
# OpenAI 兼容客户端
export OPENAI_BASE_URL=http://127.0.0.1:17890/v1
export OPENAI_API_KEY=sk-loomy-xxxxxxxx

curl $OPENAI_BASE_URL/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"imodel/spark-x","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

```bash
# Claude Code 等 Anthropic 客户端
export ANTHROPIC_BASE_URL=http://127.0.0.1:17890
export ANTHROPIC_AUTH_TOKEN=sk-loomy-xxxxxxxx

curl $ANTHROPIC_BASE_URL/v1/messages \
  -H "x-api-key: $ANTHROPIC_AUTH_TOKEN" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"imodel/spark-x","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}'
```

```bash
# Codex / Responses API
curl http://127.0.0.1:17890/v1/responses \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"imodel/spark-x","input":"你好"}'
```

## 端点一览

| 方法 | 路径 | 协议 | 说明 |
|---|---|---|---|
| POST | `/v1/chat/completions` | OpenAI | 主对话端点（也接受无 `/v1` 前缀） |
| POST | `/v1/responses` | OpenAI Responses | Codex 等客户端 |
| POST | `/v1/messages` | Anthropic | Claude Code 等（也接受 `/messages`） |
| GET | `/v1/models` | OpenAI | 模型目录（上游优先，失败回落内置表）；图像模型额外带 `endpoint` / `supports_chat=false` |
| POST | `/v1/embeddings` | OpenAI | 向量 |
| POST | `/v1/images/generations` | OpenAI | 生图 |
| POST | `/v1/search` | — | 内置联网检索通道 |
| GET | `/v1/points` | — | 全池额度 |
| GET | `/status` | — | 账号池状态（需鉴权） |
| GET | `/healthz` | — | 健康检查（有可用账号 200，否则 503；带 `service` 身份标识） |
| GET | `/metrics` | Prometheus | 指标文本 |
| GET | `/panel/` | — | Web 管理面板（账号池 / 用量 / 积分构成 / 模型档位 / 配置 / 日志） |
| POST | `/panel/api/accounts/{name}/checkin` | — | 单号签到（幂等，`first-login`） |
| POST | `/panel/api/checkin_all` | — | 全量签到（跳过禁用/登录态失效） |
| POST | `/panel/api/onboarding_all` · `/panel/api/accounts/{name}/onboarding` | — | 领新手任务（8 项合计 10000 积分，幂等） |
| GET | `/panel/api/dead_models` · `/dead_models/{model}/clear` | — | 失效模型台账与手动解除 |
| POST | `/panel/api/accounts/{name}/remark` | — | 账号备注（落盘 `auths/<账号>.json`，空串 = 清除） |
| POST | `/panel/api/accounts/{name}/pause` · `/resume` | — | 暂停选号（照常签到/刷额度）与恢复 |
| GET | `/panel/api/packages` | — | 积分构成（每日赠送 / 长期 / 来源包 + 邀请码） |
| GET | `/panel/api/credit_history` | — | 积分流水（跨账号按时间） |
| POST | `/panel/api/sms/send` · `/panel/api/sms/login` | — | 短信验证码登录 |

鉴权同时接受 `Authorization: Bearer <key>` 与 `x-api-key: <key>`。

## 配置

配置是单个 `config.json`。首次启动自动生成，包含随机 `api_key`。
所有键也可用环境变量覆盖（`LOOMY_` 前缀，如 `LOOMY_LISTEN`、`LOOMY_PANEL_PASSWORD`）。

见 **[config.example.json](config.example.json)** 的完整注释版。

### 常用项

| 键 | 默认 | 说明 |
|---|---|---|
| `listen` | `:17890` | 监听地址（改动需重启） |
| `api_keys` | `[]` | 网关密钥，空 = 不鉴权（**勿暴露公网**） |
| `auth_dir` | `./auths` | 账号凭证目录（改动需重启） |
| `state_file` | `./data/state.json` | 池状态文件（改动需重启） |
| `upstream` | `https://loomyad.xunfei.cn/api/v1` | 上游模型网关（改动需重启） |
| `default_model` | `imodel/deepseek-v4-flash-0731` | 未指定模型时用 |
| `strategy` | `weighted` | `weighted` / `round_robin` / `balance` / `lru` |
| `pick_top_n` | `5` | 加权选号短名单大小 |
| `max_inflight_per_account` | `0` | 单号在途上限（0 = 不限） |
| `balance_floor` | `0` | 积分保底：低于此值停止接单（0 = 关闭） |
| `sticky_ttl_seconds` | `1800` | 会话粘性 TTL（0 = 关闭） |
| `soft_rate_base_seconds` | `600` | 429 软冷却基数 |
| `soft_rate_max_seconds` | `7200` | 软冷却封顶 |
| `breaker_threshold` | `5` | 连败多少次熔断 |
| `keepalive_min_tokens` | `1500` | 非流式长任务阈值（超此值内部转流式） |
| `max_retries` | `2` | 换号重试次数 |
| `proxy` | `""` | 全局出口代理，账号可单独覆盖 |

面板里改配置会区分**热生效**与**需重启**：`listen` / `auth_dir` / `state_file` /
`upstream` / `account_base` / `log_dir` 改动后需重启，其余立即生效。

## 架构

```
cmd/server/           入口：配置 → 账号池 → 上游客户端 → 网关 + 面板 → HTTP
internal/
  account/            账号服务客户端：HMAC-SHA1 签名、RSA 加密、登录
  upstream/           模型网关客户端（含必需头与 camelCase 坑）
  pool/               账号池：选号 / 冷却 / 熔断 / 粘性 / 在途 / 持久化
  bridge/             Anthropic ↔ OpenAI、Responses ↔ OpenAI 翻译
  server/             HTTP 网关：入站解析、换号重试、SSE 转发/聚合
  panel/              内嵌管理面板（go:embed）
  usage/              请求台账（环形缓冲 + JSONL）
  httpauth/           常量时间 Bearer 校验
  config/             配置加载 / 保存 / 脱敏
```

### 选号是怎么算的

```
weight = 余额占比 ×10  +  闲置补偿 ×1  +  成功率 ×3
```

取权重 Top-5 组成短名单，再在名单内做加权随机抽签。
只取最大值会让单号被打爆，纯随机又会让低权重号白拿流量，Top-N + 抽签兼顾两者。

**会话粘性用「最后一条 user 消息」派生会话键**——这点很关键：常见客户端每轮重发
完整历史，若用首条 user 派生，键永远不变，粘性会永久锁死同一账号，多账号池直接
退化成单账号（参考实现里实测请求分布 779:13）。取最后一条后，多轮对话每轮键都变，
粘性只对「同一条消息的重试」生效，这正是它该干的事。

## 开发

```bash
go build ./...        # 构建
go vet ./...          # 静态检查
go test ./...         # 207 个单测
gofmt -l .            # 格式检查（应无输出）
```

测试覆盖：签名与 RSA 往返、选号分布与排他、冷却退避与封顶、熔断、模型级冷却、
积分保底、粘性 TTL 与容量、错误分类、SSE 解析与聚合、Anthropic/Responses 双向翻译
（含工具调用与思考链）、图像模型在对话端点被本地 400 拦住且不打上游、未知模型在目录层面
404 且不打上游、账号备注落盘与额度刷新后不丢、404 记模型台账不罚账号（含 TTL 与持久化）、
每日赠送额度优先分档、额度冷却终点为自然日 00:00、新手任务幂等与积分表合计、
配置读写与脱敏、鉴权、面板渲染与安全头、台账落盘与重启恢复。

## 安全与合规

- **凭证存储**：`auths/*.json` 以 0600 权限保存手机号与密码（**明文**）。这是
  「登录态 14 天到期后无人值守自动重登」的必要代价——上游没有 refresh token。
  请确保该目录不在备份 / 同步盘的共享范围内。
- **鉴权**：`api_keys` 为空时 `/v1/*` 与 `/panel/api/*` **完全不鉴权**，仅适合
  `127.0.0.1` 本机或可信内网。放到公网前必须设置 `api_keys`（面板登录与 `/v1/*` 用同一把）。
- **面板**：内嵌页带严格 CSP（`default-src 'none'`、无内联脚本）、`X-Frame-Options: DENY`、
  `Referrer-Policy: no-referrer`；配置视图与账号视图都不回显密钥或完整 session。
- **合规边界**：本项目是个人自用的协议互操作实现，**不得用于贩卖、共享他人账号、
  批量薅取免费额度或任何违反上游条款的用途**。作者不承担使用后果。

## 免责声明

本项目仅供学习和研究使用。使用者需遵守上游服务条款，自行承担使用风险
（包括账号封禁、条款违约等）。作者不对任何因使用本项目产生的直接或间接损失负责。

## License

[MIT](LICENSE)
