# modules/

Kite 宿主（主 module `github.com/zxh326/kite`）之外的独立 Go module 集合。每个模块自带
`go.mod`/`go.sum`，禁止 import 宿主任何包；宿主能力一律经模块定义的 `Deps` 接口注入
（参考 `modules/application/application.go` 的包注释与 `Deps` 定义）。本文件是 modules/
目录的模块登记与契约文档。

## 模块登记

| 模块 | 用途 | 上游来源 | 接线状态 | 独立部署方式 |
|------|------|----------|----------|--------------|
| `application` | 应用发布：逻辑应用、按集群配置及其 revision 链；导出部署门面 `DeployWithImage`（身份化发布管线，冲突返回 `*ConflictError` 明细，供 workflow deploy 节点进程内复用，见 `deploy.go`） | 本仓库 `feat/application` 分支开发 | 已接线：`pkg/applicationadapter` 桥接 + `routes.go` 装配（`Deps` 含按 id 的 `AuthorizeUser`，门面与 API 共用同一套 Deps 接线） | 无（随宿主单二进制发布） |
| `buildfleet` | 池化可复现容器构建系统（类似 docker buildx），Connect RPC + `buildfleetd` 守护进程 | `github.com/funcx27/buildfleet`，迁入 commit `0c9c922b6d354bb214414e530b848c68d40cac35`（2026-09-11） | 已接线（REST+SSE 门面 + 进程内嵌入） | `modules/buildfleet/cmd/buildfleetd` |
| `workflow` | CI/CD 编排：DAG 工作流定义（构建镜像/审批/部署应用/通知）、revision 链与进程内调度引擎、触发面（手动 / webhook 公开端点 `POST /api/workflow-hooks/:token`，token 恒定时间比较 + github/gitea/gitlab push 载荷适配；cron 内置每分钟调度循环，漏过即跳过；同一 ticker 复用为审批超时巡检）、从失败节点重试（设计文档 `docs/demo/workflow-design.md`） | 本仓库 `feat/platform` 分支开发 | 已接线：`pkg/workflowadapter` 桥接 + `routes.go` 装配（Migrate 显式触发 + 启动恢复；webhook 公开路由在公开区单点挂载） | 无（随宿主单二进制发布） |
| `credentials` | 凭证中心：命名凭证（git basic auth / registry 认证）加密存储与按名解析（设计 `docs/demo/workflow-design.md` §9） | 本仓库 `feat/platform` 分支开发 | 已接线：`pkg/credentialsadapter` 桥接 + `routes.go` 装配（Migrate 显式触发）；Resolver 由 `pkg/buildfleetadapter`（REST `credentialRef`）与 `pkg/workflowadapter`（进程内构建）消费 | 无（随宿主单二进制发布） |

## 模块契约

1. 宿主侧改动白名单：主 `go.mod`（require+replace）与 `go.sum`、`routes.go` 装配、
   进程装配点（`app.go`/`main.go`；模块配置节 `internal/config.go` 与生命周期
   Close/Drain `app.go` 是装配点的合法延伸，同属其组成部分）、
   `pkg/<name>adapter/` 桥接包。模块自身初始化（含数据库迁移）一律在装配点显式触发。
2. 宿主既有 `pkg/` 包禁止 import 模块或 adapter：adapter 已 import 宿主包，反向引用
   即成既成 import cycle，也扩大合并冲突面。
3. 禁止在模块内复刻宿主安全敏感实现（加密、Secret 处理等）。宿主能力一律经模块定义的
   `Deps` 接口注入；GORM `Scan`/`Value` 等拿不到 deps 的场景，用模块导出的包级 setter
   注入，未装配时返回明确错误。
4. adapter 是唯一同时知晓宿主与模块的包，须在边界归一化宿主 API 的非惯用错误语义。
   实例：`pkg/utils.EncryptString` 失败返回 `"encryption_error: ..."` 哨兵字符串而非
   `error`，adapter 必须检测该前缀并转为 `error`。
5. **前端目录独立**：模块前端代码一律住 `ui/src/<name>/` 自包含目录（pages / components /
   api / types / `routes.tsx` 模块路由定义 / `i18n/` 自带 zh/en 资源），不向 `ui/src/pages`、
   `ui/src/components`、`ui/src/lib` 等共享目录添加文件。跨模块的平台壳（工作模式、空间、
   应用模式侧边栏、首页路由）住 `ui/src/platform/`。模块 i18n 资源经模块入口
   `i18n.addResourceBundle(lng, 'translation', res, true, true)` 深合并注册进共享 translation
   命名空间（键一律带模块前缀命名空间，如 `applications.*`、`buildfleet.*`）——**不改共享
   `en.json`/`zh.json`**。宿主共享文件的合法集成点仅限白名单（见下），集成点之外共享组件
   宁 fork 进模块目录、不就地修改。
6. 迁移中发现的宿主通用修复落 `main`，不落功能分支。
7. 外部迁入的模块必须记录上游仓库与迁入时 commit（见登记表）。
8. 模块独立性验证，在模块目录内执行：

   ```sh
   cd modules/<name> && go build ./... && go vet ./... && go test ./...
   ```

## 前端宿主集成点白名单

模块在宿主共享文件中的全部合法触点（2026-10-02 前端降耦合改造后收敛）：

| 文件 | 集成点 | 归属 |
|------|--------|------|
| `ui/src/routes.tsx` | 每模块一个路由块（import 模块 `routes.tsx` 并展开） | 各模块 |
| `ui/src/App.tsx` | `PlatformProviders` 包裹 + 应用模式侧边栏切换（集中式壳集成） | platform |
| `ui/src/components/user-menu.tsx` | `<WorkModeMenuItem />` 单点插入 | platform |
| `ui/src/components/settings/settings-sections.tsx` | 设置页注册表条目（kcross tab；workflow credentials tab，M4 凭证管理段——tab label 键 `settings.tabs.credentials` 由 workflow 模块 i18n 资源注册；用户拍板留在统一设置界面） | kcross / workflow |

其余仍在宿主共享文件中的 diff 均为**加法式通用增强**（可选 prop / i18n 化 / 开发环境修复），
登记备查：`editors/{environment,image}-editor`（i18n 化，键 `editors.*` 由 application 模块资源
注册）、`log-viewer-content`（日志过滤重放 + cluster 参数）、`pod-file-browser` /
`pod-monitoring` / `terminal-content`（可选 cluster 参数，应用 Pod 视图跨集群寻址）、
`yaml-diff-viewer`（hideUnchanged 与 action 插槽）、`action-table`（`actionCellPrefix` 可选
prop）、`lib/api/core.ts` 与 `lib/api/observability.ts` 与 `lib/current-cluster.ts`（可选
cluster 参数与 `clusterHeader`）、`vite.config.ts`（WSL2 inotify 轮询修复）。上游同步时这些
文件是仅有的可能冲突点，均应可机械解。

## buildfleet 专属注意事项

- 模块 `go.mod` 声明 `go 1.26.3`（上游为 `go 1.27.1`，迁入时因宿主工具链仅到 1.26.x、
  且 docker/docker v28.5.1 要求 go >= 1.26.3 而下调）；接线时宿主 `go.mod` 已同步提升至
  `go 1.26.3`，两个 module 工具链一致。
- 接线时须在装配点原样保序初始化链：config 加载 → providers → `runtime.Pool` →
  Reconcile 认领 → store，不得重排或拆散。该链已收编进 `app.Setup`，接线直接调用；
  `app.Setup` 以装配 ctx 启动 store GC 周期回收（宿主与守护进程均传入进程级长生命
  周期 ctx，随 ctx 取消停止）；`app.Drain` 负责池排水并执行终局 GC 一趟。
- 为宿主嵌入与接线新增三处上游 0c9c922 没有的代码：`app` 包（`Setup` 保序收编
  `buildfleetd` 装配链并启动 store GC，`Drain` 池排水 + 终局 GC）；
  `config.LoadFromBytes`（供无文件路径的嵌入方使用，与 `Load` 共用合并与 KnownFields
  严格校验路径）；REST/SSE 门面 `pkg/buildfleetadapter`（`POST /builds` 以 SSE 流式
  回传构建进度，`GET /status`、`GET /cachestat` 查询，挂载于宿主 RBAC 中间件之前、
  逐请求自授权 `builds` 伪资源）。
- 守护进程行为有一处变化：改经 `app.Setup` 装配后，`buildfleetd` 同样随装配 ctx
  启动周期 store GC（上游 0c9c922 的独立守护进程从不运行 GC）。
- 前端导航曾经是对契约第 5 条的一处已登记偏差（在 `sidebar-config-defaults.ts` 就地新增
  菜单项并 bump `SIDEBAR_CONFIG_VERSION`）；2026-10-02 前端降耦合改造已消除——该文件恢复
  上游零差异，构建页入口改为 kite 既有「自定义侧边栏」配置或直接 URL（`/buildfleet`）。
- api 层不得 import `provider/k8s`：跨层共享类型放 `runtime` 中立包，不用 k8s 的
  嵌入方不应被迫引入 client-go。
- 与上游 0c9c922 的已知代码偏差仅一处：`provider/docker/docker.go` 的容器资源字段。
  上游在 `container.HostConfig` 字面量里平铺 `NanoCPUs`/`Memory`，但钉定的
  docker/docker v28.5.1 将二者放在内嵌的 `Resources` 结构里（提升字段不能出现在
  复合字面量中，无法编译），故改为嵌套 `Resources: container.Resources{...}` 写法；
  字段与取值逐项相同，仅语法形式不同。
