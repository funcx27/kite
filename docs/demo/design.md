# 应用模块（Application）设计方案

> 状态：设计定稿，UI 已有静态 demo（[application.html](./application.html)），后端/前端实现待启动。
> 目标：把 Deployment/StatefulSet + Service + Ingress 抽象为一个「应用」，给研发与非专业人员使用；多集群；轻量、简洁，不做 KubeVela 式强制同步。

## 1. 已确认的核心决策

| 决策点 | 结论 |
|---|---|
| 模块形态 | 仓库内独立 go module：`modules/application/`，零 kite import，依赖经 `Deps` 注入；kite 以 `replace` 引用 + 薄适配层。未来可整体抽出为独立仓库 |
| 配置粒度 | 逻辑「应用」仅是分组；**每个目标集群一份独立完整配置**，支持一键复制到其他集群 |
| 同步语义 | 双向、显式触发：发布=push（DB→集群），拉取=pull（集群→DB，无损收编）。无 controller、无后台 reconcile、无自动修复 |
| 冲突门禁 | 检测到集群侧手动修改（Live ≠ 锚点）时**禁止发布**：deploy 预检拒绝并返回冲突明细，唯一出路先**合并处理**（三方合并，见 §4）。原则：发布永不覆盖集群中的手动修改（避免生产事故）——集群侧修改要么被合并采纳、要么在合并预览中显式回退（知情覆盖），绝不静默丢失 |
| 生命周期 | 集群级 = **下线**（双选项：**仅下线**（默认）——删集群资源、配置与版本链保留、重发布恢复原配置；**下线并删除配置**——连配置与版本链一并清理，不可恢复；无「仅删配置不删资源」诉求，不做）；应用级 = **删除**（仅应用列表入口，存在已部署（未下线）的集群配置时禁止） |
| 配置表示 | **完整多文档 manifests** 存 DB（不是仅结构化 spec）。表单只是 manifests 上已知字段的视图；YAML 模式可编辑整份 manifests（高级扩展口） |
| 漂移检测 | 仅在查看时按需检测（详情页打开/手动刷新），列表页展示聚合状态 |
| 身份标记 | label 三件套（见 §5），不用 ownerReferences |

**为什么配置存完整 manifests**：拉取（收编）要求把用户在集群里手改的任意字段无损带回 DB；若只存表单覆盖的结构化 spec，收编时表单未覆盖的修改会丢失，与「避免两边配置不一致」的目标矛盾。表单字段通过 `ExtractFields/InjectFields` 在 manifests 上做手术式读写，未知字段原样保留。

## 2. 总体架构

```
ui (React) ── /api/v1/applications* ──> kite routes.go（挂载点，RBAC 中间件之前）
                                          │
                                   application.RegisterRoutes(api, deps)   ← 独立 module
                                          │ Deps 注入
                              ┌───────────┴───────────┐
                        ClusterResolver          Authorize / RecordHistory
                        (取集群 client.Client)     (rbac.CanAccess / ResourceHistory)
                              │
                       各目标集群 API Server
```

- `modules/application` 自带 `go.mod`，直接依赖仅：gin、gorm、controller-runtime `client`、`sigs.k8s.io/yaml`。
- kite 侧适配层 `pkg/applicationadapter`（约 60 行）：
  - `ClusterResolver.Resolve(ctx, name)` → 包装 `cm.GetClientSet(name).K8sClient`（`client.Client` 自带 `RESTMapper()`，无需额外暴露）；
  - `Authorize(c, resource, verb, cluster, ns)` → `rbac.CanAccess`；
  - `RecordHistory(entry)` → 写 `model.ResourceHistory`（`Source: "application"`）。
- 挂载：`routes.go` `registerClusterProtectedRoutes` 中 `api.Use(middleware.RBACMiddleware())` 之前（同 `resources/apply` 模式），自动获得 `/_clusters/:cluster` 双前缀形态；URL 非 resource 形态，权限在模块内自查。
- 持久化：模块自带 GORM 模型，`pkg/model/model.go` 的 `InitDB` 追加 `application.Migrate(DB)`。

## 3. 数据持久化模型

```go
// 逻辑应用（分组 + 描述 + 创建草稿，全局唯一）
type App struct {
    ID          uint
    Name        string     // uniqueIndex, DNS-1123, ≤63（同时用作子资源名与 label 值）
    Namespace   string     // 归属空间（创建时取当前空间；各 AppConfig.Namespace 的默认值）
    Description string
    Spec        JSONField  // AppSpec：创建向导产物（草稿），见下方「AppSpec 流转」
}

// 向导六步的结构化产物（不是渲染后的 manifests；渲染 = Render(App.Spec, namespace) 纯函数）
type AppSpec struct {
    Workload     WorkloadSpec  // 类型 / 镜像 / 副本 / env / 资源请求限额
    MountPoints  []string
    ConfigFiles  []ConfigFile // §9
    Ports        []PortSpec   // §8：端口 + 类型 + 可选域名
    IngressClass string
    ExtraYAML    string
}

// 每集群一份配置（持久化核心）
type AppConfig struct {
    ID             uint
    AppID          uint   // (AppID, Cluster) uniqueIndex
    Cluster        string // kite 集群名
    Namespace      string
    Manifests      JSONField  // 期望配置：多文档 []unstructured（表单/YAML 编辑的对象）
    LastApplied    JSONField  // 一致性锚点：上次发布/拉取后从集群读回的规范化对象
    StatusSummary  string     // synced / drift / conflict / merged / undeployed / error（详情页检测后回写）
    StatusCheckedAt *time.Time // 最近检测时间；列表聚合读该持久化值，不实时 diff
    OverriddenPaths JSONField // 合并预览中显式回退的集群侧漂移路径（知情覆盖白名单；发布读回锚定后清空）
    LastDeployedAt *time.Time
    LastSyncedAt   *time.Time
}

// 库侧配置的版本快照（每次落库记一条，支持回滚与版本对比）
type ConfigRevision struct {
    ID          uint
    AppConfigID uint      // index, OnDelete:CASCADE（下线/删配置时级联清理）
    Rev         int       // per-AppConfig 单调递增；裁剪不回收（空缺保留，「回滚到 rev 37」语义稳定）
    Source      string    // render(首次发布渲染) / wizard(保存配置) / deploy-image(改镜像) / sync(收编) / merge(冲突合并) / copy / yaml / rollback
    Manifests   JSONField // 该次落库后的整份多文档快照（Secret 值经 KITE_ENCRYPT_KEY 加密，展示脱敏）
    OperatorID  uint
    CreatedAt   time.Time
}
```

**关联关系（ER 视图，以 my-app 为例）**：

```
App（应用定义 · 全局唯一 · 与集群无关）
  ID=1 · Name="my-app" · Namespace="default" · Spec=AppSpec（出生快照）
  ［data-worker：只有 App 行、无任何 AppConfig → 未部署，Spec 为活跃草稿］
        │
        │ 1:N —— (AppID, Cluster) 唯一索引：一个应用在一个集群至多一份配置
        │
        ├────────────────────────┬────────────────────────┘
        ▼                        ▼
AppConfig #101                AppConfig #102
  Cluster="prod-cluster"        Cluster="staging-cluster"
  Namespace="default"           Namespace="default"
  Manifests   期望配置(多文档)     Manifests   期望配置
  LastApplied 锚点(读回)         LastApplied 锚点
  StatusSummary=synced          StatusSummary=conflict
  OverriddenPaths=[]            OverriddenPaths=[]
  LastDeployedAt/LastSyncedAt   LastDeployedAt/LastSyncedAt
        │                        │
        │ 1:N 版本链               │ 1:N 版本链
        ▼                        ▼
ConfigRevision 链（prod）      ConfigRevision 链（staging）
  AppConfigID=101                AppConfigID=102
  rev 1  Source=render           rev 1  Source=render        首次发布 Render(App.Spec)
  rev 2  Source=deploy-image     rev 2  Source=deploy-image  改镜像 1.24→1.25
  rev 3  Source=sync             rev 3  Source=merge         冲突合并：采纳 replicas 3、保留 image 1.25
  ...                            rev 4  Source=rollback      回滚到 rev 2
  （本链内 Rev 递增，保留最近 50 条）（独立链：Rev 计数/裁剪/回滚均在单链内，与 prod 互不相干）
```

- 集群内的对象不在 DB 中：Manifests 各文档与集群对象按 `(kind, ns, name)` 对齐，并以 `kite.application/name` label 认领（§5）。
- **版本链各集群独立**：不存在跨集群的「应用版本」；复制到其他集群 = 深拷贝 Manifests 开新链（Rev 重新从 1 计，Source=copy）。
- 级联：删除 App → 全部 AppConfig 及版本链（含下线保留的配置）；**下线不删任何行**——仅清空 LastApplied、StatusSummary=undeployed，配置与版本链原地保留。
- 草稿（App.Spec）不在任何版本链上；所有改库动作（含首次发布渲染）在对应链上追加一条快照。

要点：

- **空间（Space）**：namespace 的产品化命名，应用归属空间（App 级默认空间，各集群配置可单独改，即 `AppConfig.Namespace`，展示名为空间）。
- **空间身份标记**：namespace 打 label `kite.application/space: "true"`；空间切换器只列出带该标记的命名空间，避免混入 kube-system 等无关空间。选 label 而非 annotation：label 支持 selector 查询（一次 List 取回全部空间）、有格式校验，且与应用的 `kite.application/name` 标记体系统一；annotation 仅作补充元数据（显示名/描述）。向导「＋ 新建空间」创建命名空间时自动打该标记。
- **空间切换入口（唯一）**：应用列表页头、与「搜索应用」同一行（搜索框左侧），侧边栏收起时仍可见可调。弹层为空间较多设计——搜索框按子串过滤 + **定高滚动列表**（当前空间高亮并自动滚动到可见处）+「全部空间」固定项 +「＋ 新建」入口，创建命名空间时自动打标记。当前空间决定列表默认范围，**创建向导直接使用当前空间**（不提供空间选择控件）；可切「全部空间」查看聚合。前端实现为 SpaceProvider（与 ClusterProvider 同级），侧边栏不再重复提供入口以免双入口。不新增租户层级；未来若需跨 namespace 的逻辑分组，可再引入「项目」层。
- **空间常显与标识规则**：创建向导头部始终展示当前空间徽标（全部步骤可见）；应用名称统一以 `空间/应用名` 复合形式展示（空间段只读，不可编辑），列表与详情同规则——目的：用户始终明确知道操作发生在哪个空间。
- **应用模式无「添加集群」操作**：向导目标集群即**用户有权限的集群集合**（RBAC 过滤，有什么权限显示什么）；为已有应用追加绑定集群属专业模式的管理操作，应用模式不出现该入口。
- **AppSpec 流转（创建不绑定集群的存储答案）**：创建应用 = 仅写 App 一行（含 Spec 草稿），**不建任何 AppConfig**（它是 (AppID, Cluster) 维度，无集群即无行）→ 列表「未部署」。首次发布 `POST /configs {cluster}` 由 `Render(App.Spec)` 生成该集群配置（Manifests=渲染产物，Namespace 默认 App.Namespace）。此后 **App.Spec 退役为出生定义快照**：日常编辑都在对应集群 Manifests 上（Extract/Inject），追加集群走复制现有 Manifests（不用陈旧草稿）；从未发布期间（无任何 AppConfig）的「编辑」= 向导回填 App.Spec。草稿编辑不进 ConfigRevision 版本链（版本挂 AppConfig，有发布语义才开始计版本）。**下线保留配置**（LastApplied 清空、状态 undeployed）：重新发布直接用保留的 Manifests（下线前最后配置，非初始草稿），走 create 模式；下线态编辑 = 编辑该保留配置。
- 复制到其他集群 = 深拷贝 `Manifests`，`LastApplied` 清空（视为未部署/未对齐）。
- 集群挂掉不影响配置读取（数据在 kite DB），恢复后可直接发布。
- **库侧版本管理（ConfigRevision）**：库侧配置每次落库——向导「保存配置」、发布弹窗改镜像、收编、冲突合并、复制、回滚——写一条整份快照；**发布动作本身不新增版本**（推送的就是已保存的那份）。保留策略：每配置固定保留最近 **50 条**（模块常量 `MaxConfigRevisions`，插入后一条 `DELETE WHERE rev <= maxRev-50`，无后台任务；真不够再议配置项，不提前抽象）。约 0.25–0.75MB/集群配置封顶。版本链只管回滚与版本对比；细粒度操作审计（谁、何时、结果）仍由 kite `ResourceHistory` 审计流水承担，二者互补。

## 4. 差异对比机制（三向语义对比 + 读回锚定）

**不使用 resourceVersion**：它是 etcd 修订号，status-only 写、HPA 调副本、控制器加注解都会 bump；DB 侧不存在可对应的稳定值；只回答「变过没有」，给不出字段级差异，跨集群无可比性。

采用 kubectl diff 的三向模型：

1. **发布后读回锚定**：Apply 成功后 Get live 对象 → `Normalize` → 存入 `LastApplied`。API Server 自动补全的默认值（strategy、imagePullPolicy、terminationGracePeriod 等）随之固化进锚点，消除「刚发布就报一堆假差异」的默认值噪音。读回为 best-effort，失败随 deploy 结果返回。
2. **拉取重新对齐**：live 对象 `Normalize` 后**同写** `Manifests` 与 `LastApplied`；库侧存在未发布修改时前端警告「将被覆盖」。
3. **统一规范化 `Normalize(obj)`**：剥 `resourceVersion/uid/generation/creationTimestamp/managedFields/status/selfLink`、Service `clusterIP/clusterIPs/nodePort`（按需）、控制器注解（`deployment.kubernetes.io/revision` 等 curated 列表）。两侧同函数。
4. **两组差异**：
   - `diff(Live, LastApplied)` → 集群侧漂移（别人改了集群）；
   - `diff(Manifests, LastApplied)` → 库侧待发布（用户改了库）。
   - 仅集群侧 → 合并即整份收编（全部采纳）；仅库侧 → 建议发布；两侧都有 → **冲突**，并列展示，经合并预览裁决（demo staging Tab 即此形态）。
5. **文档集对齐**：按 `(kind, namespace, name)` 对齐；DB 有而集群无 → 未部署/已删除；label 命中受管 kinds 而清单外 → 可拉取收编。
6. **字段级差异**：遍历规范化 JSON 树收集变更叶路径（如 `spec.template.spec.containers[0].image`），封顶 20 条/文档。
7. 可选加速：live `metadata.generation` 与锚点一致时跳过深比较（generation 只随 spec 变化自增，可作短路预判，不产出差异内容）。

**冲突处理 = 三方合并（不覆盖）**：`merge(base=LastApplied, ours=Manifests, theirs=Live)`——三份输入齐备，无需新数据：

- 叶子路径：库未动 → 采纳集群值；集群未动 → 保留库值；双方改同一字段且值不同 → **碰撞**，用户逐字段二选一（采纳集群值 / 保留库值）；
- 列表：env/ports/volumes 等按**原子**处理（任一侧变即整列表变更，双方都变 → 碰撞）；`containers` 按 name 键合并；文档集按 `(kind, ns, name)` 对齐，新增/删除沿用收编规则；
- 合并预览中每个**自动采纳项可单独回退**（回到基线值，记入 `OverriddenPaths` = 知情覆盖）；「整份覆盖式拉取」的效果可由「碰撞全采纳集群值 + 保留自动项」达成，不再单独提供覆盖式拉取；
- 仅集群侧差异（库无待发布）时合并退化为整份收编——同一动词覆盖两种场景；
- 确认合并：merged 结果写入 Manifests（记 Source=merge 版本，碰撞选择与回退清单随请求提交）→ 前端自动进入发布弹窗正常发布；LastApplied 待发布读回后更新、`OverriddenPaths` 清空。
- 紧急修镜像流程：改镜像 → 发布被拦 → 合并（镜像改动自动保留、集群修改自动采纳）→ 发布，**改动全程不丢**。

## 5. 身份标记：label 三件套（不用 ownerReferences）

所有渲染/收编的对象统一打：

```yaml
labels:
  app.kubernetes.io/name: <app>          # k8s 惯例
  app.kubernetes.io/managed-by: kite     # kite 既有约定
  kite.application/name: <app>           # 模块身份：selector 查询 / 资源发现 / 收编
```

空间（命名空间）单独打 `kite.application/space: "true"`（模块创建空间时自动打上）；空间切换器与空间列表仅以该 label 为准。选型说明：身份标记一律用 label（可 selector、有校验、与应用标记统一），annotation 仅承载展示性元数据。

不用 ownerReferences 的原因：应用定义在 kite DB 中，集群内没有可作 owner 的 CR；若把 Deployment 设为 Service/Ingress 的 owner，会引入 k8s GC 的隐式级联删除，与「删除 = 按清单显式删文档」的语义冲突。label 天然支持 List/selector，且不触发集群自身行为。

## 6. 同步语义

| 操作 | 行为 |
|---|---|
| 发布（deploy） | **冲突预检**：对每个集群侧漂移字段 p（live≠锚点），要求 `manifests[p]==live[p]`（合并已采纳）或 `p ∈ OverriddenPaths`（合并预览显式回退 = 知情覆盖），否则拒绝（409 + 冲突明细，UI 引导合并）；发布读回锚定后清空 OverriddenPaths。**前置 ensure namespace**：`AppConfig.Namespace` 幂等存在（缺失则创建并自动打空间标记；无权限则明确报错）。通过后逐文档：RESTMapper 判 scope → `rbac.CanAccess`(create/update) → Get→Create/Update（带 resourceVersion）→ 写 ResourceHistory → 读回锚定。逐文档聚合结果，部分失败不整体失败。请求体可选 `image` 字段（**改镜像即发布**）：后端先把镜像写入 Manifests 并持久化，再整体 apply，库/集群保持一致 |
| 合并（merge） | 冲突处理动作：`GET merge-preview`（三方合并计算，无写入）→ 用户裁决碰撞/回退 → `POST merge`（携带选择与回退清单）→ 写 Manifests + Source=merge 版本 + 回写 OverriddenPaths → 前端自动进入发布弹窗（见 §4） |
| 拉取（sync） | 整份收编（导入已有应用 / 仅集群侧差异的对齐）：读取对象 = Manifests/LastApplied 身份 ∪ 按 `kite.application/name` label 命中的受管 kinds（Deployment/StatefulSet/Service/Ingress）→ Normalize → 覆盖双快照 |
| 复制配置 | 深拷贝源集群 `Manifests` → 新集群配置；`LastApplied` 清空；可随后改 host 等字段再发布 |
| 下线（offline） | 集群级生命周期操作：按**渲染输出 + ExtraYAML 清单**删除集群对象（对称、可预测，不做 label 全域扫除）。**双选项（弹窗 radio，默认仅下线）**：① 仅下线——保留该集群 AppConfig 与版本链（LastApplied 清空、StatusSummary=undeployed），重新发布直接用保留的 Manifests 走 create 模式（恢复下线前配置，非初始草稿）；② 下线并删除配置——同时删除该集群 AppConfig 与版本链（不可恢复）；若应用因此无任何集群配置 → 回到未部署形态，再次部署走首次发布（Render 出生定义，弹窗需明示）。不做「仅删配置不删资源」（无诉求） |
| 删除应用 | 仅应用列表入口；**存在未下线（已部署）的集群配置时拒绝**（409，提示先在各集群下线）；全部下线或从未部署时允许，级联清理保留的下线配置与版本，无集群资源需处理 |
| 版本记录 | 上述任一改库动作落库时同步写一条 `ConfigRevision` 整份快照（发布不新增版本；Source 含 merge，见 §3）；保留最近 50 条 |

**发布安全（create 语义 + 服务端 dry-run）**：

- **首次发布 = create，绝不覆盖**：AppConfig 无锚点（新建/下线后重新发布）时逐文档走 **Create**；目标对象已存在且身份 label（`kite.application/name`）不属于本应用 → **拒绝发布**（明确报错"集群中已存在同名对象且非本应用管理"，绝不静默接管/覆盖）。后续发布中文档被删后重建的路径同规则。收编（sync / 导入）是唯一显式接管无 label 对象的路径。
- **服务端 dry-run**：deploy 支持 `{dryRun: true}`——冲突预检 + 三向 diff + 对全部文档执行 API Server `dry-run: All`（抓 admission webhook 拒绝、ResourceQuota 超限、schema 校验、PodSecurity 等 DB 侧预览抓不到的问题），不落库不 apply；发布弹窗打开时自动调用渲染安全预览，dry-run 失败禁用确认。
- 选型注记：创建不绑定集群（App.Spec 草稿）vs 创建即绑定——前者创建不依赖集群权限、未部署语义干净（无 AppConfig 行 = 无配置才可删）；后者的"保存时即可集群侧校验"优势由首次发布 dry-run 补齐。维持不绑定。

## 7. API 设计（模块内自查权限）

| 路由 | 语义 |
|---|---|
| `GET/PUT/DELETE /applications`、`GET/PUT/DELETE /applications/:name` | 逻辑应用 CRUD；创建/更新携 `{description, spec}`（草稿，见 §3 AppSpec 流转）；DELETE 应用仅当全部集群配置已下线（或无配置）时允许（存在已部署配置则 409，提示先下线），删除级联清理保留下来的下线配置与版本链 |
| `POST /applications/:name/configs` | 新增集群配置：`{cluster}`（默认 `Render(App.Spec)` 渲染、namespace 缺省 App.Namespace——**首次发布/重新发布走此路径**）或 `{cluster, copyFrom}`（复制现有集群）；发布弹窗在应用无集群配置时提供目标集群选择（RBAC + 在线状态过滤），选定即创建配置并发布，此后集群固定 |
| `GET/PUT /applications/:name/configs/:cluster` | GET：manifests + Extract 的 spec + 漂移摘要；PUT：`{spec}`（Inject）或 `{manifests}`（整份替换），携带 `expectedRev`（最新版本号）做乐观锁，不匹配返回 409「配置已被他人修改」。无 DELETE——配置删除统一并入下线端点，不提供「仅删配置」路径 |
| `POST .../configs/:cluster/offline` | 下线：删集群对象；body `{deleteConfig?: false}`——false（默认，仅下线）保留配置与版本链（锚点清空、状态 undeployed）；true（下线并删除配置）连配置与版本链一并删除（不可恢复） |
| `GET .../configs/:cluster/merge-preview` · `POST .../configs/:cluster/merge` | 冲突合并：预览返回自动采纳项 + 碰撞字段（无写入）；确认携带碰撞选择与回退清单，写 Manifests + Source=merge 版本（见 §4/§6） |
| `POST .../deploy` · `POST .../sync` | 发布（冲突预检 + create 归属校验 + 可选 `{dryRun: true}`，见 §6 发布安全；`image?` 快捷改镜像）/ 整份收编 |
| `GET .../configs/:cluster/revisions` · `GET .../revisions/:rev` | 历史版本：列表（Rev/Source/操作人/时间，含脱敏标记）与单版整份 manifests（Secret 值脱敏）；任意两版对比由前端取两份计算 |
| `POST .../configs/:cluster/rollback` | `{rev}`：该版 `Manifests` 写回当前配置（新增一条 Source=rollback 的版本），变为待发布态——仍须走发布弹窗与冲突门禁后才推送 |
| `GET .../status` | 按需漂移报告（两组差异 + 建议动作），计算后回写 `StatusSummary`/`StatusCheckedAt`（列表聚合读持久化值，可批量刷新） |
| `GET .../resources` | 运行视图聚合（§8） |
| `POST /applications/preview` | spec → 渲染 YAML（纯计算，向导预览） |

权限：定义 CRUD 走 `Authorize(c,"applications",verb,cluster,ns)`（默认角色 admin `*` 全通过、viewer 仅 get）；deploy/sync/deleteResources 逐文档映射真实资源校验对应 verb；运行视图逐 section 校验（无权限段置 unauthorized 隐藏）。审计经 `RecordHistory` 钩子。

## 8. 网络模型（简化：一张端口列表）

**向导「网络访问」步骤只有一张端口列表**（不区分 Service/Ingress 两块）。每行 = 一个端口：

- **端口 + 类型（默认 ClusterIP，可选 NodePort / LoadBalancer）+ 域名（可选）+ 路径（默认 /）**；
- 不要求协议（固定 TCP；UDP 等特殊需求走 YAML 模式）；**端口名称自动生成**（如 `tcp-80`，中划线：容器端口名与 Service 端口名均为 DNS-1123 风格标签，下划线会被 API Server 拒绝），用户无需填写；
- **域名留空 = 仅集群内访问**；对外访问由行内「＋ 对外域名」按钮显式开启，展开后才出现域名/路径输入（**填写 = 同时生成一条 Ingress 规则**，`域名/路径 → 该端口`），默认行保持收敛；
- Render 按端口类型分组生成 Service：ClusterIP 端口合并为一个 Service；每个 NodePort / LoadBalancer 端口生成独立对应类型的 Service（类型是 Service 级属性，由端口级选择推导）。

即「端口 + 类型 + 可选域名」一行一个心智，多 Service、特殊端口映射等少数场景走 ExtraYAML / YAML 模式。

AppSpec 对应结构（节选）：

```go
type PortSpec struct {
    Port int32
    Type string // clusterip（默认）/ nodeport / loadbalancer
    Host string // 空 = 仅 Service；非空 = 同时生成 Ingress 规则
    Path string // 默认 "/"
    Name string // 可选；空则按 tcp-<port> 自动生成
}
// Service/Ingress 文档由 Render 按 Ports 推导，不再单独建模
```

## 9. 配置管理与挂载（ConfigMap / Secret）

**应用自带配置文件是一等能力，但是可选项**——向导独立一步「配置文件」，默认为空态并提示可直接跳过（不是所有应用都有配置文件）：

- **挂载点集合 + 文件下拉归属（方案 C）**：路径集合默认 `/etc/<app>`，可「＋ 挂载点」新增（校验：以 `/` 开头的绝对路径、不可重复；无文件绑定时才可删除）；文件添加/编辑时挂载路径为**下拉选择**（不手输，消灭拼写错误），后端仍校验 `ConfigFile.MountPath` 必须命中路径集合；
- 列表为紧凑行并**按挂载点分组展示**（组头 = 路径 + 文件数），路径可见性随分组天然解决；「添加文件」入口与挂载点一致：空态虚线框可点击、有文件时为列表底部小字链接（不放右上角）；**点击文件名弹出悬浮编辑窗（可全屏）**：文件名 / 挂载路径（下拉）/ 内容 / 敏感标记；
- 非敏感文件 → 渲染为 `<app>-config` **ConfigMap**（`data`）；勾选「敏感」→ 渲染为 `<app>-secret` **Secret**（`stringData`）；
- Render 自动注入 `volumes`（configMap / secret 引用）与 `volumeMounts`，默认以 **subPath 逐文件挂载**（`mountPath: <挂载点>/<文件名>`，`subPath: <文件名>`）——只覆盖单个文件，**不遮蔽镜像内该目录下的其他文件**；目录级整目录挂载属高级需求，走 YAML 模式；
- 配置文档随 manifests 一起**发布 / 拉取（收编 kinds 含 ConfigMap/Secret）/ 漂移检测（data 差异）/ 复制 / 删除**，行为与Deployment 等文档完全一致；
- **引用已有的共享 ConfigMap/Secret**、envFrom、subPath 等高级用法 → ExtraYAML / YAML 模式（v1 不做共享配置中心）；
- **敏感数据处理**：模块 DB 中 Secret 内容沿用 `KITE_ENCRYPT_KEY` 加密存储；审计（RecordHistory）与「查看 YAML」对 Secret 的 data/stringData 值脱敏为 `***`；详情页 Secret 只可查看文件名列表。

AppSpec 增量（节选）：

```go
type ConfigFile struct {
    Name      string // 同时作为挂载后的文件名与 data key
    MountPath string // 必须命中 MountPoints；UI 为下拉选择
    Content   string
    Secret    bool   // true → 存入 Secret
}
type WorkloadSpec struct {
    // ...
    MountPoints []string     // 路径集合，默认含 /etc/<app>
    ConfigFiles []ConfigFile
}
```

## 10. 运行视图聚合（用户维度，一屏呈现）

`RuntimeView(ctx, cli, cfg)` 一次返回（demo 详情页同屏展示，无需跳转 k8s 资源页）：

- 工作负载 rollup：ready/replicas、conditions；
- Pod 列表：按工作负载 `spec.selector.matchLabels` 查询（名称/状态/**实时 CPU/内存（metrics API）**/重启/启动时间/节点）；
- Service：端口映射、类型、ClusterIP/NodePort；
- Ingress：host/path、LB 地址；
- 最近 Warning 事件：workload + pods 的 involvedObject，fieldSelector 过滤。

**重启能力**：详情页支持「应用重启」（滚动重启工作负载，复用 kite 既有 restart 能力，打 `kite.kubernetes.io/restartedAt`）与 Pod 级重启（删除 Pod 由控制器重建）。

## 11. 前端（视觉与交互以 demo 为准）

- **工作模式（全局 UI 状态切换，不是子页面）**：kite 提供两种模式——**应用模式**（普通用户默认：侧边栏仅「应用」，应用工作台即主页，空间切换在应用列表页头与搜索同行；不设仪表盘——集群监控视角对普通用户价值有限，总览需求由应用列表的状态聚合承担，未来可加应用维度的总览页）与**专业模式**（现有 k8s 资源全集：工作负载 / 网络 / Helm / 配置 / 集群管理）。切换入口为用户菜单中的**工作模式**单选项（应用模式不出现集群切换器——那是专业模式的全局上下文；专业模式不出现应用入口，两模式互不跳转），属用户级偏好（存用户配置/localStorage，可按角色给默认值，如 viewer 默认应用模式；后续可演化为管理员给用户强制绑定模式）。应用页面不再是 k8s 资源侧边栏中的一个子页。
- demo：`docs/demo/application.html`（顶部工具条切换四视图；支持 `#view=detail&tab=staging`、`#view=wizard&step=5` 深链）。
- 列表页：ActionTable，集群状态 chips 聚合（已同步/有漂移/异常/未部署）——聚合读各配置持久化的 `StatusSummary`（详情页检测回写，见 §3），非实时 diff，可手动批量刷新；范围由页头空间选择器决定（与「搜索应用」同行，可切「全部空间」），表格含「空间」列；集群筛选内嵌在「集群状态」列头（下拉，+ 点击行内集群 chip），避免与全局「当前集群」混淆。
- Pod 交互：名称可点击打开 **Pod 监控弹窗**（CPU/内存曲线，时间范围切换；弹窗内直达 终端/日志/重启）；行内操作提供 重启/终端/日志。
- 空间呈现：向导不提供空间选择控件，第 1 步与目标集群步骤均**只读展示当前空间**（如需更换先在侧边栏切换）；详情页标题旁展示所属空间。
- 页头右侧为**全局集群切换器**（kite 全局上下文：决定资源详情跳转、终端/日志等集群级操作的目标集群）。应用列表本身跨集群聚合，不受全局集群影响——集群维度的过滤在列表「集群状态」列头。
- **创建流程**：创建应用 = 单个向导一次完成，**只保存定义（未部署，不选集群）**；首次发布在详情页对指定集群进行，与「追加集群配置」是同一机制（`POST /configs`）。App / AppConfig 的拆分是**存储结构**而非操作步骤；向导中「目标集群」步骤为**只读展示**：编辑模式显示发布入口所在的集群，创建模式显示「未绑定集群（未部署）」。
- 详情页：**集群 Tab 布局**；选中集群纵向排开：概览条（Ready/同步状态/时间 + **发布/重启/下线/⋯**）→ Pod 表 → 网络卡（复制按钮）→ 配置一致性卡（两组差异分栏 + 冲突处理动作）→ Warning 事件。**合并/拉取不设常驻按钮**——作为冲突处理动作由一致性卡与发布弹窗冲突态提供。**下线弹窗为双选项**（radio 卡片，默认「仅下线」高亮）：仅下线——删集群资源、配置与历史版本保留，Tab 切换为「已下线」空态，「重新发布」在原 Tab 直接进行、使用保留的原配置走 create 模式；下线并删除配置——确认按钮转危险样式，连配置与版本链一并清理（弹窗明示不可恢复；若应用因此无集群配置，再次部署走首次发布/初始定义）。详情页无删除入口，**删除应用仅在应用列表且存在已部署配置时禁止**（提示先下线）。
- **未部署应用的首次发布**：无集群配置的应用（创建向导保存后），发布入口提供目标集群选择（RBAC + 在线状态过滤），选定即 `Render(App.Spec)` 创建该集群配置并走发布弹窗「首次发布」形态（预览展示将创建的资源而非差异）；列表「部署」按钮同入口。从未发布期间的「编辑」= 向导以创建形态回填 App.Spec 草稿（不涉集群）。**仅下线后的重新发布不走此入口**——集群已绑定，在原集群 Tab 直接 [重新发布]，使用保留的原配置（create 模式）；「下线并删除配置」后应用回到无配置形态，再次部署走此入口（初始定义）。
- 向导：六步（基本信息 → 运行配置 → 配置文件（见 §9）→ 网络访问（端口列表：端口 + 类型 + 可选域名，名称自动生成，见 §8）→ **目标集群（只读展示，不可修改）** → 预览与高级），复用 `EnvironmentEditor/ResourceEditor/ImageEditor`；编辑态同组件，Form 模式提交 `{spec}`，YAML 模式提交 `{manifests}`。末步「预览与高级」**即高级模式**：渲染预览 ⇄ YAML 模式切换，可编辑整份 manifests（表单未覆盖字段原样保留），不设独立的高级编辑弹窗。
- 详情页 Pod 行内操作：**终端 / 日志**（复用 kite 既有 WebSocket 端点 `/api/v1/terminal/:ns/:pod/ws`、`/logs/:ns/:pod/ws` 及 FloatingTerminal/日志组件，模块不新建端点；多集群经 `x-cluster-name` 寻址）。
- **发布弹窗 = 修改 + 发布的唯一入口**（详情页不设「编辑」按钮）：内置**镜像快捷修改**（改镜像即发布，见 §6）与**[修改配置]**（打开向导，末步 YAML 模式即高级模式）两个修改面；变更预览动态跟随当前 Tab 的集群与同步状态，无变更时禁用确认。预览含**服务端 dry-run 校验**结果（admission/quota/schema，见 §6 发布安全），失败禁用确认；**首次发布为 create 模式**（仅创建，目标对象已存在且非本应用管理时拒绝并报错）。
- **冲突禁止发布 → 处理冲突（三方合并）**：发布弹窗打开时若检测到集群侧手动修改（Live ≠ 锚点），切换为冲突态——展示冲突明细、禁用确认，唯一动作「处理冲突（合并）」打开**合并预览弹窗**：自动采纳项（集群侧/库侧来源标记，每项可单独回退=知情覆盖）+ 碰撞字段（基线/库值/集群值三列，二选一，未选完禁用确认）；「确认合并并发布」写库（Source=merge 版本）后自动回到发布弹窗正常态发布。紧急修镜像流程中库侧改动全程不丢。仅集群侧差异（无库侧改动）时同一入口退化为整份收编预览。
- **向导不再直接发布**：向导是纯编辑器，末步按钮随模式——编辑模式 **[保存配置] / [去发布]**（后者暂存修改并返回发布弹窗，经变更预览确认后才推送），创建模式 **[创建应用]**（保存定义后回到列表，应用状态「未部署」）；发布动作只存在于发布弹窗，保证冲突门禁不可绕过。
- 发布/拉取确认后状态即时流转：漂移 chip → 已同步、一致性卡切换为同步态、上次发布/拉取时间更新、Tab 状态点变色（对应「读回锚定/重置锚点」语义）。
- **历史版本**：详情页 ⋯ 菜单「历史版本」打开该集群配置的版本列表面板（Rev / 来源 / 操作人 / 时间，含脱敏标记），支持任选两版对比；「回滚到此版」仅将该版 `Manifests` 写回为待发布状态（新增一条 Source=rollback 的版本），经发布弹窗变更预览与冲突门禁确认后才推送——回滚不直接触碰集群。
- 接入：`routes.tsx` 静态路由（`applications`、`applications/:name`，置于 `:resource` 通配之前）；侧边栏 `sidebar.groups.application` 分组，`SIDEBAR_CONFIG_VERSION` 需递增；`en.json/zh.json` 同步。

## 12. 里程碑

| 阶段 | 内容 | 状态 |
|---|---|---|
| M0 | UI demo + 设计定稿 | ✅ |
| M1 | 模块骨架：go.mod、Deps、模型与 Migrate（含 ConfigRevision 版本快照与 50 条保留策略、StatusSummary/OverriddenPaths）、kite 适配层、路由挂载、应用/配置 CRUD（含 expectedRev 乐观锁） | 待启动 |
| M2 | 渲染器 + 发布（ensure namespace/冲突预检/Apply/读回锚定）+ 合并（三方 merge）+ 收编 + 复制 + 下线/删除 | 待启动 |
| M3 | 漂移报告（Normalize/Diff + StatusSummary 回写）+ 运行视图聚合 | 待启动 |
| M4 | 前端接入（路由/双模式侧边栏/API hooks/页面：发布弹窗/合并预览/首次发布/历史版本）+ i18n | 待启动 |

**Backlog（不承诺）**：列表「导入」入口（收编 helm/kubectl 部署的存量应用，sync 语义已支持）；HPA 字段忽略（ArgoCD ignore-differences 式——合并已缓解大半：库不碰 replicas 即无碰撞；HPA 管理的工作负载建议 YAML 模式删除 replicas 字段）。

## 13. 验证口径

- 模块独立：`cd modules/application && go build ./... && go vet ./...`；
- 宿主：`go build ./... && go vet ./...`、既有测试通过（不新增测试）；
- UI：`pnpm --dir ui exec tsc --noEmit -p tsconfig.app.json`；
- 提交前 `make pre-commit`。
