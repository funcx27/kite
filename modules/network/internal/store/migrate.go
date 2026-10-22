package store

import (
	"fmt"

	"gorm.io/gorm"
)

// seedAnnotationKeys is the initial annotation key catalog, mirroring the
// confirmed prototype (ui/src/portal/network/mock.ts).
var seedAnnotationKeys = []AnnotationKey{
	{Key: "nginx.ingress.kubernetes.io/force-ssl-redirect", Description: "强制将 HTTP 重定向到 HTTPS（true/false）", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/ssl-redirect", Description: "配置了证书时是否重定向到 HTTPS，控制器默认 true", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/proxy-body-size", Description: "请求体大小上限，控制器默认 1m，0 表示不限制", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/proxy-read-timeout", Description: "后端读超时（秒），控制器默认 60", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/proxy-send-timeout", Description: "向后端发送的超时（秒），控制器默认 60", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/proxy-connect-timeout", Description: "连接后端的超时（秒），控制器默认 5", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/rewrite-target", Description: "重写目标路径，如 /$1 配合捕获组使用", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/use-regex", Description: "路径启用正则匹配（true/false）", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/whitelist-source-range", Description: "客户端来源 IP 白名单，CIDR 逗号分隔", IngressClass: "nginx"},
	{Key: "nginx.ingress.kubernetes.io/proxy-buffering", Description: "响应缓冲开关，SSE/流式响应需设为 off", IngressClass: "nginx"},
	{Key: "traefik.ingress.kubernetes.io/router.entrypoints", Description: "Traefik 路由入口点（如 web, websecure）", IngressClass: "traefik"},
	{Key: "konghq.com/strip-path", Description: "转发前剥去匹配的路径前缀（true/false）", IngressClass: "kong"},
	{Key: "alb.ingress.kubernetes.io/listen-ports", DefaultValue: `[{"HTTP":80},{"HTTPS":443}]`, Description: "关联监听端口/协议，决定了 Ingress 挂到哪些监听上", IngressClass: "alb"},
	{Key: "alb.ingress.kubernetes.io/ssl-redirect", DefaultValue: "false", Description: "HTTP 308 跳转 HTTPS，需同时关联 80 与 443 监听", IngressClass: "alb"},
	{Key: "alb.ingress.kubernetes.io/healthcheck-enabled", DefaultValue: "false", Description: "开启后端健康检查，开启后通常再配 healthcheck-path", IngressClass: "alb"},
	{Key: "alb.ingress.kubernetes.io/rewrite-target", Description: "路径重写，用法同 nginx 的 rewrite-target", IngressClass: "alb"},
	{Key: "alb.ingress.kubernetes.io/backend-protocol", DefaultValue: "http", Description: "后端协议 http/https/grpc，HTTPS 监听才能转发 gRPC 后端", IngressClass: "alb"},
	{Key: "alb.ingress.kubernetes.io/order", DefaultValue: "10", Description: "转发规则优先级 1-1000，数值越小越优先，路径重叠时必配", IngressClass: "alb"},
}

// seedDefaultBackfill fills DefaultValue on catalog rows created before the
// column existed. It only touches rows whose value is still empty, so admin
// edits survive re-runs.
var seedDefaultBackfill = map[string]string{
	"nginx.ingress.kubernetes.io/proxy-body-size":       "1m",
	"nginx.ingress.kubernetes.io/proxy-read-timeout":    "60",
	"nginx.ingress.kubernetes.io/proxy-send-timeout":    "60",
	"nginx.ingress.kubernetes.io/proxy-connect-timeout": "5",
}

// Migrate creates or updates the module tables on the host database and seeds
// the annotation key catalog. Seeding is firstOrCreate-by-key: re-running
// neither duplicates entries nor overwrites rows edited by admins.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&Host{}, &AnnotationKey{}); err != nil {
		return fmt.Errorf("migrate network models: %w", err)
	}
	for i := range seedAnnotationKeys {
		seed := seedAnnotationKeys[i]
		if err := db.Where("key = ?", seed.Key).FirstOrCreate(&seed).Error; err != nil {
			return fmt.Errorf("seed annotation key %s: %w", seed.Key, err)
		}
	}
	return backfillAnnotationDefaults(db)
}

// backfillAnnotationDefaults fills the still-empty default_value of the
// pre-column nginx seed rows.
func backfillAnnotationDefaults(db *gorm.DB) error {
	for key, value := range seedDefaultBackfill {
		err := db.Model(&AnnotationKey{}).
			Where("key = ? AND (default_value = '' OR default_value IS NULL)", key).
			Update("default_value", value).Error
		if err != nil {
			return fmt.Errorf("backfill default of annotation key %s: %w", key, err)
		}
	}
	return nil
}
