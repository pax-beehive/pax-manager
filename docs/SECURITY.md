# Security Plan

pax-manager (Fleet Cloud API) 安全防护设计。部署在 GCP Cloud Run，面向公网。

## 1. 传输层：TLS

**来源：GCP Cloud Run 自带。**

Cloud Run 默认 TLS termination，不需要自己配证书。所有流量强制 HTTPS，HTTP→HTTPS 自动 301。

- DNS → Cloud Run mapping 在 Cloud Console 里绑
- Let's Encrypt 证书自动颁发和续期
- 不做额外配置

## 2. 认证

**来源：Cloudflare Access。**

Gateway 层统一检查。两种身份来源共用一套中间件。

### 2.1 Dashboard（User → Gateway）

```
Browser → Cloudflare Access → Cloud Run
           │
           ├── OIDC (Google 登录)
           ├── 验证通过后注入 Header: Cf-Access-Jwt-Assertion
           └── Gateway 解析 JWT → 提取 email → 映射到 user 身份
```

- 用户身份：`Cf-Access-Jwt-Assertion` JWT 中的 email
- 只允许配置的邮箱列表访问（kevin + todd）
- 不在 Gateway 做 session/cookie 管理

### 2.2 Agent（paxd → Gateway）

```
paxd → Cloudflare Access → Cloud Run
        │
        ├── Service Token (CF-Access-Client-Id + CF-Access-Client-Secret)
        ├── Cloudflare 验证后放行
        └── Gateway 解析 Cf-Access-Client-Id → 查到 agent 身份
```

- 每个 agent 在 Cloudflare Dashboard 创建 Service Token
- 生成一对 client_id + client_secret
- paxd 配置 `cloud.cf_client_id` + `cloud.cf_client_secret`
- paxd 在每个 HTTP 请求头携带这两个值
- Gateway 维护映射：`client_id → agent_id → owner`

## 3. 授权 / 数据隔离

**不做多租户 DDL。用 owner 字段 + admin override。**

| 表 | 字段 |
|----|------|
| `agents` | `owner TEXT NOT NULL` |
| `sessions` | `owner TEXT NOT NULL`（通过 agent 继承） |
| `messages` | `owner TEXT NOT NULL`（通过 session 继承） |

### Gateway 中间件

```go
func AuthMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 1. 检查 Cf-Access-Jwt-Assertion（User）
        if jwt := r.Header.Get("Cf-Access-Jwt-Assertion"); jwt != "" {
            email := validateAndExtractEmail(jwt)
            r = r.WithContext(context.WithValue(r.Context(), "user", email))
            r = r.WithContext(context.WithValue(r.Context(), "role", "user"))
            r = r.WithContext(context.WithValue(r.Context(), "is_admin", isAdmin(email)))
            next.ServeHTTP(w, r)
            return
        }

        // 2. 检查 CF-Access-Client-Id（Agent Service Token）
        if clientID := r.Header.Get("Cf-Access-Client-Id"); clientID != "" {
            agent := lookupAgentByClientID(clientID)
            r = r.WithContext(context.WithValue(r.Context(), "agent_id", agent.ID))
            r = r.WithContext(context.WithValue(r.Context(), "owner", agent.Owner))
            r = r.WithContext(context.WithValue(r.Context(), "role", "agent"))
            next.ServeHTTP(w, r)
            return
        }

        // 3. 未认证
        http.Error(w, "unauthorized", 401)
    })
}
```

### 查询模式

```sql
-- User 请求：只查自己的数据
SELECT * FROM agents WHERE owner = $current_user;

-- Admin 请求：查所有人的数据
SELECT * FROM agents;  -- 不设 WHERE，admin 豁免
```

### Admin 列表

硬编码在 Gateway 环境变量中：

```yaml
ADMIN_EMAILS: "kevin@example.com"
```

## 4. 不做的事

| 项 | 原因 |
|----|------|
| **端到端加密** | 不需要。Cloud 是自己控制的 GCP 实例。加密消息在 Cloud 也能解 |
| **自建 API Key 系统** | Cloudflare Service Tokens 已覆盖，不用自己做生成/轮换/吊销 |
| **多租户 DDL** | Tenant 隔离等有第三个用户再说。当前只需要 owner 字段 |
| **Rate limiting** | MVP 阶段不做。流量低，Cloud Run 自带一定保护 |
| **Audit log** | 不做。所有 tool/message 已在 DB 中，天然可审计 |

## 5. Cloudflare 配置清单

- [ ] Dashboard 应用：OIDC (Google)，allowed emails: kevin + todd
- [ ] Service Token 1：kevin 的 agent
- [ ] Service Token 2：todd 的 agent
- [ ] 两个应用都指向同一个 Cloud Run 后端
- [ ] TLS：Cloud Run 默认已启用
