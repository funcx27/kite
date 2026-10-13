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
| 凭证 | 节点以 `gitCredentialRef` / `registryCredentialRef` 两个独立引用**凭证中心**（独立前置模块，见 §9）；本模块不存凭证明文，只透传给宿主能力侧 |
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
// 工作流定义（全局实体：数字 ID 是身份，Name 是可重复的显示标签）
type Workflow struct {
    ID           uint
    Name         string        // 任意 unicode（含中文），trim 后 1-63 字符；普通索引（可重复显示标签，需唯一引用处以 name#id 消歧）；Update 可改名（nil=不改）
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
Workflow（定义 · 全局实体 · ID 即身份，name 为可重复标签）
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

**空间绑定（`workflow_bindings` 表，可选可见性元数据）**：

```
WorkflowBinding(id, workflow_id FK→workflows ON DELETE CASCADE, kind, target)
  UNIQUE (workflow_id, kind, target)   INDEX (kind, target)
```

- **可选绑定**：工作流可绑定零个或多个空间；未绑定 = 平台级（仅"全部空间"视图可见）。绑定**不改变** ID 身份、命名、URL 与权限（CRUD/触发仍走通配鉴权）——纯展示/筛选元数据，纯构建等无部署引用的流水线借此获得空间归属。
- **通用 kind/target 行式设计**：kind 是代码常量（首期仅 `space`，target = 空间名 DNS-1123），未来 cluster/app/项目等新类型零 schema 变更。目标侧是刻意的软引用：空间只存在于集群侧（本地无表）、未来类型属其它模块，无处可挂外键——有效性由 API 入口按 kind 校验，集群侧消失的目标只是不再命中筛选。
- **列表的归属集合 = 绑定 ∪ 派生**（deploy-app 节点引用的空间），顶栏空间选择器按该集合联动过滤工作流列表；「全部空间」视图聚合所有工作流（未绑定的平台级流水线也只在此可见）。列表不设空间列——可见范围即页头全局空间；高频行操作（运行/运行记录/复制）内联为图标按钮，其余留在操作菜单。
- 写入随 Create/Update 既有事务整体替换（`ReplaceBindingsTx`：delete + 批量插入）；删除工作流经 FK 级联清理绑定。

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

实现分层（引擎内部）：

- **宿主能力端口**：`Deps` 的 `BuildImage`/`DeployApp`/`Notify` 三个方法（见 §2），由 `pkg/workflowadapter` 注入；引擎不感知具体能力来源。
- **内置 runner 注册表**：引擎为 build-image / deploy-app / notify 各实现一个内置 runner（`modules/workflow/internal/engine/runner.go`），装配时注册进 `Engine.runners`，并同步注册进 spec 校验的节点类型注册表；runner 负责 props 解码校验与宿主能力调用，节点级授权仅 build runner 承担（deploy 的授权由 application 部署语义内部逐文档承接，见 §8）。新增节点类型 = 实现一个 runner + 注册一行；只有节点需要新宿主能力时才扩 `Deps`。
- **引擎原生 approval**：审批不经 runner——事件驱动等待 API 决定（见 §5.3），由引擎直接管理。

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
  "gitCredentialRef": "gitlab-main",   // 凭证中心引用（可选）：git 拉源认证
  "registryCredentialRef": "reg-acme", // 凭证中心引用（可选）：registry 推送认证
  "provider":    "",                   // 钉定 provider；空 = 按 space 解析构建集群默认
  "space":       ""                    // 构建集群默认（build_settings）选择键，可选；空 = 全局默认行
}
```

- 执行：`Deps.BuildImage(ctx, req)` → workflowadapter 组装 `fleet.Request`（git/http 源、dockerfile、args、platforms、OutputRef、Provider）并注入凭证（`fleet.Request.Creds`，SDK 原生支持按请求传递：git 凭证进 clone、registry 凭证进 solve 会话，实例不落凭证）——**进程内直调，天然绕过 REST 接线层暂不暴露凭证的缺口**（该缺口对 REST 页面另行修复，见 §9）。
- **provider 解析顺序**：节点 `provider` 优先；为空时按节点 `space` 取 build_settings 的空间级默认（精确匹配， miss 再退全局默认行），`space` 为空即全局默认——space 在此只是构建默认的选择键，不承载应用身份。
- 进度：fleet 构建回调（vertex/log 事件）桥接为节点日志（SSE 实时 + LogTail 截断落库）。
- **Outputs**：`{imageRef: "reg.acme/web:v1.4.0", digest: "sha256:…", imageRefWithDigest: "reg.acme/web@sha256:…"}`——下游 deploy 节点应消费 `imageRefWithDigest`（不可变引用，可复现）。
- 取消：run 取消传播 ctx cancel 至 Solve，buildfleet 排他租约释放。
- 超时：可选 `timeout`；默认不限（长构建合法）。

### 5.2 部署应用（deploy-app）

```jsonc
"props": {
  "space":   "team-a",                       // 应用归属空间；应用身份 = (space, name)，必填
  "app":     "web",                          // 应用模块的逻辑应用名（空间内）
  "cluster": "prod",                         // 目标集群
  "image":   "{{steps.build.outputs.imageRefWithDigest}}",  // 可选；空 = 按当前配置发布
  "dryRun":  false                           // true = 仅服务端 dry-run 校验
}
```

- 执行：`Deps.DeployApp(ctx, req)` → workflowadapter 调 **application 模块导出的服务门面** `application.DeployWithImage(ctx, deps, space, app, cluster, image, registryCredentialRef, operatorID, dryRun, onLog)`（M2b 落地：模块内部演进，不引入对 workflow 或宿主的依赖），按 (space, app) 身份寻址，等价于应用模块「改镜像即发布」路径：镜像写入 Manifests（追加 Source=deploy-image 版本）→ 冲突预检 → ensure namespace → Apply → 读回锚定 → 变更 preview；发布头、逐文档 apply 结果与冲突明细按行写入 onLog（节点日志：SSE 实时 + LogTail，同 §5.1 的桥接方式）。
- **props 校验**：`space` / `app` / `cluster` 三者必填（缺失 = 节点失败，错误明确指出缺哪项）；属性面板的空间**固定不可选**——跟随流水线所属空间（未保存过空间的节点取当前锁定空间），应用列表严格限定在该空间内（裸名），**不提供跨空间的应用选择**。落库仍写 space+app 两个字段。平台工作流（未绑定空间）暂不支持部署应用节点：编辑器调色板隐藏该节点，保存校验兜底报错。
- **门禁语义完整保留**：冲突（Live≠锚点）→ 节点失败，Error 含冲突明细与「去应用模块处理合并」指引；重试在人工合并后进行。create 归属校验、dry-run、逐文档权限同样生效。
- **溯源闭环（M2b 落地形状）**：产生的 ConfigRevision（Source=deploy-image，操作者=触发用户）与 ResourceHistory（归属 application 模块，操作者=触发用户）记录每次门面发布的镜像变更；run ↔ 应用版本的关联目前以操作者+时间定位——run 序号写入审计（Source=workflow）尚未落地，列为后续项。
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

- **手动**：`POST /workflows/:id/runs {params}`（参数按 schema 校验，必填缺失拒绝）。
- **webhook**：公开端点 `POST /api/workflow-hooks/:token`（**无会话鉴权**，挂载于 routes.go 公开组——这是宿主触点白名单内 routes.go 装配的一处合法新增）；鉴权 = 路径中的 WebhookToken，恒定时间比较，未知 token 一律 404（不暴露工作流是否存在；响应只含 run id 与序号，不暴露定义细节；token 属凭据，宿主访问日志对该路径前缀脱敏）。载荷按 github / gitea / gitlab 三家 push 事件适配（识别 `X-GitHub-Event` 等特征头），提取 repo/ref/sha 填入预留参数 `branch`/`ref`/`sha`/`repo`（schema 未声明的自动忽略）与 TriggerRef（sha 优先，缺 sha 用全 ref）；非 push 事件（PR 等）、tag push、分支删除及无法识别的载荷（含无特征头且未声明 push 的载荷）v1 一律忽略（200 `{"ignored":true}`）。token 在 UI 可见、可重置（重置即失效旧值）。
- **cron**：模块内置调度循环（自带轻量 cron 解析，标准 5 段、本地时区，核心为可单测的纯函数 `NextFire`；不依赖也不修改宿主 `pkg/scheduler`——其仅支持 interval/daily，扩展它反而增加宿主触点）；cron 表达式保存期校验（非法即 400），运行期遇到不可触发的表达式记 warn；每分钟 tick，到点以 Triggers 中定义用参数 default 触发，`maxConcurrent` 预算照常生效；进程重启后错过的调度点**漏过即跳过**（不补跑）。同一 ticker 复用为审批超时巡检（§5.3）。cron 触发的 run 的 TriggeredBy 记录最后编辑该触发器的用户（权限以其为准）。
- **运行身份**：run 全程以触发用户的 RBAC 身份执行（节点执行时 Authorize 检查；webhook/cron 无会话上下文，用 TriggeredBy 的用户身份）。审批人只做决定，不改变 run 的执行身份。

## 8. 权限与审计

- 伪资源 `"workflows"`，模块内自授权（挂载于 RBAC 中间件之前，同 applications/builds 模式）：CRUD → get/create/update/delete；run/cancel/retry/approve → `exec`。**定义是全局实体（无 namespace），鉴权走通配模型**：CRUD 与触发的 authorize 一律传空 namespace——仅命中 namespace 段为通配的授权，不再逐 workflow 按 namespace 过滤；列表入口一次通配 get 校验即全量可见。
- **节点级映射（运行时鉴权不变）**：build 节点派发前以触发用户身份检查 `AuthorizeUser(触发用户, "builds", "create", "", "")`（宿主级 builds 能力，空 cluster 仅匹配通配集群授权）；provider 为空时按节点 `space` 解析构建集群默认（空 space = 全局默认行，见 §5.1）；deploy 节点由 application 模块内部的逐文档校验承接（`Deps.DeployApp` 传入触发用户身份与目标 (space, app)，见 §5.2）；无权限 → 节点失败并明确报错（不是静默跳过）。
- 审批：assignees 内用户或 admin（审批不检 workflows 资源 verb——assignees 列表本身就是授权）。
- 审计：run 触发/取消/重试、审批决定、定义修订经 `Deps.RecordHistory` 写 ResourceHistory（`Source: "workflow"`，detail 含 workflow/run/node 标识）；name 可重复，workflow 条目的 ResourceName 用 `name#id` 复合消歧（同 run 的 name#runNumber 模式）。

## 9. 凭证接口契约（凭证中心 = 独立前置模块）

构建节点访问私有 git / 推送私有 registry 需要**凭证中心**先行落地（或至少最小实现）。本模块的对接形状：**节点只透传 `gitCredentialRef` / `registryCredentialRef` 两个字符串字段**（BuildRequest 的组成字段，git 拉源认证与 registry 推送认证分开），凭证的解析与注入由宿主能力实现侧完成——`pkg/workflowadapter` 实现 BuildImage 时经凭证中心按 ref 解析出 git/registry 凭证表，再填入 `fleet.Request.Creds`（进程内直调，SDK 原生支持按请求传递）。模块与引擎全程不接触凭证明文，也不感知凭证结构（解析失败 = 节点失败并透出来自 adapter 的明确错误）：

```go
// exec.BuildRequest 的凭证字段（模块侧只有这一形状）
type BuildRequest struct {
    // ...
    GitCredentialRef      string // 凭证中心引用：git 拉源认证；空 = 匿名拉取
    RegistryCredentialRef string // 凭证中心引用：registry 推送认证；空 = 匿名推送
}

// 凭证中心自身的接口契约（宿主侧，由其自身设计文档定稿；此处仅约定被消费面）
type Credential struct {
    Git      *GitCred    // {Username, Password} 或 {SSHKey}
    Registry []RegCred   // [{Host, Username, Password}]，最长前缀匹配（对齐 fleet.Creds.Table）
}
```

- 凭证中心自身（存储 `model.SecretString` 加密、管理 UI、RBAC）定位为独立模块/独立里程碑，不在本文档展开；落地形态可以是 `modules/credentials/` 或宿主 settings 页扩展，由其自身设计定。模块侧消费面即上文 `BuildRequest.GitCredentialRef`/`RegistryCredentialRef` + adapter 内的解析注入。
- buildfleet REST 接线层与 workflow 同型：fleet SDK 的 `Request.Creds` 原生支持（git 进 clone、registry 进 solve 会话），REST 门面的 `buildRequest` 已补 `gitCredentialRef`/`registryCredentialRef` 字段，与进程内构建复用同一 Resolver。
- **已落地（M2a）**：凭证中心住 `modules/credentials/`（payload SecretJSON 加密存储 + 按名 Resolve），宿主桥接 `pkg/credentialsadapter`；`pkg/buildfleetadapter`（REST `buildRequest.gitCredentialRef`/`registryCredentialRef`）与 `pkg/workflowadapter`（进程内 `BuildImage`）经其注入的同一 Resolver 解析后填入 `fleet.Request`（git 源认证 + registry 凭证表，两个 ref 分别解析）。使用侧授权：引用凭据要求 `credentials` 资源 `get`（REST 逐请求检查 / workflow 节点以触发用户身份检查，两个 ref 分别校验）。凭证中心按「Git 凭证 / 镜像仓库凭证」两类管理，单条凭证只含其中一类。M2a 仅落地 Git basic auth（`Username`/`Password`）；`SSHKey` 形状留待后续里程碑（fleet gitctx 目前仅支持 http(s) basic auth）。

## 10. API 设计（模块内自查权限）

| 路由 | 语义 |
|---|---|
| `GET/POST /workflows` · `GET/PUT/DELETE /workflows/:id` | 定义 CRUD，按数字 ID 寻址（name 是可重复的显示标签；非法 id 400）；PUT `{spec, params, triggers, expectedRev}` 乐观锁，保存即追加 WorkflowRevision；DELETE 拒绝存在 running run 的 workflow（409） |
| `GET /workflows/:id/revisions` · `GET .../revisions/:rev` | 定义版本链（列表 + 单版完整 spec） |
| `POST /workflows/:id/runs` | 手动触发 `{params}`（schema 校验） |
| `GET /workflows/:id/runs` | run 列表（序号/状态/触发者/耗时，倒序分页） |
| `GET /runs/:id` | run 详情（拓扑 + 全部 NodeRun 状态/输出/错误） |
| `GET /runs/:id/events` | **SSE 实时流（M2b 落地形状）**：`event: node/log/run/done` + `data` JSON（形状对齐 buildfleet 接线），15s `: ping` 心跳；连接时先订阅引擎事件流、再以落库快照对齐（run 现状一帧 `run` + 全部 NodeRun 逐帧 `node`），随后消费实时事件；run 到终态发 `done` 收流；授权同 run 动作（exec）；引擎侧 per-run 订阅为带缓冲 channel、慢订阅者丢弃溢出事件（绝不反压引擎）；断线重连以 NodeRun 落库状态对齐（快照即落库值） |
| `POST /runs/:id/cancel` · `POST /runs/:id/retry` | 取消 / 从失败节点重试（仅 failed/cancelled/interrupted 态可重试） |
| `POST /runs/:id/nodes/:nodeId/approve` · `.../reject` | 审批动作 `{comment}`（assignees/admin） |
| `POST /api/workflow-hooks/:token` | webhook 接收（公开组，路径内 token 恒定时间比较、未知 token 404，三家 push 载荷适配；非 push 忽略） |
| `GET /workflows/:id/webhook-token` · `POST .../webhook-token/reset` | token 查看/重置（update 权限） |

## 11. 前端：目录结构独立（视觉以 demo 为准）

- demo：`docs/demo/workflow.html`（顶部工具条切三视图：列表 / 画布编辑器 / run 视图；支持 `#view=editor&node=deploy`、`#view=run&node=approve` 深链）。
- **工程形态**：kite `ui/` 工程内单一自包含目录 `ui/src/workflow/`——`pages/`（列表 / 详情含编辑器 / run 三页）、`components/`（画布、属性抽屉、日志流、参数表单等模块组件）、`api.ts`（自有 API 客户端，不动共享 `lib/api.ts`）、`types.ts`、`routes.tsx`（模块路由定义，导出给宿主单块接入）、`i18n/`（自带 zh/en 资源 + 轻量 hook，读宿主语言偏好）。TanStack Query、react-router、shadcn 原语直接复用宿主已有设施（import 复用）。
- **路由**：`workflows`（列表）、`workflows/new`（创建，静态段优先于 `:id`）、`workflows/:id`（详情 + 编辑器）、`workflows/:id/runs`（run 列表）、`workflows/:id/runs/:runId`（run 视图），模块 `routes.tsx` 导出，宿主 `ui/src/routes.tsx` 单块插入（置于 `:resource` 通配之前，同 application 模式）。编辑器把全局空间**锁定并同步**为流水线的绑定空间（全部空间下点开空间流水线 → 自动切到该空间；平台流水线 → 全部空间；复制/fromApp 草稿同理），编辑期间空间选择器禁用；新建沿用进入时的空间上下文（全部空间下新建 = 平台工作流）；运行页等子页切换空间则跳回列表页（对齐应用模块）；名称可编辑改名（任意 unicode，1-63 字符）。
- **编辑器**（详情页内）：React Flow 画布——左侧节点面板（四类可拖入）、中央画布（节点卡 + 贝塞尔连线 + 小地图）、右侧属性抽屉（按节点类型出表单，模板变量插入器）；顶部工具条：参数 / 触发器 / 校验 / 保存（校验规则见 §4，错误定位到节点）。画布 ⇄ YAML 双模切换进 Backlog（先画布单向）。
- **run 视图**：只读画布按节点状态着色（绿成功/红失败/蓝运行/黄等待审批/灰跳过）+ 底部节点日志抽屉（SSE 实时流）；**审批节点内联操作**（通过/拒绝 + 评论框，仅 assignees 可见按钮）；头部：run 序号、触发者与来源（git SHA 链接）、参数 chips、耗时、取消/从失败节点重试按钮。notify 模板的 `{{run.url}}` 即 `/workflows/:id/runs/:runId` 深链。
- 列表页：工作流名/描述/最近 run 状态与序号/触发器徽标（手动/webhook/cron）；行内「运行」（弹参数表单）与「编辑」。
- 入口：kite 既有「自定义侧边栏」配 `/workflows` 链接（零代码触点），或直接 URL。

## 12. 里程碑

| 阶段 | 内容 | 状态 |
|---|---|---|
| M0 | UI demo + 设计定稿 | ✅（本文档 + workflow.html） |
| M1 | 模型与引擎骨架：go.mod、Deps、Migrate、定义 CRUD + 版本链 + 乐观锁、DAGSpec 解析与校验、进程内调度状态机（并发/失败策略/取消/重启恢复）、run 展开与保留裁剪 | 待启动 |
| M2 | 宿主能力实现（引擎内置 runner 消费这些端口）：BuildImage（buildfleet 直调 + 凭证注入，**前置：凭证中心最小实现**）、DeployApp（application 模块导出门面）、notify webhook、SSE run 事件流与 LogTail | 待启动 |
| M3 | 触发与审批：webhook 公开端点 + token + 三家载荷适配、cron 循环、审批交互与超时巡检、从失败节点重试 | 待启动 |
| M4 | 前端：`ui/src/workflow/` 自包含目录骨架（pages/components/api/i18n 自带）、React Flow 编辑器（属性抽屉/校验）、run 视图（着色/日志抽屉/审批内联）、列表页、`routes.tsx` 单块接入 + `package.json` 依赖 | 待启动 |

**Backlog（不承诺）**：通用任务节点（跑任意镜像，承载单测/trivy 扫描；需 k8s Job 宿主能力——新内置 runner + 新 Deps 方法——或 buildfleet 扩展）；画布 ⇄ YAML 双模编辑；通知渠道扩展（钉钉/企微/飞书/邮件，待通知中心）；PR 触发（v1 仅 push）；run 定时清理策略（当前 50 条保留）。

## 13. 验证口径

- 模块独立：`cd modules/workflow && go build ./... && go vet ./... && go test ./...`；
- 宿主：`go build ./... && go vet ./...`、既有测试通过（不新增测试）；
- UI：`pnpm --dir ui exec tsc --noEmit -p tsconfig.app.json`（模块目录在工程内，自动覆盖）；
- 提交前 `make pre-commit`。

## 14. 开源参考（仅借鉴架构，不抄代码；AGPL 项目除外）

| 项目 | 借鉴点 | 许可 |
|---|---|---|
| [Dify](https://github.com/langgenius/dify) | 工作流编辑器整体形态：React + React Flow 同构技术栈、左侧节点面板 + 右侧属性抽屉 + 运行日志逐节点查看 | Apache-2.0（附加条件） |
| [n8n](https://github.com/n8n-io/n8n) | 节点 + 凭证引用模型：凭证集中存储、节点只挂凭证引用——§9 的直接样板（本项目已细化为 git/registry 双 ref） | fair-code（参考不抄码） |
| [Apache DolphinScheduler](https://github.com/apache/dolphinscheduler) | DAG 引擎语义：失败重试、超时策略、并行分支、补数；节点状态机参考 | Apache-2.0 |
| [Kestra](https://github.com/kestra-io/kestra) | run 模型：执行与定义版本分离、inputs 快照 | Apache-2.0 |
| [Argo Workflows UI](https://github.com/argoproj/argo-workflows) | run 视图：拓扑着色、节点详情抽屉的呈现密度 | Apache-2.0 |
| [@xyflow/react](https://github.com/xyflow/xyflow) | 画布库本体（MIT，可直接依赖；Dify/Windmill/Langflow 共用） | MIT |
| [Windmill](https://github.com/windmill-labs/windmill) | 编辑器交互细节参考 | **AGPL-3.0——只看架构，严禁抄码** |
