# buildfleet

buildfleet is a Go SDK for pooled, reproducible container builds. A facade
(`fleet`) orchestrates the flow: build contexts are exchanged by digest via a
content-addressable `store`, a `runtime.Pool` hands out exclusive leases on
buildkitd instances (warm reuse, LRU keep-alive), pluggable providers
(`provider/docker`, `provider/k8s`) supply those instances, and `builder`
drives the buildkit Solve. Full rationale in [DESIGN.md](DESIGN.md).

> **Module path:** buildfleet has migrated into the `kite` repository; import it
> as `github.com/funcx27/kite/modules/buildfleet` (previously the standalone
> `github.com/funcx27/buildfleet` repo).

## Design invariants

- **Credentials travel per-Solve.** The `creds` table resolves env-var auth
  refs caller-side and attaches them via the buildkit session; runtime
  instances hold no credentials.
- **Registry config is static per instance.** `registry.Table` is rendered
  into `buildkitd.toml` at provision time; a request referencing an unknown
  registry is a business error, never a protocol probe.
- **Runtimes are exclusive.** Every build holds a lease; node-class failures
  fail over to another runtime, business errors surface immediately.

## Quickstart

```go
st := store.NewLocalStore("/tmp/buildfleet-store")
f, _ := os.Open("context.tar")
digest, _ := st.Push(ctx, f)
f.Close()

pool := runtime.NewPool(docker.New(docker.Config{
	Total:      resource.Resources{CPU: 8, Memory: 16 * resource.Giga},
	Image:      "moby/buildkit:master-rootless",
	Registries: registry.Table{{Host: "registry.local:5000", HTTP: true}},
	DockerAPI:  dockerCLI, // client.NewClientWithOpts(client.FromEnv)
}))

res, err := fleet.New(pool, nil).Build(ctx, fleet.Request{
	ContextDigest: digest,
	Dockerfile:    "Dockerfile",
	OutputRef:     "registry.local:5000/demo:latest",
})
```

See [examples/basic/main.go](examples/basic/main.go) for a runnable version
(`go run ./examples/basic context.tar`; needs a docker daemon).

## Build sources

Besides a pushed tarball/digest, a build can name its context directly:

- `fleet.Request{Git: &gitctx.Source{URL, Ref, Subdir, Mode}}` clones a git
  repo caller-side (mode `server`, default) or delegates the fetch to
  buildkitd (mode `runtime`, credentials ride the solve session).
- `fleet.Request{HTTP: &httpctx.Source{URL, Mode}}` downloads an http(s)
  URL caller-side with the same flow as git: a (possibly gzipped) tar is
  extracted into the context root, any other body becomes a single-file
  context; `mode: runtime` lets buildkitd download the URL itself, with
  credentials riding the solve session (`HTTP_AUTH_*` secrets) exactly
  like git.

Both normalize to a store digest (deterministic tars, zero ModTime), take
`AuthEnv`/`Username`/`Password` basic auth, and match host-scoped
kind-`git` entries in the request credential table. See
[examples/gitclient](examples/gitclient) and
[examples/httpclient](examples/httpclient) for API-driven versions.

## Configuration notes

- `Image` is a global buildkit image setting on the provider config — it is
  never taken from pod/container templates.
- `provider/docker.Config` takes the node's usable `Total` resources and an
  optional `DockerAPI` client override; `provider/k8s.Config` takes a
  clientset, namespace, and declarative pod templates merged with
  map-merge / list-append / scalar-replace semantics.
- The k8s `podTemplate` block is the generic managed-cluster surface: any
  annotations/labels/selector/tolerations use k8s API field names and are
  merged under buildfleet's own pod fields. `startupCost` (duration) is a
  cloud-agnostic cold-start penalty — the pool prefers cheaper warm
  candidates when several providers have warm runtimes.

## 托管集群 / 阿里云 ECI

ECI 实例就是标准 K8s Pod（ASK Serverless 集群或 ACK + ECI 虚拟节点，标准 K8s API 创建），
行为由 `k8s.aliyun.com/eci-*` 注解控制。k8s provider 提供两层配置：

1. **通用底座 `podTemplate`**：任何托管集群（ECI、Fargate、GKE Autopilot …）的
   注解/标签/调度差异直接声明，字段名同 K8s API；
2. **每云薄糖层**：`eci` 块把易错的 ECI 注解做成带校验的参数（如 spot 限价
   `"0.25,0.35"` = 每 vCPU/每 GiB 每小时最高价），翻译成注解叠加在 podTemplate 之上。
   其他云后续各加一个同模式文件即可。

```yaml
- type: k8s
  name: aliyun-eci
  total: {cpu: 64, memory: 128Gi}   # ASK 无 ResourceQuota，容量必须靠 total
  readyTimeout: 180s                # ECI 冷启动 10s+
  startupCost: 15s                  # warm 排序的冷启动惩罚
  eci:
    imageCache: true
    spotStrategy: SpotWithPriceLimit
    spotPriceLimit: "0.25,0.35"
  podTemplate:                      # 未做成参数的注解从这里透传
    annotations: {k8s.aliyun.com/eci-vswitch: vsw-xxx}
```

注意事项：`instanceTypes` 缺省不配——指定后 ECI 按 ECS 规格创建实例并忽略 pod
requests，与池的按请求规格调度冲突；不配则按 requests 精确计费（ECI 自定义规格）。
缓存仓库（`cache.enabled`）的 PVC 需 ECI 支持的 StorageClass（NAS / 云盘 CSI）。
daemon 跑在 VPC 外时配 `portForward: true`（经 apiserver 隧道访问运行时）。

## Tests

```sh
go test ./...
```


## 构建缓存

### 镜像层缓存（registry cache）

k8s provider 配置 `cache.enabled: true` 后，daemon 在集群内自动部署一个专用缓存 registry
（PVC + Deployment + Service，幂等复用），构建结束把中间层导出、下次构建导入，
缓存跨 pod / 跨 daemon 重启共享，与实例生命周期解耦：

```yaml
- type: k8s
  cache:
    enabled: true
    # image: registry:3
    # storageClass: openebs-lvm
    # size: 20Gi
```

导入导出均由 buildkitd（集群内）直连该 registry 完成，daemon 无需可达。

缓存按项目隔离：缓存 ref 的 path 由推送镜像的仓库路径派生（无推送时回退 git 路径），
如 `host:3050/team/demo:gitea` → `<cache>/team/demo`。基础镜像层按 digest 全局去重，
多项目共享同一份存储；各项目缓存链独立，删除单项目缓存（删 manifest / 删 repo 目录）
不影响其他项目。GC CronJob（`gcSchedule`，缺省每天 03:00）回收所有未被项目最新缓存链
引用的层，防无限膨胀；`off` 关闭。

### 应用依赖缓存（maven / node / go）

registry cache 不覆盖 RUN 步骤内的文件状态，两条正交路径：

1. **buildkit cache mount**（包级细粒度）：`RUN --mount=type=cache,target=/root/.m2 mvn package`。
   同一 buildkitd 实例上的所有构建共享（warm 池保持实例存活，daemon 重启后 reconcile
   接管也保留）；实例销毁即丢，持久化（每实例 PVC）为后续演进。
2. **私服代理**（生产推荐）：maven 用 nexus/artifactory、node 用 verdaccio、
   go 用 Athens 做内网 mirror，Dockerfile 内指向私服即可，daemon 无需任何改动。
   与方案 1 叠加效果最佳。

传统写法（COPY pom.xml + RUN mvn go-offline）的依赖下载层会被 registry cache 以层的
形式间接缓存，pom 不变时命中；粒度粗，多阶段构建拆分良好时已够用。

### 实例亲和（会话保持）

同一项目的构建尽量调度到上次服务它的 runtime 实例（软亲和：实例 Busy/销毁/规格不匹配时
回退常规选择，不排队）。亲和 key 为**代码仓库**（host+repo 路径，不含分支——cache mount
是累加式下载缓存，跨分支共享提高命中；monorepo 多镜像共享同一份依赖缓存）；非 git 构建
回退推送镜像路径。这样 `RUN --mount=type=cache` 的依赖缓存在实例上持续累积，maven/node
构建的依赖下载基本零耗时。实例被回收（idleTtl/gen 变更）时亲和与缓存一起失效，属预期边界。
