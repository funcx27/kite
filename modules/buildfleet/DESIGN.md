# buildfleet 设计方案

通用镜像构建组件：将 CI 中的镜像构建（CI 子集）抽象为独立能力，供第三方以 Go SDK（后续 gRPC 服务）调用。核心由三部分组成：**Runtime 池化调度（负载均衡）+ 数据面（代码/制品引用式传递）+ 统一构建 Runtime（BuildKit）**，docker / kubernetes 只是可插拔的供给方式（Provider）。

> 本文档为纯设计方案，不包含实现。

## 1. 总体架构

```
调用方
  │ BuildRequest (context + resources + platforms + credentials + output)
  ▼
Scheduler（负载均衡层）
  │ 均衡对象 = Runtime 实例；确定性匹配（声明式，非实测）
  ▼
Runtime 层（一等资源，与 docker/k8s 无关）
  ├─ RuntimePool：实例注册 / 记账 / 生命周期（warm/busy/draining/failed）
  ├─ RuntimeProfile 分档（small/medium/large）+ warm-pool 预热
  └─ RuntimeProvider 接口（供给层，可插拔）
       ├─ DockerProvider   （默认受限 buildkitd 容器；daemon 内置 BuildKit 仅兜底）
       ├─ K8sProvider       （buildkitd Pod，rootless）
       ├─ StaticProvider    （外接现成 buildkitd，StartupCost=0）
       └─ 第三方自定义（VM / Firecracker / 远程物理机…）
  ▼
Builder 执行层：连接 runtime 的 buildkit gRPC → Solve → push Registry
数据面（贯穿）：ArtifactStore（context / 日志 / 本地导出制品，content-addressable）
```

分层要点：

- **Runtime 是自定义创建的一等资源**，不区分 docker 还是 k8s——环境差异被压缩到 Provider 一层；
- 两层分工：Scheduler 全局选 Runtime 实例（需求-Spec 匹配），RuntimePool 决定复用热实例还是触发 Provider 供给；
- **TLS 执行、镜像构建、缓存、mirror 等能力全部下沉 BuildKit**，自研代码只做调度、供给、数据面引用传递与配置渲染。

## 2. 数据模型

```go
type BuildRequest struct {
    Context     ContextSource      // git / tar / ref，见 §6
    Dockerfile  string
    Args        map[string]string
    Platforms   []string           // 一等调度维度
    Resources   Resources          // 零值 = 按档位默认规格记账
    Credentials []Credential       // 唯一凭证表（registry + git），见 §7
    Output      ImageOutput        // 产物输出目标（对齐 buildx --output 语义）
}

type Resources struct {
    CPU    float64
    Memory resource.Bytes          // ParseQuantity("4Gi")
}

// 命名对齐 buildx --output，不使用易混淆的 "RegistryConf/Registry"
type ImageOutput struct {
    Kind     OutputKind            // image | local | oci（local/oci 预留，走 ArtifactStore 回传）
    Registry string                // image 模式：推送目标仓库地址
    Tags     []string
    TLS      TLSMode               // 预留，v1 不生效（见 §7.2）
    Mirrors  []string              // 预留：跨集群镜像 mirror，落地映射 buildkitd.toml mirrors
}

type BuildResult struct {
    Digest       string
    ImageRef     string
    Attestations []string          // provenance / SBOM 附证 digest
}
```

Runtime 层：

```go
type RuntimeSpec struct {
    Resources     Resources
    Platforms     []string         // 实例可构建平台（worker 标签）
    Capabilities  []string         // multi-arch / remote-cache / attestations…
    StartupCost   time.Duration    // Provider 实测上报，调度惩罚项用
}

type Runtime struct {
    ID    string
    Spec  RuntimeSpec
    Addr  string                   // buildkitd gRPC 地址
    State RuntimeState             // warm / busy / draining / failed
}

type RuntimePool interface {
    Acquire(ctx context.Context, spec RuntimeSpec) (*RuntimeLease, error)
}

// Provider：docker/k8s 只是两个内置实现；第三方可自定义 runtime
// Dispose 幂等：实例已不存在时返回 nil（failover 与重启 reconcile 都会调用）
type RuntimeProvider interface {
    Provision(ctx context.Context, spec RuntimeSpec) (*Runtime, error)
    Dispose(ctx context.Context, id string) error
    Capacity() Resources           // 本 provider 剩余可供给容量
}
```

分档模板：`RuntimeProfile{small: 1C2G, medium: 2C4G, large: 4C8G}`。任务 Resources 向上取整到档位，池按档位预热——避免任意规格导致池永远命不中。

## 3. 调度（负载均衡）

原则：**确定性调度，基于声明而非实测**。不采集实际资源占用参与调度（实际占用仅进 Stats() 观测）——避免时变占用导致的打分抖动与负载振荡。

```
1. 硬过滤：State=warm
           ∧ req.Resources ⊆ runtime.Spec.Resources
           ∧ req.Platforms ⊆ runtime.Spec.Platforms
           ∧ 请求能力 ⊆ Spec.Capabilities
2. 排序：startupPenalty 升序 + 最近释放优先（LRU 保活，避免轮转打穿所有实例）
   // startupPenalty 由 spec.StartupCost 归一化；池内有热实例为 0
3. 同分随机打散
4. 无匹配：池向 Provider 请求供给（决策依据 provider.Capacity() + 档位模板）
```

- 独占租用模式下 warm 实例即空闲实例，不存在"部分占用的剩余资源"，故不做装箱打分，只按冷启动惩罚 + LRU 排序；
- **满载/无容量**：阻塞等待（ctx 可取消、排队上限可配）或快速失败，二选一配置。等待队列 FIFO，扩容/释放事件广播唤醒，队头优先分配——后到请求不得越过队头直接命中新实例；扩容与空闲 Dispose 之间设滞回带（如扩容阈值深度 > 收缩阈值深度 + 余量）防伸缩抖动；
- **failover**：runtime 故障 → 归还并标记 failed → 重新 Acquire（可能触发新供给）；provider 连续供给失败才熔断 provider。重试链路总时长受构建超时上限约束，防止阻塞+重试叠加死等。

错误三分类（决定是否换实例重试）：

| 类别 | 例子 | 处理 |
|---|---|---|
| 节点类 | runtime 不可达、context 拉取失败、registry 网络超时 | 换实例重试 ≤K 次 |
| 资源类 | unschedulable、供给失败 | 退避换实例/档位，不熔断 |
| 业务类 | Dockerfile 编译失败、context 上传失败、401/403 | 不重试 |

## 4. Provider 容量与 Runtime 供给

### 4.1 Capacity 来源（声明 + 自记账，不做实测探测）

```go
func (p *k8sProvider) Capacity() Resources {
    return p.total.Sub(p.liveSum())  // total 声明值 - 当前存活 runtime Spec 之和
}
```

- Docker 节点：机器规格减系统预留，或读 provider 所属 cgroup limit（容器化部署最准）；
- K8s 集群：读 namespace ResourceQuota（K8s API），无 quota 用配置值；
- `Capacity()` 只回答"还能供给多大的 runtime"，是供给决策依据，不是任务调度依据；K8s 场景真正的资源裁决由 apiserver quota/scheduler 兜底，供给失败按资源类错误回退降档。

### 4.2 Provision 流程

```
Acquire(spec) 池内无匹配
  → 选档位（req.Resources 向上取整到 RuntimeProfile）
  → provider.Capacity() 检查
  → provider.Provision(spec)：
      Docker:  docker run <BuildkitImage> --cpus=N --memory=M（默认供给路径，rootless 变体）；
              daemon 内置 BuildKit 也建模为池内实例，但独占模式下单并发——
              定位为冷启动兜底/低频场景，不作默认
      K8s:     创建 buildkitd Pod（requests=limits=spec）
      Static:  无创建，返回预注册地址
  → 等就绪：轮询 Addr 的 gRPC health check（间隔 500ms，超时 ~60s）
  → 注册进 RuntimePool（warm）→ 立即被 Acquire 命中（busy）
```

创建是异步供给动作，不阻塞调度打分（打分只读 StartupCost）；创建失败按资源类错误处理（换档位重试或换 provider）。

### 4.3 分配模型（池化独占，唯一模式）

每任务独占租用一个 runtime，Release 回池（warm），下任务复用。不提供 shared 复用与 per-task 销毁模式——统一模型换取实现与推理的简单：

- docker 节点的 daemon 内置 BuildKit 也建模为一个池内 runtime 实例（独占租用、Capacity 由 provider 扣减），冷启动为零；但独占模式下单并发，仅作兜底而非默认并发来源；
- 强隔离/多租户/不可信 Dockerfile 场景：通过 PodTemplate/ContainerMutator 配置（rootless 镜像、无持久化、用后 Dispose 的空闲 TTL 调成 0）表达，而非独立分配模式；
- 隔离需求在 `BuildRequest.Isolation` 声明（normal | hard），hard 时 Acquire 只匹配标记为隔离档的实例池。

## 5. Runtime 生命周期与 Provider 定制

### 5.1 生命周期策略（节点/档位级配置）

- 健康检测不做定时探测，由请求侧链路完成：Provision 时一次性 gRPC ready 探测（准入）→ Solve 前轻量 Info 预检（兜底，死实例毫秒级连接拒绝即可发现）→ node-class 错误（gRPC Unavailable / 预检失败）标 Failed → reaper 及时销毁。idle 超过 TTL 的 warm 实例同样由 reaper 回收，因此死实例的滞留窗口以 idleTtl 为上限；
- 池伸缩：Acquire 排队深度超阈值且容量足够 → 扩一个；warm 超过空闲 TTL → Dispose 归还容量；draining（探活失败）先排空在途任务再销毁。
- **状态管理**：RuntimePool 纯内存，不落盘不引外部存储；重启后从 provider 实况 reconcile（扫存量容器/Pod 重建池视图），但 lease 归属不可恢复——存量实例一律先标 draining 排空（不接管在途构建，调用方经重试链路自愈）再入池；ArtifactStore 的 GC 引用计数随 Store 落盘（LocalStore 同目录小文件），重启不丢在制品保护。

### 5.2 Provider 定制（模板 + 钩子）

托管集群（阿里云 ECI 等）需要注入特定 annotations/labels（如 `k8s.aliyun.com/eci-*`），实例创建不能写死。**镜像统一、模板分开**：buildkitd 镜像引用是全局配置（版本决定能力集，统一才能保证池内 Capabilities 一致），容器/Pod 形态由各 provider 模板声明：

```go
type Config struct {
    BuildkitImage string            // 全局镜像引用（建议锁 digest），可按 RuntimeProfile 覆盖
    Docker DockerProviderConfig
    K8s    K8sProviderConfig
}

type DockerProviderConfig struct {
    BaseContainer *ContainerTemplate
    Profiles      map[string]*ContainerTemplate // 按档位覆盖
}

type K8sProviderConfig struct {
    BasePod  *PodTemplate
    Profiles map[string]*PodTemplate           // 按档位覆盖
}
// PodTemplate 字段：Annotations / Labels / NodeSelector / Tolerations /
//   Affinity / Env / Volumes / SecurityContext / ServiceAccount / PriorityClassName
// ContainerTemplate 字段：Env / SecurityOpt / Mounts / NetworkMode / ExtraArgs
// 模板不含 Image——Provision 时从全局 BuildkitImage（+ profile 覆盖）注入；
// 镜像变更 = 配置代次变更，触发存量实例滚动重建（见 §7.2）
```

1. **模板（声明式）**：ECI 镜像缓存、抢占式实例、专用节点池 tolerations 等全部配置化。合并语义：map 类键级合并（自定义优先）、列表类追加、标量整体覆盖；可按 RuntimeProfile 差异化（large 挂 PVC 缓存盘等）；
2. **Mutator（代码钩子）**：配置表达不了的动态逻辑（按 spec 计算 ECI 规格注解、从 CMDB 拉标签），Provision 创建实例前逐个调用；第三方可借此完全重写容器/Pod（含最终覆盖镜像）；
3. **StartupCost**：可按 provider 实测覆盖（ECI 冷启动 10s+）；实例注册时记录 buildkitd 版本，Capabilities 按版本推导，避免人工声明与实际不符。

## 6. 数据面（代码 / 制品 copy）

原则：**引用式传递**。调用方不向 runtime 流式上传大文件，统一走 ArtifactStore，传递 digest 引用；failover 换实例时按引用重新拉取（或命中本地缓存），零拷贝成本。

```go
type Store interface {
    Push(ctx context.Context, r io.Reader) (digest string, err error)
    Pull(ctx context.Context, digest string) (io.ReadCloser, error)
    PullToFile(ctx context.Context, digest, dir string) error
}
```

- content-addressable（sha256 为 key），同 context 去重可兼作缓存；
- 实现：LocalStore（共享目录/NFS，零依赖起步）→ S3Store（生产，后置）；
- **GC**：按 TTL 回收；进行中构建的 digest 引用计数保护；
- **ContextSource 四形态**：GitSource（runtime 侧 git fetch，凭证经 Solve session 下发）/ HTTPSource（http(s) 文件 URL，tar/tar.gz 解包或单文件，daemon 侧下载归一化为 digest；runtime 模式由 buildkitd 下载，凭证同样经 Solve session（HTTP_AUTH_*）下发）/ TarSource（tar 上传 Store）/ RefSource（透传引用），统一归一化为 `ctx://<digest>`，Builder 只认引用；
- **日志**：事件流直接透传 buildkit status progress（`Watch(ctx, fn(Event))`，不自创格式；可选 OTel 导出器）；每次尝试落 `log://<build-id>/<attempt>`，facade 聚合，failover 不丢历史；
- **镜像**：runtime 直 push 共享 Registry，调度层只传 digest 不搬运镜像。

## 7. 统一凭证与 Registry 连接

### 7.1 凭证表（对齐 docker config.json，不按 pull/push 拆分）

```go
type Credential struct {
    Kind CredentialKind   // registry | git（git 同时匹配 http 源下载 host）
    Match string          // registry 地址（可含 namespace）或 git host；最长前缀匹配
    Auth  AuthRef         // env/secret 名引用，SDK 侧解析
}
```

- **凭证经 Solve session 传递，实例无凭证**：SDK 侧解析 AuthRef 后，在发起 Solve 时随 session 附带（registry 认证 + git 认证均支持），buildkitd 拉取/推送时经 session 回叫取用——对齐 buildx docker-container/remote driver 的做法；
- 一张表同时服务：所有 `FROM` 基础镜像 pull、目标 Registry push、git clone；未命中匿名（如 Docker Hub）；
- 实例 Release 回池不残留任何凭证（多租户安全），warm 池实例无需为新凭证重建，Provider 供给层不感知凭证（不渲染 dockerconfigjson Secret、不写 config.json）；
- 约束：session 依赖调用方在线（Solve 流本身就在该连接上，天然成立）；凭证脱敏收敛在 SDK 侧事件流出口。

### 7.2 Registry 连接（静态配置，不做运行时探测）

buildkitd.toml 是启动时配置、不支持热加载，而 warm 池实例跨请求复用——按请求探测协议并渲染配置与池化不兼容。因此 registry 连接**全部来自静态配置**（provider 级声明，新建实例时渲染进 buildkitd.toml）：

```go
type RegistryConfig struct {
    Host     string   // registry 地址
    HTTP     bool     // 纯 http
    Insecure bool     // https 跳过证书校验（内网自签默认）
}
// ProviderConfig.Registries []RegistryConfig
```

- TLSMode 字段保留但语义调整：v1 不按请求生效，仅作为配置校验提示（strict 需公网证书场景，v2 再议）；
- 调用方请求的 Registry 若不在配置表内 → 业务类错误快速失败（提示补配置），而非运行时探测。SDK 侧可选做一次 /v2/ ping 仅用于提升报错质量，不参与连接决策（协议配置无法经 Solve 注入，须随实例创建渲染）；
- provider 配置新增 registry 后，存量 warm 实例不认识新 host：Acquire 硬过滤含实例配置代次匹配，配置变更将旧代次实例标 draining 滚动重建；
- Mirrors 落地时直接映射 buildkitd.toml mirrors 配置。

## 8. 资源限制（类 buildx）

- BuildRequest.Resources 零值不限 → 按档位默认规格记账，防止超卖；
- K8sProvider：Pod requests=limits（Guaranteed QoS，避免被驱逐），K8s 二次兜底调度；
- DockerProvider：默认供给受限 buildkitd 容器（--cpus/--memory），容量额度由 provider Capacity 控制；daemon 内置 BuildKit 建模为单个池内实例（零冷启动但独占模式下单并发），仅作兜底，不作默认并发来源。

## 9. 可观测性

- `Stats()`：各 runtime 的 state / 成败计数 / 冷启动耗时；实际资源占用仅观测不参与调度；
- 构建历史与 trace：透传 buildkit progress + 可选 OTel。

## 10. 与 buildx 的对比

| 维度 | buildx | 本组件 |
|---|---|---|
| driver / provider 抽象 | ✅（docker / docker-container / kubernetes / remote） | ✅ 同构，且可第三方扩展（模板 + Mutator 等） |
| builder 实例池 | 手动 create/ls | 自动 warm-pool + 分档 + 按容量供给 |
| 调度/排队/failover | 无（手动 --builder，满载报错） | ✅ 核心价值 |
| context 传输 | 流式直传 | 引用式 ArtifactStore（failover 友好） |
| registry http/自签/mirror | buildkitd.toml 声明式 | ✅ 复用 buildkitd.toml（静态配置渲染，不引入探测复杂度） |
| attestations / OTel / multi-output | ✅ | ✅ 借齐（Attestations、Output.Kind、OTel） |

已借鉴 buildx：StaticProvider（remote driver）、daemon 内置 BuildKit 复用、attestations、platform 一等维度、`--output` 多类型、OTel、buildkitd.toml registry 配置。明确不抄：本地状态文件（纯内存 + reconcile）。

## 11. 边界（明确不做）

- 通用 L4/L7 流量代理；
- 分布式状态同步（单进程内 LB，多副本各自打分，偏差可容忍）；
- 外部存储依赖（etcd / redis）；
- kaniko 等非 BuildKit 降级路径（无 gRPC Addr / health / Solve，不符合 Runtime 模型；无特权集群按资源类错误报出）；
- 跨集群镜像 mirror（Mirrors 字段已预留）。

## 12. 交付顺序

1. **MVP**：Scheduler + RuntimePool + RuntimeProfile + DockerProvider/K8sProvider + LocalStore + BuildKit runtime + 统一凭证表 + registry 静态配置；
2. **v2**：gRPC 服务、multi-arch、buildkit 远程缓存、StaticProvider、weight 自学习（perfFactor EMA）、S3Store、TLSMode strict；
3. **v3**：CRD / Operator。
