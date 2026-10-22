package store

import "time"

// Model carries the columns shared by the module's mutable tables.
type Model struct {
	ID        uint      `json:"id" gorm:"primarykey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Host is one registered domain: unique per (cluster, host). Namespace always
// equals the space the host was created in; the column doubles as the space
// dimension of the listing index.
type Host struct {
	Model
	Host         string `json:"host" gorm:"column:host;type:varchar(253);not null;uniqueIndex:idx_network_hosts_cluster_host,priority:2"`
	Cluster      string `json:"cluster" gorm:"column:cluster;type:varchar(63);not null;uniqueIndex:idx_network_hosts_cluster_host,priority:1;index:idx_network_hosts_space,priority:2"`
	Namespace    string `json:"namespace" gorm:"column:namespace;type:varchar(63);not null;index:idx_network_hosts_space,priority:1"` // = space
	IngressClass string `json:"ingressClass" gorm:"column:ingress_class;type:varchar(63);not null"`

	// TLSCertificate names the platform certificate; empty means no TLS.
	TLSCertificate string    `json:"tlsCertificate" gorm:"column:tls_certificate;type:varchar(63);not null;default:''"`
	CertSynced     bool      `json:"certSynced" gorm:"column:cert_synced;not null;default:true"`
	Annotations    JSONField `json:"annotations" gorm:"column:annotations;type:text"` // map[string]string, catalog keys only
	Routes         JSONField `json:"routes" gorm:"column:routes;type:text"`           // []Route

	// DesiredRev/AppliedRev derive the pending status: DesiredRev moves on
	// every spec edit, AppliedRev only on successful publish.
	DesiredRev  uint       `json:"desiredRev" gorm:"column:desired_rev;not null;default:1"`
	AppliedRev  uint       `json:"appliedRev" gorm:"column:applied_rev;not null;default:0"`
	PublishedAt *time.Time `json:"publishedAt" gorm:"column:published_at"` // nil = undeployed
	LastError   string     `json:"lastError" gorm:"column:last_error;type:text;not null;default:''"`
}

func (Host) TableName() string { return "network_hosts" }

// AnnotationKey is one entry of the global annotation key catalog: key is
// unique, there is no space or origin dimension.
type AnnotationKey struct {
	Model
	Key          string `json:"key" gorm:"column:key;type:varchar(316);not null;uniqueIndex"`
	Description  string `json:"description" gorm:"column:description;type:varchar(255);not null;default:''"`
	DefaultValue string `json:"defaultValue" gorm:"column:default_value;type:varchar(255);not null;default:''"`
	IngressClass string `json:"ingressClass" gorm:"column:ingress_class;type:varchar(63);not null;default:''"`
}

func (AnnotationKey) TableName() string { return "network_annotation_keys" }
