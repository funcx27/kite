# 工作流模块（Workflow / CI-CD）设计方案

> 状态：设计定稿，UI 已有静态 demo（[workflow.html](./workflow.html)），后端/前端实现待启动。
> 目标：为平台补齐 CI/CD 编排：可视化定义 DAG 工作流（构建镜像 → 审批 → 部署应用 → 通知），串起 buildfleet（构建引擎）与 application（发布模块），打通 code → image → cluster 的自动化链路。
> 姊妹篇：[design.md](./design.md)（应用模块）。本模块大量沿用其既有模式（Deps 注入、revision 链、伪资源自授权、发布门禁不可绕过）。

## 1. 已确认的核心决策

| 决策点 | 结论 |
|---|---|
| 模块形态 | 仓库内独立 go module：`modules/workflow/`，零 kite import、零其他模块 import；依赖经 `Deps` 注入，kite 以 `replace` 引用 + `pkg/workflowadapter` 桥接 |
| 引擎形态 | **自研进程内 DAG 引擎**（无 controller、无消息队列、无外部 runner）：每个 run 一个编排 goroutine，拓扑序调度节点，状态逐节点落库，进程重启可恢复。单副本部署（与 buildfleet 嵌入一致，charts 注明 `replicaCount: 1`） |
| 节点类型（MVP 四类） | **构建镜像**（buildfleet）、**部署应用**（application）、**人工审批**（门禁）、**通知**（webhook）。通用任务节点（跑任意镜像）进 Backlog |
| 与发布门禁的关系 | **工作流不绕过应用模块的任何门禁**：deploy 节点经 application 的 deploy 语义执行，遇冲突（Live≠锚点）即节点失败并透出冲突明细——自动发布永远不覆盖集群手动修改，人工在应用模块合并后「从失败节点重试」 |
| 定义与执行分离 | Workflow 定义带版本链（WorkflowRevision）；Run 钉住触发时的 rev，历史 run 永远可回溯当时定义 |
| 凭证 | 节点以 `credentialRef` 引用**凭证中心**（独立前置模块，见 §9）；本模块不存凭证明文，只透传给执行器 |
| 触发器 | 手动（参数表单）/ git webhook（github、gitea、gitlab 载荷适配，每 workflow 独立 token）/ cron（模块内置轻量调度循环，不改 pkg/scheduler） |
| 前端形态 | 住在 kite `ui/` 工程内，但**目录结构完全独立**：全部代码自包含在 `ui/src/workflow/` 单一目录（pages / components / api / i18n 自带），不向任何共享目录添加文件；风格复用同一套 Tailwind 主题与 shadcn 原语（import 而非修改）；宿主共享文件仅两处**追加式**改动：`routes.tsx` 一个路由块、`package.json` 一个依赖行（见 §2/§11） |
| 可视化 | React Flow（`@xyflow/react`，MIT）自由画布：拖拽节点 + 连线 + 右侧属性抽屉；无显式 start/end 节点（无入边即起点）；DAG 无环校验 |

**为什么不集成 Argo Workflows / Tekton**：工作流的执行原语（构建、部署）已是平台内能力（buildfleet SDK 进程内可用、application deploy 是库调用），引入集群内 CRD 引擎反而要求每集群安装、多集群编排与审批绕远，与 kite 单二进制自包含理念冲突。自研引擎的复杂度收敛在一个拓扑遍历状态机（参考 DolphinScheduler/Kestra 的 run 语义，见 §14）。

**与手动构建页的关系**：buildfleet 接线页（专业模式即席构建）与本模块互补不冗余——前者对标 kubectl 式单次操作，后者是可编排、可重试、带审批的流水线；同一构建能力两种消费形态。

## 2. 总体架构与解耦约束

```
ui (React) ── /api/v1/workflows* ──> kite routes.go（挂载点，RBAC 中间件之前）
                                        │
                                 workflow.RegisterRoutes(api, deps)   ← 独立 module
                                        │ Deps 注入
                    ┌───────────────────┼────────────────────┐
              Authorize/RecordHistory  BuildImage          DeployApp/Notify
              (rbac.CanAccess/History) (→ buildfleet)      (→ application / webhook)
                                        │
                        由 pkg/workflowadapter 组装（唯一同时知晓宿主与各模块的包）
```

**解耦是硬性约束**（用户明确原则：前后端尽可能不与 kite 耦合，方便上游更新后重新合并）：

- 后端严格走 [modules/README.md](../../modules/README.md) 契约：宿主触点白名单仅 ①主 `go.mod`（require+replace）、②`routes.go` 装配（含 Migrate 显式触发与 webhook 公开挂载）、③`pkg/workflowadapter/`。`pkg/model`、`pkg/common/resource.go` 等宿主共享文件**零改动**。
- 模块不 import 宿主、不 import application/buildfleet 模块。构建与部署能力以 Deps 接口注入：`BuildImage(ctx, req)`、`DeployApp(ctx, req)`、`Notify(ctx, req)`、`ResolveCredential(ctx, ref)`——全部由 workflowadapter 实现（它 import buildfleet/application 模块并组装，进程内直调，不走 HTTP）。
- **前端目录结构完全独立**（用户明确原则：风格可相似，代码独立；同一 Vite 工程、同一构建链，但模块代码自成一个目录）：
  - 单一自包含目录 `ui/src/workflow/`：`pages/`、`components/`（含画布/抽屉/日志流等模块组件）、`api.ts`（自有 API 客户端）、`types.ts`、`i18n/{zh,en}.json` + 轻量翻译 hook（读宿主语言偏好，资源自带）。**不向** `ui/src/pages`、`ui/src/components`、`ui/src/lib` 等共享目录添加任何文件。
  - 风格与原语靠 **import 复用**而非复制：直接 import `ui/src/components/ui/` 的 shadcn 原语与既有 Tailwind 主题（import 不是编辑，不产生上游合并冲突面）；模块内部不依赖 kite 业务组件，复杂展示组件（日志流等）按需拷入模块目录自有演化。
  - i18n 资源模块自带，**不动共享 `en.json`/`zh.json`**——历次上游同步的最大冲突热点就此绕开。
  - 宿主共享文件仅两处追加式改动：`ui/src/routes.tsx`（单块：import 模块路由定义并展开，置于 `:resource` 通配之前）与 `ui/package.json`（`@xyflow/react` 依赖行）。
  - 入口：不修改 kite 侧边栏代码。用 kite 既有**自定义侧边栏**能力配 `/workflows` 链接（零代码触点），或直接访问 URL；同工程同源，鉴权/主题/i18n 语言态天然继承。
- 持久化：模块自带 GORM 模型，`routes.go` 装配处追加 `workflow.Migrate(DB)`（同 application 模式）。

**上游同步回归检查点**（每次 merge 上游后必查，在既有三条之上新增）：
① `pkg/workflowadapter` 编译（Deps 签名依赖 fleet.Request / application 门面 / rbac / model API）；② `rbac.CanAccess` 对伪资源 `"workflows"` 的行为；③ webhook 公开挂载点在 routes.go 的位置漂移；④ `ui/src/routes.tsx` 的模块路由块与 `ui/package.json` 依赖行是否幸存（上游若重构路由或依赖清单，这两处是唯一可能的冲突点）。

## 3. 数据持久化模型

```go
// 工作流定义（逻辑实体，全局唯一）
type Workflow struct {
    ID           uint
    Name         string        // uniqueIndex, DNS-1123, ≤63
    Namespace    string        // 归属空间（对齐应用模块的 Space 概念）
    Description  string
    CurrentRev   int           // 最新定义版本
    WebhookToken SecretString  // 随机生成，触发鉴权（展示/重置走 API）
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

// 定义版本链（每次保存定义记一条；Run 钉住 rev）
type WorkflowRevision struct {
    ID         uint
    WorkflowID uint      // index, OnDelete:CASCADE
    Rev        int       // per-Workflow 单调递增，不回收
    Spec       JSONField // DAGSpec（nodes/edges，见 §4）
    Params     JSONField // []ParamSpec（参数 schema）
    Triggers   JSONField // TriggerConfig（manual/webhook/cron，见 §7）
    OperatorID uint
    CreatedAt  time.Time
}

// 一次执行（钉住定义版本与入参快照）
type WorkflowRun struct {
    ID           uint
    WorkflowID   uint  // index
    RunNumber    int   // per-Workflow 展示序号（#1、#2…）
    RevisionRev  int   // 触发时钉住的定义版本
    Status       string // queued / running / succeeded / failed / cancelled
    TriggerType  string // manual / webhook / cron
    TriggeredBy  uint   // 触发用户（webhook/cron 记配置操作者；权限以其为准）
    TriggerRef   string // 来源（git SHA / 分支 / 手动备注）
    Params       JSONField // 本次入参快照（模板解析的根数据之一）
    StartedAt    *time.Time
    FinishedAt   *time.Time
}

// 节点执行记录（引擎状态机的落库形态）
type NodeRun struct {
    ID        uint
    RunID     uint      // index, OnDelete:CASCADE
    NodeID    string    // DAGSpec 中的节点 id
    Status    string    // pending / running / succeeded / failed / skipped / cancelled / interrupted
    Inputs    JSONField // 渲染后的节点有效参数（模板已解析；敏感字段脱敏存储）
    Outputs   JSONField // 如 {imageRef, digest}；供下游模板取值
    Error     string    // 失败原因（截断）
    LogTail   string    // 日志尾部截断存储（默认 256KB 封顶；完整日志实时走 SSE）
    Attempt   int       // 重试计数（retry-from-failed 递增）
    StartedAt *time.Time
    FinishedAt *time.Time
}
```

ER 视图：

```
Workflow（定义 · 全局唯一）
  │ 1:N 版本链（Rev 单调；编辑保存 = 追加一条）
  ▼
WorkflowRevision（rev 3 = 当前）     Spec=DAGSpec  Params  Triggers
  │ 钉住 (WorkflowID, RevisionRev)
  ▼
WorkflowRun #12（running）          Params={tag:"v1.4.0"}  TriggerRef="git:9c3f2e1"
  │ 1:N（DAGSpec 每个 node 一行，run 创建时按定义展开）
  ▼
NodeRun(build)     NodeRun(approve)   NodeRun(deploy)
  succeeded          running(等待)      pending
  Outputs={imageRef: "reg.acme/web@sha256:…"}
```

- Run 创建时按钉住的 DAGSpec **展开**全部 NodeRun 行（pending），执行只改状态不增删行——run 的拓扑在创建时刻固化，之后编辑定义不影响在跑的 run。
- 保留策略：Run 按 Workflow 保留最近 **50** 条（含 NodeRun 级联删除），同 ConfigRevision 的裁剪方式（插入后一条 DELETE，无后台任务）。
- 审批的操作人、评论不单独建表：审批动作即 NodeRun 状态流转，操作者与评论追加写入 `Outputs`（`{decision, by, comment, at}`）。

## 4. DAG 规格与模板传参

```jsonc
// WorkflowRevision.Spec（DAGSpec）
{
  "nodes": [
    { "id": "build", "type": "build-image", "name": "构建镜像",
      "onFailure": "stop",                       // stop(默认) | continue
      "props": { /* 见 §5 各节点 schema */ } },
    { "id": "gate",  "type": "approval", "name": "生产发布审批", "props": { } },
    { "id": "ship",  "type": "deploy-app", "name": "部署到 prod", "props": { } },
    { "id": "ping",  "type": "notify", "name": "通知群", "props": { } }
  ],
  "edges": [
    { "from": "build", "to": "gate" }, { "from": "gate", "to": "ship" }, { "from": "ship", "to": "ping" }
  ]
}
```

- **无显式 start/end**：无入边节点即起点，可多个（并行起点）；出度为 0 即终点。run 成功 = 全部节点 succeeded/skipped。
- **模板语法**：`{{params.<name>}}` 与 `{{steps.<nodeId>.outputs.<key>}}`，节点派发时解析；解析失败（引用不存在的参数/未成功的步骤输出）= 节点失败并给出明确错误。仅支持两层取值，不做表达式引擎。
- **参数 schema**（`Params`，`[]ParamSpec`）：`{name, type: string|number|bool|enum, required, default, options?}`。手动触发渲染表单；webhook 载荷与 cron 用 default 兜底。
- **校验**（保存时）：节点 id 唯一、edges 引用存在、**无环**（DFS）、至少一个起点、模板引用的 params/steps 存在且类型可达、审批节点不可达自身。校验失败拒绝保存（400 + 明细）。

## 5. 节点类型（MVP 四类）

### 5.1 构建镜像（build-image）

```jsonc
"props": {
  "source": { "git": { "url": "https://git.acme/web.git", "ref": "{{params.branch}}", "subdir": "" } }
          | { "http": { "url": "https://…/ctx.tar.gz" } },
  "dockerfile": "Dockerfile",          // 路径；或：
  "dockerfileContent": "",             // 全文覆盖（覆盖式）
  "args":        { "VERSION": "{{params.tag}}" },
  "platforms":   ["linux/amd64"],
  "outputRef":   "reg.acme/web:{{params.tag}}",
  "credentialRef": "gitlab-main",      // 凭证中心引用（可选）：git 拉源 + registry 推送
  "provider":    ""                    // 钉定 provider；空 = 任意
}
```

- 执行：`Deps.BuildImage(ctx, req)` → workflowadapter 组装 `fleet.Request`（git/http 源、dockerfile、args、platforms、OutputRef、Provider）并注入凭证（`fleet.Request.Creds`，SDK 原生支持按请求传递：git 凭证进 clone、registry 凭证进 solve 会话，实例不落凭证）——**进程内直调，天然绕过 REST 接线层暂不暴露凭证的缺口**（该缺口对 REST 页面另行修复，见 §9）。
- 进度：fleet 构建回调（vertex/log 事件）桥接为节点日志（SSE 实时 + LogTail 截断落库）。
- **Outputs**：`{imageRef: "reg.acme/web:v1.4.0", digest: "sha256:…", imageRefWithDigest: "reg.acme/web@sha256:…"}`——下游 deploy 节点应消费 `imageRefWithDigest`（不可变引用，可复现）。
- 取消：run 取消传播 ctx cancel 至 Solve，buildfleet 排他租约释放。
- 超时：可选 `timeout`；默认不限（长构建合法）。

### 5.2 部署应用（deploy-app）

```jsonc
"props": {
  "app":     "web",                          // 应用模块的逻辑应用名
  "cluster": "prod",                         // 目标集群
  "image":   "{{steps.build.outputs.imageRefWithDigest}}",  // 可选；空 = 按当前配置发布
  "dryRun":  false                           // true = 仅服务端 dry-run 校验
}
```

- 执行：`Deps.DeployApp(ctx, req)` → workflowadapter 调 **application 模块导出的服务门面**（M2 在 application 模块内新增导出入口，如 `syncops.DeployWithImage(ctx, app, cluster, image, operator)`——模块内部演进，不引入对 workflow 或宿主的依赖），等价于应用模块「改镜像即发布」路径：镜像写入 Manifests（追加 Source=deploy-image 版本）→ 冲突预检 → ensure namespace → Apply → 读回锚定 → 变更 preview。
- **门禁语义完整保留**：冲突（Live≠锚点）→ 节点失败，Error 含冲突明细与「去应用模块处理合并」指引；重试在人工合并后进行。create 归属校验、dry-run、逐文档权限同样生效。
- **溯源闭环**：产生的 ConfigRevision（Source=deploy-image）与 ResourceHistory（Source=workflow，含 run 序号）把 镜像 digest ↔ run ↔ 应用版本 串起来；应用详情的历史版本可反查触发来源。
- **Outputs**：`{revision: <rev>, preview: [...]}`（deploy 响应的变更文档摘要）。

### 5.3 人工审批（approval）

```jsonc
"props": {
  "assignees": [2, 7],          // 用户 id 列表；任一人批准即通过（或门）
  "timeout": "24h",             // 可选等待上限
  "timeoutAction": "reject",    // 超时动作：reject(默认，节点失败) | approve
  "message": "发布 {{params.tag}} 到生产？构建产物 {{steps.build.outputs.digest}}"
}
```

- 到达即 NodeRun=running（等待态，**不占 goroutine**——审批是事件驱动的 API 调用，天然跨重启存活）；`POST /runs/:id/nodes/:nodeId/approve|reject` 携带 `{comment}`。
- 权限：操作者须在 assignees 中（或 admin）；审批操作本身记录操作者与评论进 Outputs。
- 超时由引擎的周期巡检（复用 cron 循环的 ticker）判定，动作按 timeoutAction。
- 拒绝 = 节点失败（Error=评论），按 onFailure 传播（默认 run 失败，下游 skipped）。

### 5.4 通知（notify）

```jsonc
"props": {
  "channel": "webhook",         // v1 仅通用 webhook；渠道列表待通知中心落地后扩展
  "url":     "https://hook.acme/…",
  "payloadTemplate": "{\"text\": \"run #{{run.number}} {{run.status}}: {{params.tag}}\"}"
}
```

- v1 内置变量：`{{run.number}}`、`{{run.status}}`、`{{run.url}}`（run 视图深链）。
- 通知失败不阻断流水线（onFailure 恒为 continue，UI 灰提示）；这是「尽力而为」节点的语义。
- 站内待办：审批等待在列表页/run 视图有醒目徽标，无需通知系统。

## 6. 引擎语义（进程内状态机）

```
run: queued ──> running ──┬─> succeeded（全部节点 succeeded/skipped）
                          ├─> failed（任一 onFailure=stop 的节点失败）
                          └─> cancelled（用户取消）

node: pending ──> running ──┬─> succeeded ──(下游解锁)
              │             ├─> failed（onFailure: stop→run failed / continue→下游照常解锁)
              │             ├─> cancelled（run 取消传播）
              │             └─> interrupted（进程崩溃时在跑的节点，重启时标记）
              └─> skipped（上游 failed 且本节点需要其输出 / 取消时未开始）
```

- **调度**：编排 goroutine 维护就绪集（全部前驱已 succeeded 的 pending 节点），并发派发，全局并发上限（模块常量 `MaxConcurrentNodes = 8`）；每个 workflow 并发 run 上限（Triggers 内配置，默认 3，超出进 queued FIFO）。
- **并行分支**：DAG 天然支持分叉/汇合；汇合节点等全部入边成功。审批/构建等待不阻塞其他分支。
- **从失败节点重试**：`POST /runs/:id/retry` 把 failed/interrupted 节点及其下游 skipped 重置为 pending（succeeded 不动，Attempt+1），run 回到 running——构建产物已推 registry 的场景不需要重跑构建。
- **重启恢复**：启动扫描 status=running 的 run：任务节点 running→interrupted；审批等待原样保留；随后自动重新驱动（就绪集重算——已 succeeded 的跳过，interrupted 视为待重试的失败节点，遵循失败策略；全部可推进则继续跑）。恢复不重放已 succeeded 节点（状态即检查点）。
- **取消**：running 节点 ctx cancel（buildfleet Solve、deploy Apply 中断），pending→skipped。
- **幂等**：run 状态流转全部经条件 UPDATE（`WHERE status IN (…)`），并发 API（approve 与超时巡检竞争）以受影响行数裁决。

## 7. 触发机制

```jsonc
// WorkflowRevision.Triggers
{ "manual": true, "webhook": true, "cron": "30 2 * * *" }
```

- **手动**：`POST /workflows/:name/runs {params}`（参数按 schema 校验，必填缺失拒绝）。
- **webhook**：公开端点 `POST /api/v1/hooks/workflows/:name`（**无会话鉴权**，挂载于 routes.go 公开组——这是宿主触点白名单内 routes.go 装配的一处合法新增）；鉴权 = `X-Kite-Token: <WebhookToken>` 头（或 `?token=`）。载荷按 github / gitea / gitlab 三家 push 事件适配（识别 `X-GitHub-Event` 等特征头），提取 repo/ref/sha 填入 `{{params.branch}}` 等预留参数与 TriggerRef；非 push 事件（PR 等）v1 忽略。token 在 UI 可见、可重置（重置即失效旧值）。
- **cron**：模块内置调度循环（自带轻量 cron 解析，不依赖也不修改宿主 `pkg/scheduler`——其仅支持 interval/daily，扩展它反而增加宿主触点）；每分钟 tick，到点以 Triggers 中定义用参数 default 触发。cron 触发的 run 的 TriggeredBy 记录最后编辑该触发器的用户（权限以其为准）。
- **运行身份**：run 全程以触发用户的 RBAC 身份执行（节点执行时 Authorize 检查；webhook/cron 无会话上下文，用 TriggeredBy 的用户身份）。审批人只做决定，不改变 run 的执行身份。

## 8. 权限与审计

- 伪资源 `"workflows"`，模块内自授权（挂载于 RBAC 中间件之前，同 applications/builds 模式）：CRUD → get/create/update/delete；run/cancel/retry/approve → `exec`。
- **节点级映射**：build 节点派发前 `Authorize("builds", "create", cluster, "")`；deploy 节点由 application 模块内部的逐文档校验承接（Deps.DeployApp 传入触发用户身份）；无权限 → 节点失败并明确报错（不是静默跳过）。
- 审批：assignees 内用户或 admin（审批不检 workflows 资源 verb——assignees 列表本身就是授权）。
- 审计：run 触发/取消/重试、审批决定、定义修订经 `Deps.RecordHistory` 写 ResourceHistory（`Source: "workflow"`，detail 含 workflow/run/node 标识）。

## 9. 凭证接口契约（凭证中心 = 独立前置模块）

构建节点访问私有 git / 推送私有 registry 需要**凭证中心**先行落地（或至少最小实现）。本模块的对接形状：**节点只透传 `credentialRef` 字符串**（BuildRequest 的组成字段），凭证的解析与注入由宿主执行器侧完成——`pkg/workflowadapter` 实现 BuildImage 时经凭证中心按 ref 解析出 git/registry 凭证表，再填入 `fleet.Request.Creds`（进程内直调，SDK 原生支持按请求传递）。模块与引擎全程不接触凭证明文，也不感知凭证结构（解析失败 = 节点失败并透出来自 adapter 的明确错误）：

```go
// exec.BuildRequest 的凭证字段（模块侧只有这一形状）
type BuildRequest struct {
    // ...
    CredentialRef string // 凭证中心引用；空 = 匿名构建
}

// 凭证中心自身的接口契约（宿主侧，由其自身设计文档定稿；此处仅约定被消费面）
type Credential struct {
    Git      *GitCred    // {Username, Password} 或 {SSHKey}
    Registry []RegCred   // [{Host, Username, Password}]，最长前缀匹配（对齐 fleet.Creds.Table）
}
```

- 凭证中心自身（存储 `model.SecretString` 加密、管理 UI、RBAC）定位为独立模块/独立里程碑，不在本文档展开；落地形态可以是 `modules/credentials/` 或宿主 settings 页扩展，由其自身设计定。模块侧消费面即上文 `BuildRequest.CredentialRef` + adapter 内的解析注入。
- buildfleet **REST 接线层的凭证缺口**（手动构建页暂不能传凭证）是同根问题的另一面：fleet SDK 的 `Request.Creds` 原生支持（git 进 clone、registry 进 solve 会话），workflow 走进程内直调天然可用；REST 门面的 `buildRequest` 补 `credentialRef` 字段即可复用同一 Resolver，建议与凭证中心同期修复。

## 10. API 设计（模块内自查权限）

| 路由 | 语义 |
|---|---|
| `GET/POST /workflows` · `GET/PUT/DELETE /workflows/:name` | 定义 CRUD；PUT `{spec, params, triggers, expectedRev}` 乐观锁，保存即追加 WorkflowRevision；DELETE 拒绝存在 running run 的 workflow（409） |
| `GET /workflows/:name/revisions` · `GET .../revisions/:rev` | 定义版本链（列表 + 单版完整 spec） |
| `POST /workflows/:name/runs` | 手动触发 `{params}`（schema 校验） |
| `GET /workflows/:name/runs` | run 列表（序号/状态/触发者/耗时，倒序分页） |
| `GET /runs/:id` | run 详情（拓扑 + 全部 NodeRun 状态/输出/错误） |
| `GET /runs/:id/events` | **SSE 实时流**：节点状态流转 + 节点日志行 + run 级事件（心跳 15s，复用 buildfleet 接线的 SSE 模式）；断线重连以 NodeRun 落库状态对齐 |
| `POST /runs/:id/cancel` · `POST /runs/:id/retry` | 取消 / 从失败节点重试（仅 failed/cancelled/interrupted 态可重试） |
| `POST /runs/:id/nodes/:nodeId/approve` · `.../reject` | 审批动作 `{comment}`（assignees/admin） |
| `POST /hooks/workflows/:name` | webhook 接收（公开组，token 鉴权，三家载荷适配） |
| `GET /workflows/:name/webhook-token` · `POST .../webhook-token/reset` | token 查看/重置（update 权限） |

## 11. 前端：目录结构独立（视觉以 demo 为准）

- demo：`docs/demo/workflow.html`（顶部工具条切三视图：列表 / 画布编辑器 / run 视图；支持 `#view=editor&node=deploy`、`#view=run&node=approve` 深链）。
- **工程形态**：kite `ui/` 工程内单一自包含目录 `ui/src/workflow/`——`pages/`（列表 / 详情含编辑器 / run 三页）、`components/`（画布、属性抽屉、日志流、参数表单等模块组件）、`api.ts`（自有 API 客户端，不动共享 `lib/api.ts`）、`types.ts`、`routes.tsx`（模块路由定义，导出给宿主单块接入）、`i18n/`（自带 zh/en 资源 + 轻量 hook，读宿主语言偏好）。TanStack Query、react-router、shadcn 原语直接复用宿主已有设施（import 复用）。
- **路由**：`workflows`（列表）、`workflows/:name`（详情 + 编辑器）、`workflows/:name/runs/:runId`（run 视图），模块 `routes.tsx` 导出，宿主 `ui/src/routes.tsx` 单块插入（置于 `:resource` 通配之前，同 application 模式）。
- **编辑器**（详情页内）：React Flow 画布——左侧节点面板（四类可拖入）、中央画布（节点卡 + 贝塞尔连线 + 小地图）、右侧属性抽屉（按节点类型出表单，模板变量插入器）；顶部工具条：参数 / 触发器 / 校验 / 保存（校验规则见 §4，错误定位到节点）。画布 ⇄ YAML 双模切换进 Backlog（先画布单向）。
- **run 视图**：只读画布按节点状态着色（绿成功/红失败/蓝运行/黄等待审批/灰跳过）+ 底部节点日志抽屉（SSE 实时流）；**审批节点内联操作**（通过/拒绝 + 评论框，仅 assignees 可见按钮）；头部：run 序号、触发者与来源（git SHA 链接）、参数 chips、耗时、取消/从失败节点重试按钮。notify 模板的 `{{run.url}}` 即 `/workflows/:name/runs/:runId` 深链。
- 列表页：工作流名/描述/最近 run 状态与序号/触发器徽标（手动/webhook/cron）；行内「运行」（弹参数表单）与「编辑」。
- 入口：kite 既有「自定义侧边栏」配 `/workflows` 链接（零代码触点），或直接 URL。

## 12. 里程碑

| 阶段 | 内容 | 状态 |
|---|---|---|
| M0 | UI demo + 设计定稿 | ✅（本文档 + workflow.html） |
| M1 | 模型与引擎骨架：go.mod、Deps、Migrate、定义 CRUD + 版本链 + 乐观锁、DAGSpec 解析与校验、进程内调度状态机（并发/失败策略/取消/重启恢复）、run 展开与保留裁剪 | 待启动 |
| M2 | 节点执行器：BuildImage（buildfleet 直调 + 凭证注入，**前置：凭证中心最小实现**）、DeployApp（application 模块导出门面）、notify webhook、SSE run 事件流与 LogTail | 待启动 |
| M3 | 触发与审批：webhook 公开端点 + token + 三家载荷适配、cron 循环、审批交互与超时巡检、从失败节点重试 | 待启动 |
| M4 | 前端：`ui/src/workflow/` 自包含目录骨架（pages/components/api/i18n 自带）、React Flow 编辑器（属性抽屉/校验）、run 视图（着色/日志抽屉/审批内联）、列表页、`routes.tsx` 单块接入 + `package.json` 依赖 | 待启动 |

**Backlog（不承诺）**：通用任务节点（跑任意镜像，承载单测/trivy 扫描；需 k8s Job 执行器或 buildfleet 扩展）；画布 ⇄ YAML 双模编辑；通知渠道扩展（钉钉/企微/飞书/邮件，待通知中心）；PR 触发（v1 仅 push）；run 定时清理策略（当前 50 条保留）；手动构建页 REST 层 credentialRef 透传（与凭证中心同期）。

## 13. 验证口径

- 模块独立：`cd modules/workflow && go build ./... && go vet ./... && go test ./...`；
- 宿主：`go build ./... && go vet ./...`、既有测试通过（不新增测试）；
- UI：`pnpm --dir ui exec tsc --noEmit -p tsconfig.app.json`（模块目录在工程内，自动覆盖）；
- 提交前 `make pre-commit`。

## 14. 开源参考（仅借鉴架构，不抄代码；AGPL 项目除外）

| 项目 | 借鉴点 | 许可 |
|---|---|---|
| [Dify](https://github.com/langgenius/dify) | 工作流编辑器整体形态：React + React Flow 同构技术栈、左侧节点面板 + 右侧属性抽屉 + 运行日志逐节点查看 | Apache-2.0（附加条件） |
| [n8n](https://github.com/n8n-io/n8n) | 节点 + 凭证引用模型：凭证集中存储、节点只挂 credentialRef——§9 的直接样板 | fair-code（参考不抄码） |
| [Apache DolphinScheduler](https://github.com/apache/dolphinscheduler) | DAG 引擎语义：失败重试、超时策略、并行分支、补数；节点状态机参考 | Apache-2.0 |
| [Kestra](https://github.com/kestra-io/kestra) | run 模型：执行与定义版本分离、inputs 快照 | Apache-2.0 |
| [Argo Workflows UI](https://github.com/argoproj/argo-workflows) | run 视图：拓扑着色、节点详情抽屉的呈现密度 | Apache-2.0 |
| [@xyflow/react](https://github.com/xyflow/xyflow) | 画布库本体（MIT，可直接依赖；Dify/Windmill/Langflow 共用） | MIT |
| [Windmill](https://github.com/windmill-labs/windmill) | 编辑器交互细节参考 | **AGPL-3.0——只看架构，严禁抄码** |
