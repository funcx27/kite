package model

import (
	"time"
)

// Certificate is a platform-managed TLS certificate: the normalized PEM chain
// plus the encrypted private key, consumed by the application module's
// publish pipeline to upsert kubernetes.io/tls secrets. KeyPEM is a
// SecretString and must never leave the process in a response or log.
type Certificate struct {
	Model
	// Name is the user-facing reference stored in ingress annotations; a
	// DNS-1123 label of at most 63 chars (validated on create, immutable
	// afterwards because references are not tracked in v1).
	Name string `json:"name" gorm:"column:name;type:varchar(63);uniqueIndex;not null"`
	// Description is a free-form note; empty is allowed.
	Description string `json:"description" gorm:"column:description;type:varchar(255);not null;default:''"`
	// CertPEM is the normalized chain, leaf first.
	CertPEM string `json:"certPem" gorm:"column:cert_pem;type:text;not null"`
	// KeyPEM is the normalized PKCS#8 private key, encrypted at rest.
	KeyPEM SecretString `json:"-" gorm:"column:key_pem;type:text;not null"`
	// CommonName / DNSNames / Issuer / NotBefore / NotAfter are extracted from
	// the leaf certificate; Issuer is the issuer's common name.
	CommonName string    `json:"commonName" gorm:"column:common_name;type:varchar(255);not null;default:''"`
	DNSNames   []string  `json:"dnsNames" gorm:"column:dns_names;serializer:json;type:text"`
	Issuer     string    `json:"issuer" gorm:"column:issuer;type:varchar(255);not null;default:''"`
	NotBefore  time.Time `json:"notBefore" gorm:"column:not_before;not null"`
	NotAfter   time.Time `json:"notAfter" gorm:"column:not_after;not null"`
	// Fingerprint is the SHA-256 hex of the leaf DER — the display identity.
	Fingerprint string `json:"fingerprint" gorm:"column:fingerprint;type:varchar(64);not null;default:''"`
	// ContentHash is the SHA-256 hex of the normalized certPem + keyPem pair —
	// the rotation marker the publish pipeline stamps onto managed secrets.
	ContentHash string `json:"-" gorm:"column:content_hash;type:varchar(64);not null;default:''"`
}

func (Certificate) TableName() string {
	return "certificates"
}

// GetCertificateByName loads the full row, KeyPEM included (decrypted on scan).
func GetCertificateByName(name string) (*Certificate, error) {
	var cert Certificate
	if err := DB.Where("name = ?", name).First(&cert).Error; err != nil {
		return nil, err
	}
	return &cert, nil
}

// GetCertificateContentHash loads only the content_hash column so rotation
// checks never touch the encrypted key material.
func GetCertificateContentHash(name string) (string, error) {
	var cert Certificate
	err := DB.Where("name = ?", name).Select("content_hash").First(&cert).Error
	if err != nil {
		return "", err
	}
	return cert.ContentHash, nil
}
