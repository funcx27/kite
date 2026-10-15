# Buildfleet 模块（chart 部署）

可选的 buildfleet 模块（池化可复现容器构建），内嵌在 kite 进程中。
`config.buildfleet` 子树会原样传给模块的配置加载器，未知字段会在启动时报错；
省略该段落即保持模块禁用。

- `store` 应放在 `/data` 下以复用既有的持久卷挂载（与 sqlite PVC 同一卷）。
  `/data` 仅在启用 `db.sqlite.persistence` 时存在——使用 postgres/mysql 或关闭
  持久化时它是容器临时存储，blob 不会在 Pod 重启后保留。
- 不支持多副本（内存运行时池 + 本地存储）：`replicaCount` 必须保持 `1`。
- **Provider 不在这里配置**：chart 部署下，托管的 Provider 存于数据库，通过平台
  构建设置界面注册与编辑；在添加第一个 Provider 之前，构建会按请求失败。yaml 的
  `providers:` 列表仅适用于独立部署的 buildfleetd（见模块的
  `buildfleet.example.yaml`）。

```yaml
config:
  buildfleet:
    store: /data/buildfleet-store
```

核心 chart 参数见 [chart-values.md](./chart-values.md)。
