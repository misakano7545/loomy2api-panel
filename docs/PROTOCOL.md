# 逆向协议参考（Loomy / 讯飞）

本文档描述本项目所依赖的上游协议形态，供互操作与学习使用。
内容来自对官方 Loomy 桌面客户端（Electron `0.9.38`）行为的观察与公开参考实现，
**与讯飞无任何关联**。

> 本仓库只描述**协议结构**（端点、鉴权形状、请求/响应字段）。
> 客户端内置的签名常量在 `internal/config` 里作为可覆盖的默认值存在，
> 可在 `config.json` 的 `access_key_id` / `access_key_secret` 中替换为你自己客户端的值。

---

## 1. 组件与端点

| 组件 | Base URL | 用途 |
|---|---|---|
| 模型网关 | `https://loomyad.xunfei.cn/api/v1` | OpenAI 兼容模型访问（`/chat/completions`、`/models`、`/embeddings`、`/images/generations`、`/search/tencent`、`/points/records`） |
| 账号服务 | `https://account.xfinfr.com` | 登录、签发 session、用户信息 |
| 交易服务 | `https://trade.xfinfr.com` | 支付（本项目不用） |
| Nexus 网关 | `wss://dispatch-nexus.xfinfr.com/ws/loomy` | 远控长连（本项目不用） |
| 官网 / 微信回调 | `https://loomy.xunfei.cn` | 微信扫码回调域 |

## 2. 鉴权模型

模型网关用 **登录态本身** 作为 Bearer token，不发放独立 API key：

```
Authorization: Bearer <session>
token: <session>                      # 双写，上游两种拼写都接受
traceparent: 00-<32 hex>-<16 hex>-01  # 必需；缺失会让上游挂死到超时
loomy-version: 0.9.38                 # 仅做存在性检查
Content-Type: application/json
```

可选的归属头（影响服务端积分记账）：`ChatId`、`MsgId`、`TurnId`、
`X-Loomy-Request-Purpose`（取值如 `chat.message`、`chat.title`、`kb.embed`、
`image.generate`、`search.web` 等）。

- **双写 `Authorization` 与 `token`**：客户端 provider 配置里是 `useSessionAuth: true`，
  网关两种拼写都认，所以本项目两个都发。
- **`traceparent` 不能省**：实测缺失时请求会挂到超时而不是快速报错，
  这是最容易踩的坑。本项目每个请求都重新生成。

### 登录态生命周期

- 有效期 **14 天**（客户端登录时显式申请 `expire=1209600`）；
- **没有 refresh token**，过期只能重新登录；
- 因为密码登录可全自动，所以「存手机号 + 密码」是无人值守的前提。

## 3. 账号服务签名

请求头 `Authorization` 的形态是 `account <accessKeyId>:<signature>`，
签名算法为 **HMAC-SHA1**。

`stringToSign` 固定 **9 行**（无 `x-*` 头时最后两行为空串，但**不能省略**）：

```
METHOD                                   # 大写，如 POST
ESCAPED_PATH                             # 逐段 RFC3986，额外转义 !'()*
ESCAPED_QUERY                            # 无 query 时为空串
Content-MD5                              # base64(md5(body))，无 body 时为空串
Content-Type                             # application/json
Date                                     # 如 Sat, 26 Sep 2026 12:00:00 GMT
Nonce                                    # 一个 UUID
SignedHeaders                            # 排序后的 x-* 小写名，用 ; 连接
CanonicalizedHeaders                     # 排序的 "k:v\n" 拼接，去掉末尾换行
```

```
signature     = base64(HMAC-SHA1(accessKeySecret, stringToSign))
Authorization = "account <accessKeyId>:<signature>"
```

实现见 `internal/account/signer.go`，单测 `signer_test.go` 覆盖了行数、转义、
自定义头规范化与签名稳定性。

## 4. 登录流程

### 4.1 密码登录（全自动）

```
POST /login/account/getPuKey   → { pukey: <base64 DER 公钥>, rcode: <服务端 nonce> }
POST /login/account/byPwd      → { session, userid, phone }
   body: { loginid, password: RSA_PKCS1v15(pukey, 明文密码), rcode, type: 1, expire: 1209600 }
```

关键点：**`rcode` 是服务端下发的 nonce，不是验证码**，所以密码登录完全可自动化，
不需要人工看图或输码。

公钥是 DER 编码（PKIX `SubjectPublicKeyInfo`）；本项目同时兼容裸 PKCS#1，
并容忍 base64 缺失 padding（Node 的 `Buffer.from` 容忍，Go 的标准库不容忍）。

### 4.2 短信登录（需人工读一次码）

```
POST /login/phone/sendMsgCode  → { msgid }
POST /login/phone/checkCode    → { session }
```

### 4.3 其他

```
POST /userinfo/query/baseInfo   # 校验登录态是否仍有效
POST /login/account/logout
```

### 4.4 请求信封

账号服务的每个请求体都是同一个信封：

```json
{
  "base": {
    "appid": "GM3LOOMY",
    "modelid": "Web",
    "version": "1.0.0",
    "devid": "web",
    "ua": "Loomy|Desktop|Electron|macOS",
    "traceid": "<每请求新生成的 32 hex>"
  },
  "param": { "...": "各端点自己的参数" }
}
```

**`devid` 必须是固定常量 `web`**。所有官方客户端（含 Windows 版）都发这个值，
自创格式（如 `web-<hex>`）在全部真实流量里都是异常信号。设备维度区分由
promotions 侧的 `campus_device_id` 承担（客户端每台机器一个），本项目按账号保持独立。

## 5. 模型网关

### 5.1 请求

```http
POST {base}/chat/completions
body: { model, messages, temperature, max_tokens, stream,
        enable_thinking, chat_template_kwargs: { enable_thinking } }
```

- 模型标识形态为 `provider/model`，如 `imodel/spark-x`。
- 推理模型会在 `message.reasoning_content` 里给思考链；
  可用 `enable_thinking: false`、`chat_template_kwargs.enable_thinking`
  或消息前缀 `/no_think` 抑制。

### 5.2 响应

- 非流式：`choices[].message.reasoning_content` + `content`；
  `usage` 含 `completion_tokens_details.reasoning_tokens`（思考 token）、
  `points_consumed`（本次扣的积分）、`prompt_tokens_details.cached_tokens`。
- 流式：标准 OpenAI SSE（`data: {...}`），末尾 `data: [DONE]`。

### 5.3 额度与积分

```
availableBalance = balance（永久）+ dailyBalance（每日免费额度，先扣后补，每日重置）
```

模型有倍率（`x0.1` … `x12.0`），决定一次请求扣多少积分；`spark-x` 在 `x0.1` 最便宜。

```
GET  {base}/points/records?record_type=all&pageNo=1&pageSize=100
POST {base}/points/first-login
```

**坑：上游只认 camelCase 的 `pageSize`**。传 `page_size` 会被静默忽略并回落默认 20 条，
不会报错——排查起来极其费时。本项目在 `internal/upstream` 用常量固定了拼写，
并有单测 `TestQuotaCamelCasePageSize` 守着。

### 5.4 长任务与非流式的 504

上游对「长时间无响应的**非流式**请求」约 60 秒就返回 504；而**同一模型的流式调用**
能跑几分钟。所以本项目对非流式长任务（`max_tokens >= keepalive_min_tokens`）
内部改用 `stream: true` 调上游，边收 SSE 边聚合，最后拼回非流式 JSON——
客户端完全无感，长任务不再被掐断。

## 6. 风险控制相关的观察

- 客户端身份字段（`devid` / `ua` / `modelid` / `version`）是**常量**，
  改这些不会降低风控判定，反而更显眼。
- 真正决定风险画像的是网络出口；本项目提供全局 `proxy` 与账号级 `proxy`。
- 请求归属头（`ChatId` / `MsgId` / `TurnId`）影响积分记账口径，建议透传。

## 7. 已知局限

- 没有 refresh token → 登录态到期只能重登，因此需要保存手机号 + 密码。
- `/v1/messages` 是本地翻译层（上游只有 OpenAI 形态），工具调用与思考链已覆盖，
  但 Anthropic 的一些细节（如 `cache_control` 断点）无法传递到上游。
- 模型目录以 `/models` 为准；上游不可用时回落到内置表（`internal/config`）。

---

本项目仅供学习研究，使用者需自行遵守上游服务条款。
