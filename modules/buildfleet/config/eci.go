package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ECI tunes k8s-provider pods for Alibaba Cloud Elastic Container
// Instances (ASK serverless clusters, or ACK with the ECI virtual node):
// typed fields translated to k8s.aliyun.com/eci-* annotations with
// validation, because the annotation formats (e.g. the spot price pair)
// fail silently when mistyped. Anything not covered here goes through
// podTemplate.annotations verbatim; on key clashes ECI wins.
//
// This file is the per-cloud "thin sugar" pattern: future managed clouds
// (CCI, EKS, ...) each get an equivalent file — struct + validate +
// Template — and one Provider field.
type ECI struct {
	// ImageCache speeds cold starts by reusing ECI image caches
	// (buildkit images are large; recommended).
	ImageCache bool `yaml:"imageCache"`
	// SpotStrategy: "SpotAsPriceGo" (spot at market price) |
	// "SpotWithPriceLimit" (spot below SpotPriceLimit). Empty = on-demand.
	SpotStrategy string `yaml:"spotStrategy"`
	// SpotPriceLimit caps spot pricing as "vCPUPrice" or
	// "vCPUPrice,perGiBPrice" (CNY per hour). Requires
	// SpotWithPriceLimit.
	SpotPriceLimit string `yaml:"spotPriceLimit"`
	// InstanceTypes pins ECS instance types (first available wins).
	// Leave empty: with it ECI ignores pod requests and sizes by the
	// type, breaking the pool's per-request spec model; empty keeps
	// custom ECI specs billed by the requested vCPU/GiB.
	InstanceTypes []string `yaml:"instanceTypes"`
}

func (e *ECI) validate() error {
	switch e.SpotStrategy {
	case "", "SpotAsPriceGo", "SpotWithPriceLimit":
	default:
		return fmt.Errorf("spotStrategy must be SpotAsPriceGo or SpotWithPriceLimit, got %q", e.SpotStrategy)
	}
	if e.SpotPriceLimit == "" {
		if e.SpotStrategy == "SpotWithPriceLimit" {
			return fmt.Errorf("spotStrategy SpotWithPriceLimit requires spotPriceLimit")
		}
		return nil
	}
	if e.SpotStrategy != "SpotWithPriceLimit" {
		return fmt.Errorf("spotPriceLimit requires spotStrategy SpotWithPriceLimit")
	}
	parts := strings.Split(e.SpotPriceLimit, ",")
	if len(parts) > 2 {
		return fmt.Errorf("spotPriceLimit must be vCPU[,perGiB] prices, got %q", e.SpotPriceLimit)
	}
	for _, p := range parts {
		if v, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err != nil || v <= 0 {
			return fmt.Errorf("spotPriceLimit must be positive numbers, got %q", e.SpotPriceLimit)
		}
	}
	return nil
}

// Template translates the typed fields into an annotations-only override
// PodTemplate, merged over the provider's podTemplate by buildfleetd.
func (e *ECI) Template() *PodTemplate {
	ann := map[string]string{}
	if e.ImageCache {
		ann["k8s.aliyun.com/eci-image-cache"] = "true"
	}
	if e.SpotStrategy != "" {
		ann["k8s.aliyun.com/eci-spot-strategy"] = e.SpotStrategy
	}
	if e.SpotPriceLimit != "" {
		parts := strings.Split(e.SpotPriceLimit, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
		}
		ann["k8s.aliyun.com/eci-spot-price-limit"] = strings.Join(parts, ",")
	}
	if len(e.InstanceTypes) > 0 {
		ann["k8s.aliyun.com/eci-instance-type"] = strings.Join(e.InstanceTypes, ",")
	}
	return &PodTemplate{Annotations: ann}
}
