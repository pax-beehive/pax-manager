# Inquiry Access Model Draft 中文版

## 摘要

这份文档记录 inquiry、representative agent 和共享知识的权限模型草稿。模型刻意保持小，但借鉴了 Zanzibar/OpenFGA 的 relationship grant 思路，以及 IAM/Cedar 的请求评估方式。

核心想法：

- 用 representative agent 表达一个有固定范围的对外回答身份。
- 用 grant 表达哪个 subject 可以对哪个 resource 做哪个 action。
- 用 access grant 作为唯一共享原语。第一版可以依赖 subject membership；如果以后需要，再加入 explicit boundary。
- 当 inquiry 需要当前 representative scope 之外的数据时，必须显式 escalation。

这是设计草稿，不是实现记录。

## 目标

- 支持 `knowledge_capsule`、`inquiry`，以及未来的 `task` envelope payload。
- 让 inquiry answer 可以使用或引用 evidence，同时不泄露 private knowledge。
- 让用户的 agent 可以在受限 scope 内代表用户回答。
- 避免把所有知识都塞进一个粗糙的 visibility 字段。
- 避免 team 或 conversation 成员变化后留下过期访问权。
- 给未来 task 和 Linear integration 留出可复用的权限基础。

## 非目标

- 第一版不做完整 IAM policy language。
- 第一版不要求接外部 authorization engine。
- representative agent 不能自动跨 scope 读取数据。
- 不保证 representative agent 必须有独立 runtime。
- 第一版不做群聊产品面，但模型要预留 conversation-scoped grants。

## 术语

- Principal：发起访问请求的主体。可以是 user，也可以是 agent session。
- Agent session：一次可审计的 agent runtime 运行。
- Representative agent：固定 scope 的受限身份，可以代表某个用户回答 inquiry。
- Resource：被保护的对象，比如 knowledge、envelope、task、document、message。
- Action：请求对 resource 执行的操作。
- Grant：允许某个 subject 对某个 resource 做某个 action 的关系。
- Boundary：未来可选的约束，用于把 explicit grant 限定在 team、workspace root、conversation、personal scope、public scope 内。
- Escalation：从一个 scope 显式转交到更窄或权限更高的 scope 的 child inquiry 或 delegation。

## Principal 模型

访问请求应该只有两类 principal：

```text
user
agent_session
```

agent session 在授权和审计里本身就是 principal，不需要再内嵌另一个 principal 字段。它需要携带足够信息来定位 actor 和 runtime：

```text
agent_sessions
- session_id
- agent_id
- actor_type              personal_agent | representative_agent | worker
- actor_id                actor_type 是 representative_agent 时为 representative_agent_id
- created_by_user_id
- created_at
```

对于 user principal，授权依赖用户成员关系和 direct grants。对于 agent-session principal，授权依赖 session actor、agent owner，以及适用时的 representative scope。

## Representative Agent

representative agent 是一个权限身份，不一定是物理 runtime。它可以使用 backing agent/runtime，但访问评估必须通过 representative identity 和它的 scope。

```text
representative_agents
- representative_agent_id
- backing_agent_id
- owner_type              user | team
- owner_id                这个 agent 代表的 user_id 或 team_id
- provided_by_type        user | team
- provided_by_id          维护这个 agent 的 user_id 或 team_id
- scope_type              public | team | user_pair | conversation
- scope_id                team_id、pair_id、conversation_id，public 时为空
- approval_policy_id
- status                  active | paused
- created_at
```

Representative scopes：

- `public`：只能基于 public resources 回答。
- `team`：可以在某个 team、workspace、company、project 或其他 team-like context 里回答。
- `user_pair`：可以在一对一关系里回答，比如好友 pair，或者 employee-to-HR channel。
- `conversation`：预留给群聊和其他多方 conversation context。

representative agent 不能静默继承 backing agent 的 private context。它只能使用自己 scope 和 grants 允许的 resources。

ownership 和 provisioning 要分开：

- `owner_type` 和 `owner_id` 表示 representative 是代表谁说话，比如 Alice、HR team，或者 company。
- `provided_by_type` 和 `provided_by_id` 表示谁运营这个 representative，谁维护 escalation policy，谁负责它的 intake process。
- `approval_policy_id` 指向当其他 representative 想 escalation 到这个 representative 时使用的审批策略。

比如 HR representative 可以是：

```text
owner_type = team
owner_id = hr_team
provided_by_type = team
provided_by_id = hr_team
approval_policy_id = hr_escalation_policy
```

Alice 面向 company 内部的 representative 可以是：

```text
owner_type = user
owner_id = alice
provided_by_type = user
provided_by_id = alice
scope_type = team
scope_id = acme_root_team
```

## Resources

被保护的 resource 应该统一引用：

```text
resource_ref
- type                    knowledge | envelope | task | doc | message |
                          escalation_policy | escalation_rule | agent_profile
- id
```

第一批需要这个模型的 resource 是 inquiry envelopes，以及作为 evidence 使用的 knowledge objects。

## Actions

第一版 action set：

```text
read
cite
answer_with
reply
escalate
view
edit
manage
```

含义：

- `read`：principal 可以读取 resource 内容。
- `cite`：principal 可以把 resource 作为 evidence 暴露给 inquiry recipient，但仍要经过 boundary 和 scope 检查。
- `answer_with`：principal 可以基于 resource 生成回答，但不一定能引用或暴露原文。
- `reply`：principal 可以回复 envelope 或 inquiry。
- `escalate`：principal 可以创建 child inquiry 或 escalation request，转交给更窄的 representative scope。
- `view`：principal 可以看到并复用 escalation policies、escalation rules、agent profiles 等配置资源。
- `edit`：principal 可以修改配置资源的内容。
- `manage`：principal 可以修改或管理 resource 及其 grants。

Evidence 需要分别检查 `cite` 和 `answer_with`。一个 resource 可以允许用于推理，但不允许作为 evidence 分享出去。

## Grant 模型

Grant 表达谁可以对某个 resource 做什么。Access grant 是业务资源和配置资源共享的唯一基础原语。第一版不要为 policy 或 rule 的可见性单独引入 `ScopeControl` 表。

第一版 `access_grants` 可以不包含 boundary。Team、conversation、public grants 可以依赖 subject 自身的当前 membership 语义。以后如果 explicit user grant 需要被限定在 company、team root 或 conversation 内，再加入 boundary fields。

```text
access_grants
- grant_id
- resource_type
- resource_id
- subject_type            user | team | team_root | representative_agent | conversation | public
- subject_id              user_id、team_id、representative_agent_id、conversation_id，public 时为空
- action                  read | cite | answer_with | reply | escalate | view | edit | manage
- created_by_user_id
- created_at
- expires_at
- revoked_at
```

重要规则：

```text
grant 不是永久通行证。它只在 subject 仍然匹配 principal 时有效。比如 team grant 跟随当前 team membership，conversation grant 跟随 conversation membership 和 history policy。
```

例子：

```text
knowledge:k1 answer_with user:alice
knowledge:k2 cite team:infra
knowledge:k3 answer_with conversation:conv_123
knowledge:k4 read public:*
representative_agent:hr_team escalate team:acme_employees
escalation_policy:hr_policy view team_root:acme
escalation_policy:hr_policy edit team:hr
escalation_policy:hr_policy manage team:hr
```

上面的例子里，company/root team 可以 view 和复用 HR policy，而 HR team 可以 edit 和 manage 它。这通过多条 grants 表达，不需要单独的 visibility preset。

## Future Boundaries

Boundary 是未来用于约束 explicit grants 的扩展。第一版 access-grant 模型不要求 boundary。如果以后加入 boundary，它用来判断 grant 对 principal 是否仍然有效。

```text
public
- 永远有效。

personal:user_id
- 对 owner 有效；对尚未 revoke 的 explicit private shares 有效。

team_root:root_team_id
- 对 root team、workspace、company 或 independent team tree 的当前成员有效。

team:team_id
- 对该 team 的当前成员有效。

conversation:conversation_id
- 对 conversation 的当前成员有效，并受 conversation history policy 约束。
```

即使第一版 grant 上没有 boundary fields，也可以把 company、workspace、side project、independent group 当作 team root。company 是带 company 语义的 root team，不是单独的 authorization primitive。

## Personal、Work 和 Life Contexts

这个模型不把 `work` 和 `life` 作为一等 authorization type。它们应该映射到 personal、user-pair、team-root 和 team boundaries：

```text
life / personal context
- personal:user_id boundary
- 一对一 private relationship 使用 user-pair scope
- personal agents 可以使用这个 context
- representative agents 不能使用它，除非某个 sanitized resource 被显式 grant 给它的 scope

work context
- team_root 或 team boundary
- company、workspace、independent project、side project 都可以是 root team
- team representatives 只能通过 grants 和当前 membership 使用 work resources
```

跨 context 访问应该是显式的。personal/life agent 不应该直接把 private context 暴露给 work representative；work representative 也不应该直接 query personal context。应该使用 sanitized handoff、inquiry 或 escalation。

例子：

```text
用户的 life agent 知道用户生病了。
work-facing representative 只收到：
"The user is unwell and requests sick leave today."
```

这个 bridge 传递的是必要的最小披露，而不是原始 private context。

## Team Relationship Model

Teams 应该形成 forest，而不是一个全局唯一树。一个 root team 可以表示 company、workspace、independent project、community、family group，或者其他协作边界。

推荐 team fields：

```text
teams
- team_id
- parent_team_id
- root_team_id
- team_type              company | team | project | community | family | group
- name
- status
```

`root_team_id` 可以直接存，也可以通过 `parent_team_id` 推导。直接存可以让 boundary check 更便宜；推导可以避免 denormalization。

Team relationships 可以表达层级和协作：

```text
team_relations
- from_team_id
- to_team_id
- relation_type          parent | partner | shared_project | alias
- status                 active | archived
```

Access 不应该只从 team relationships 自动推断：

- parent-child membership 不自动授予所有 child team resource 访问权。
- overlapping membership 不会自动合并 knowledge scopes。
- partner 或 shared-project relationship 应该要求 explicit grants。
- 如果要支持继承，继承应是 grant 或 team relation 上的 explicit policy。

安全默认规则是：

```text
team relationships 用来帮助 routing 和 suggested grants；
真正 access 仍然需要 grant 加有效 subject membership。
```

## Team Membership 语义

Team grant 是 grant 给 team，而不是 grant 创建时成员列表的快照。

```text
team grant = 当前 team 成员可访问
```

如果 user A 曾经在 team 里，后来离开：

- A 默认不再拥有 team-grant 的历史 team-only knowledge 访问权。
- A 也不能访问离开后新创建的 team knowledge。
- A 只有在存在其他有效 grant 时才能继续访问，比如 public grant，或尚未过期/撤销的 explicit user grant。如果未来加入 boundary fields，也可以让 explicit user grant 在用户离开 company、team root 或 conversation 时失效。

贡献者身份和访问权分开：

```text
created_by_user_id = A
```

不代表 A 永久可读。

## Conversation 语义

一对一 conversation 可以用 explicit user grants 或 `user_pair` scope 表示。群聊不要长期建模成固定的一组 user grants。群聊应该使用 conversation audience：

```text
grant resource -> conversation:conv_123
```

推荐 conversation fields：

```text
conversations
- conversation_id
- conversation_type       direct | group | escalation_thread
- boundary_type           personal | team_root | team | public
- boundary_id
- history_policy          full_history | from_join | none | admin_approved
- status                  active | archived
```

Conversation membership 需要记录加入和离开时间：

```text
conversation_members
- conversation_id
- user_id
- role                    owner | admin | member
- joined_at
- left_at
```

Conversation history policy 决定新成员是否可以看到过去消息和派生知识：

```text
full_history
from_join
none
admin_approved
```

Conversation access 应由以下条件共同决定：

- resource 是否 grant 给 `conversation:conversation_id`。
- 当前或历史 conversation membership。
- conversation boundary，比如 team root 或 team。
- conversation history policy。

例如，`from_join` conversation 可以允许新成员读取加入后的新消息，但拒绝旧消息和只从旧消息派生的 knowledge。`full_history` conversation 可以在成员加入后授予历史访问。`admin_approved` conversation 应该在暴露历史内容前要求显式 approval。

## Inquiry 和 Escalation

representative agent 不能直接 query 自己 scope 之外的数据。如果回答 inquiry 需要更窄或更高权限的 scope，representative 应该创建 child inquiry 或 escalation。

HR 流程例子：

```text
1. Employee 向某个 HR 人员的 company representative 询问敏感 HR 数据。
2. company representative 判断需要 HR-scoped data。
3. 它创建 child inquiry 给 HR team representative。
4. HR team representative 检查 HR-scoped data 和 policy。
5. HR team representative 可能需要 human approval。
6. 它把 sanitized answer 返回给 company representative。
7. company representative 回复原始 employee。
```

规则：

- 每个跨 boundary 的数据请求都应该显式 escalation。
- Escalation 不一定都需要 human approval。
- 敏感数据、policy commitment、legal、HR、security、financial 或其他高风险回答需要 human approval。
- 返回的 answers 和 evidence 应该获得适合最终 audience 的 grants，常见是 company boundary 内的 user pair。

## Escalation Authorization

Escalation 本身也是一个 action。一个 representative agent 如果无法在当前 scope 内回答，并不代表它自动有权限升级到更窄或更高权限的 scope。

目标 representative 或 team 应该通过 grant 暴露 inbound escalation policy：

```text
resource: representative_agent:hr_team
action: escalate
subject: team:acme_employees
```

这表示 Acme employees team 的当前成员可以向 HR team representative 请求 escalation。但这不表示 HR team representative 必须自动回答。

Escalation check 应该验证：

- requester 可以在当前 inquiry boundary 内发问。
- source representative 可以在该 boundary 内创建 child inquiry。
- target representative 接受来自 requester 或 requester 所属 team 的 `escalate`。
- requested purpose 被 target 允许。
- requester 仍然满足 membership 要求，比如仍是当前 company 或 team member。

HR、security、legal、finance、customer support 这类敏感目标通常可以接受较宽的 internal audience 发起 escalation，但在返回数据前应保留 human approval 的权利。

Inbound escalation rules 应该由目标 representative 的 provider 维护。对于 user-provided representative，由用户维护规则。对于 team-provided representative，由 team owners 或 team routine 维护规则。

推荐 policy 结构：

```text
escalation_approval_policies
- policy_id
- maintained_by_type       user | team
- maintained_by_id
- status                   active | paused
- created_at
- updated_at

escalation_rules
- rule_id
- policy_id
- subject_type             user | team | public
- subject_id
- source_scope_type        public | team | user_pair | conversation | any
- source_scope_id
- purpose
- requested_action         answer | verify | route | approve
- decision                 auto_accept | needs_approval | deny | needs_clarification
- approver_type            user | team_routine | representative_agent
- approver_id
- priority
- status                   active | paused
```

Team routine 是 team 拥有的命名 intake path，不是某个具体用户：

```text
team_routines
- routine_id
- team_id
- name                     HR intake | Security review | Legal approval
- status                   active | paused
```

Rule decision 含义：

- `auto_accept`：target representative 可以不经过 human approval 处理 escalation，但仍然要应用 resource grants 和 boundaries。
- `needs_approval`：escalation 必须路由给配置好的 approver。
- `deny`：target representative 拒绝 escalation。
- `needs_clarification`：target representative 在决定前要求 source requester 补充信息。

Policy 应该按 requester、source scope、purpose、requested action、可选 subject user 和 sensitivity 匹配。宽泛的 `escalate` grant 只表示 source 可以敲 target 的门。approval policy 决定门是否打开、谁来 review、以及最终能返回什么。

有用的 escalation request fields：

```text
escalation_request
- parent_envelope_id
- target_representative_agent_id
- requester_user_id
- subject_user_id
- purpose
- requested_action           answer | verify | route | approve
- sensitivity                low | medium | high
- original_scope_type
- original_scope_id
- approval_policy_id
- approval_rule_id
- approver_type
- approver_id
- justification
```

目标 representative 应返回以下之一：

```text
accepted
declined
needs_human_approval
needs_clarification
```

返回的 answers 应该被 sanitized，并且只 grant 给允许看到它们的最终 audience。

## Inquiry Evidence

Inquiry response 在有用时应该携带 evidence，但 evidence 必须做权限检查。

推荐 response fields：

```text
inquiry_response
- outcome                 answered | declined | needs_clarification | partial
- answer
- selected_ids
- clarifying_questions
- evidence
- confidence              low | medium | high
- answer_mode             auto | draft_approved | human | representative_auto
```

Evidence 应区分：

- shareable evidence：answer 可以引用或暴露它。
- private review evidence：可以帮助用户的 personal agent 起草 answer，但不能发给 requester。
- sensitive evidence：需要 approval，或不能用于 automated representative answer。

## Access Check

授权检查概念上应该是：

```text
Can(principal, action, resource)
```

伪代码：

```text
func Can(principal, action, resource):
    grants = FindGrants(resource, action)

    for grant in grants:
        if grant.revoked_at is set or grant is expired:
            continue
        if !subjectMatches(principal, grant.subject):
            continue
        # Optional future extension:
        # if grant has a boundary and !boundaryAllows(principal, grant.boundary):
        #     continue
        if !representativeScopeAllows(principal, grant):
            continue
        return true

    return false
```

`subjectMatches` 处理 direct users、team membership、conversation membership、public grants 和 representative-agent grants。

`boundaryAllows` 是 explicit grant boundary 的未来可选扩展。第一版可以依赖 `subjectMatches` 处理 team、team-root、conversation、public 和 direct-user grants。

`representativeScopeAllows` 防止 representative session 使用 scope 外的 grants，即使 backing runtime 或 owner 原本有更高权限。

## 产品说明

- "Representative agent" 是面向用户的概念。
- backing implementation 可以用 shared runtime，也可以用 dedicated runtime。
- personal agents 是 private/internal，默认不应允许外部 inquiry。
- representative agents 可以被外部 inquiry，但只能在配置的 scope 内。
- scope 变化应该创建新的 representative agent 或显式 escalation，而不是静默修改 active session。

## 参考模型

这份草稿受到以下模型启发：

- Google Zanzibar 和 OpenFGA relationship tuples。
- AWS IAM 和 Cedar 的 request evaluation concepts。
- RBAC 的 team roles，以及 ABAC 的 current membership 和 boundary checks。

第一版实现应比这些系统小得多。最重要、可复用的形状是：

```text
principal + action + resource + grants + representative scope
```

## 未决问题

- `team_root` 应该直接存到 team 上，还是通过 `parent_team_id` 向上遍历得到？
- `user_pair` 应该是一张一等表，还是用 deterministic pair id？
- inquiry response 的每个字段分别应该要求哪些 actions？
- representative agent 应该有显式 tool allowlist，还是 tool access 完全从 scope 派生？
- conversation-scoped grants 是否应该等 group chat 存在后再实现？
