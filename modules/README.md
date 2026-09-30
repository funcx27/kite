# modules/

Kite 宿主（主 module `github.com/zxh326/kite`）之外的独立 Go module 集合。每个模块自带
`go.mod`/`go.sum`，禁止 import 宿主任何包；宿主能力一律经模块定义的 `Deps` 接口注入
（参考 `modules/application/application.go` 的包注释与 `Deps` 定义）。本文件是 modules/
目录的模块登记与契约文档。

## 模块登记

| 模块 | 用途 | 上游来源 | 接线状态 | 独立部署方式 |
|------|------|----------|----------|--------------|
| `application` | 应用发布：逻辑应用、按集群配置及其 revision 链 | 本仓库 `feat/application` 分支开发 | 已接线：`pkg/applicationadapter` 桥接 + `routes.go` 装配 | 无（随宿主单二进制发布） |
| `buildfleet` | 池化可复现容器构建系统（类似 docker buildx），Connect RPC + `buildfleetd` 守护进程 | `github.com/funcx27/buildfleet`，迁入 commit `0c9c922b6d354bb214414e530b848c68d40cac35`（2026-09-11） | 未接线宿主 | `modules/buildfleet/cmd/buildfleetd` |

## 模块契约

1. 宿主侧改动白名单仅三处：主 `go.mod` 的 require+replace、`routes.go` 装配、
   `pkg/<name>adapter/` 桥接包。模块自身初始化（含数据库迁移）一律在装配点显式触发。
2. 宿主既有 `pkg/` 包禁止 import 模块或 adapter：adapter 已 import 宿主包，反向引用
   即成既成 import cycle，也扩大合并冲突面。
3. 禁止在模块内复刻宿主安全敏感实现（加密、Secret 处理等）。宿主能力一律经模块定义的
   `Deps` 接口注入；GORM `Scan`/`Value` 等拿不到 deps 的场景，用模块导出的包级 setter
   注入，未装配时返回明确错误。
4. adapter 是唯一同时知晓宿主与模块的包，须在边界归一化宿主 API 的非惯用错误语义。
   实例：`pkg/utils.EncryptString` 失败返回 `"encryption_error: ..."` 哨兵字符串而非
   `error`，adapter 必须检测该前缀并转为 `error`。
5. 前端接入时共享组件宁 fork 到 `ui/src/components/<name>/`，不就地修改，也不改
   `ui/src/contexts` 等共享文件。
6. 迁移中发现的宿主通用修复落 `main`，不落功能分支。
7. 外部迁入的模块必须记录上游仓库与迁入时 commit（见登记表）。
8. 模块独立性验证，在模块目录内执行：

   ```sh
   cd modules/<name> && go build ./... && go vet ./... && go test ./...
   ```

## buildfleet 专属注意事项

- 模块 `go.mod` 声明 `go 1.26.3`（上游为 `go 1.27.1`，迁入时因宿主工具链仅到 1.26.x、
  且 docker/docker v28.5.1 要求 go >= 1.26.3 而下调），宿主为 `go 1.26.0`：接线时需先统一工具链。
- 接线时须在装配点原样保序初始化链：config 加载 → providers → `runtime.Pool` →
  Reconcile 认领 → store，不得重排或拆散。该链已收编进 `app.Setup`，接线直接调用；
  `app.Drain` 负责池排水并启动 store GC 周期回收（随 ctx 取消）。
- 为宿主嵌入新增两处上游 0c9c922 没有的代码（守护进程行为不变）：`app` 包（`Setup`
  保序收编 `buildfleetd` 装配链，`Drain` 池排水 + store GC 接线）；`config.LoadFromBytes`
  （供无文件路径的嵌入方使用，与 `Load` 共用合并与 KnownFields 严格校验路径）。
- api 层不得 import `provider/k8s`：跨层共享类型放 `runtime` 中立包，不用 k8s 的
  嵌入方不应被迫引入 client-go。
- 与上游 0c9c922 的已知代码偏差仅一处：`provider/docker/docker.go` 的容器资源字段。
  上游在 `container.HostConfig` 字面量里平铺 `NanoCPUs`/`Memory`，但钉定的
  docker/docker v28.5.1 将二者放在内嵌的 `Resources` 结构里（提升字段不能出现在
  复合字面量中，无法编译），故改为嵌套 `Resources: container.Resources{...}` 写法；
  字段与取值逐项相同，仅语法形式不同。
