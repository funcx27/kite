// Package networkadapter wires kite capabilities to the network module's
// Deps interface. It is the only kite-side package aware of both.
package networkadapter

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/network"
	"github.com/zxh326/kite/pkg/cluster"
	"github.com/zxh326/kite/pkg/model"
	"github.com/zxh326/kite/pkg/rbac"
)

// historySource marks audit rows written by the network module.
const historySource = "network"

// Deps implements network.Deps on top of kite's cluster manager, RBAC roles,
// certificate center and ResourceHistory audit log.
type Deps struct {
	cm *cluster.ClusterManager
}

var _ network.Deps = (*Deps)(nil)

// New builds the adapter from the platform cluster manager.
func New(cm *cluster.ClusterManager) *Deps {
	return &Deps{cm: cm}
}

// Migrate creates or updates the network module's tables on db.
func Migrate(db *gorm.DB) error {
	return network.Migrate(db)
}

func (d *Deps) DB() *gorm.DB {
	return model.DB
}

func (d *Deps) ResolveCluster(_ context.Context, name string) (client.Client, error) {
	cs, err := d.cm.GetClientSet(name)
	if err != nil {
		return nil, fmt.Errorf("resolve cluster %q: %w", name, err)
	}
	return cs.K8sClient, nil
}

func (d *Deps) Authorize(c *gin.Context, resource, verb, cluster, namespace string) bool {
	user := c.MustGet("user").(model.User)
	return rbac.CanAccess(user, resource, verb, cluster, namespace)
}

// CertificateData resolves the named platform certificate to its TLS
// material. The returned key is decrypted in-memory only: it must never be
// logged or persisted outside the upserted kubernetes.io/tls secret.
func (d *Deps) CertificateData(name string) (string, string, string, error) {
	cert, err := model.GetCertificateByName(name)
	if err != nil {
		return "", "", "", fmt.Errorf("load certificate %q: %w", name, err)
	}
	if cert.CertPEM == "" || cert.KeyPEM == "" || cert.ContentHash == "" {
		return "", "", "", fmt.Errorf("certificate %q has incomplete material", name)
	}
	return cert.CertPEM, string(cert.KeyPEM), cert.ContentHash, nil
}

// CertificateNotAfter resolves only the certificate's expiry for display; the
// private key column is not selected, so the material is never decrypted.
func (d *Deps) CertificateNotAfter(name string) (time.Time, error) {
	var cert model.Certificate
	if err := model.DB.Model(&model.Certificate{}).
		Where("name = ?", name).
		Select("not_after").
		First(&cert).Error; err != nil {
		return time.Time{}, fmt.Errorf("load certificate %q: %w", name, err)
	}
	return cert.NotAfter, nil
}

// CertificateDomains parses the certificate chain's leaf and returns its
// subject common name and DNS SANs, deduplicated in order, at most five.
// Only the PEM column is selected: the private key is neither loaded nor
// logged.
func (d *Deps) CertificateDomains(name string) ([]string, error) {
	var cert model.Certificate
	if err := model.DB.Model(&model.Certificate{}).
		Where("name = ?", name).
		Select("cert_pem").
		First(&cert).Error; err != nil {
		return nil, fmt.Errorf("load certificate %q: %w", name, err)
	}
	leaf, err := parseLeafCertificate([]byte(cert.CertPEM))
	if err != nil {
		return nil, fmt.Errorf("parse certificate %q: %w", name, err)
	}
	var out []string
	seen := map[string]bool{}
	for _, domain := range append([]string{leaf.Subject.CommonName}, leaf.DNSNames...) {
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, domain)
		if len(out) == maxCertificateDomains {
			break
		}
	}
	return out, nil
}

// maxCertificateDomains caps the display domain list.
const maxCertificateDomains = 5

// parseLeafCertificate decodes the first certificate of the PEM chain (the
// chain is stored normalized, leaf first).
func parseLeafCertificate(pemBytes []byte) (*x509.Certificate, error) {
	for len(pemBytes) > 0 {
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		return x509.ParseCertificate(block.Bytes)
	}
	return nil, errors.New("no certificate in PEM chain")
}

func (d *Deps) RecordHistory(entry network.HistoryEntry) {
	if err := model.DB.Create(&model.ResourceHistory{
		ClusterName:     entry.Cluster,
		ResourceType:    entry.ResourceType,
		ResourceName:    entry.ResourceName,
		Namespace:       entry.Namespace,
		OperationType:   entry.Operation,
		OperationSource: historySource,
		ResourceYAML:    entry.YAML,
		PreviousYAML:    entry.PreviousYAML,
		Success:         entry.Success,
		ErrorMessage:    entry.Err,
		OperatorID:      entry.OperatorID,
	}).Error; err != nil {
		// Never log the entry payload: ingress YAML may carry annotations a
		// host admin considers sensitive.
		klog.Errorf("Failed to record network history: %v", err)
	}
}
