// Package certificate parses, validates and stores the platform's TLS
// certificates. Parse failures are user-facing: every error explains what was
// wrong with the submitted PEM material.
package certificate

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"time"
)

// Bundle is a parsed certificate chain plus (when submitted) its matching
// private key, in the canonical forms the platform stores: the chain is
// re-encoded block by block in its original order, leaf first; the key is
// re-encoded as an unencrypted PKCS#8 "PRIVATE KEY" block regardless of the
// input encoding.
type Bundle struct {
	CertPEM string
	KeyPEM  string
	// CommonName / DNSNames / Issuer / NotBefore / NotAfter describe the leaf.
	CommonName string
	DNSNames   []string
	// Issuer is the issuer certificate's common name.
	Issuer    string
	NotBefore time.Time
	NotAfter  time.Time
	// Fingerprint is the SHA-256 hex of the leaf DER.
	Fingerprint string
	// ContentHash is the SHA-256 hex of the normalized certPem and keyPem
	// pair; empty until a key has been validated against the chain.
	ContentHash string
}

// ParseBundle parses a leaf-first certificate chain and its private key,
// verifies the key matches the leaf's public key and returns everything
// normalized. Accepted key encodings: PKCS#1 RSA, SEC1 EC, PKCS#8 (RSA, EC
// and Ed25519).
func ParseBundle(certPEM, keyPEM string) (*Bundle, error) {
	chain, normCert, err := parseChain(certPEM)
	if err != nil {
		return nil, err
	}
	leaf := chain[0]
	key, normKey, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	if err := matchKey(leaf, key); err != nil {
		return nil, err
	}
	return bundleOf(leaf, normCert, normKey), nil
}

// ParseCertificate parses the chain only; KeyPEM and ContentHash stay empty.
func ParseCertificate(certPEM string) (*Bundle, error) {
	chain, normCert, err := parseChain(certPEM)
	if err != nil {
		return nil, err
	}
	return bundleOf(chain[0], normCert, ""), nil
}

// MatchKey validates that keyPEM belongs to the given certificate chain and
// returns the normalized key together with the pair's content hash. Used when
// only the key of an unchanged certificate is replaced.
func MatchKey(certPEM, keyPEM string) (string, string, error) {
	chain, _, err := parseChain(certPEM)
	if err != nil {
		return "", "", err
	}
	key, normKey, err := parseKey(keyPEM)
	if err != nil {
		return "", "", err
	}
	if err := matchKey(chain[0], key); err != nil {
		return "", "", err
	}
	return normKey, ContentHash(certPEM, normKey), nil
}

// ContentHash is the SHA-256 hex of the normalized pair joined by a newline —
// the marker the publish pipeline compares against managed TLS secrets. Both
// inputs must already be normalized or the hash is not comparable.
func ContentHash(certPEM, keyPEM string) string {
	sum := sha256.Sum256([]byte(certPEM + "\n" + keyPEM))
	return hex.EncodeToString(sum[:])
}

func bundleOf(leaf *x509.Certificate, normCert, normKey string) *Bundle {
	b := &Bundle{
		CertPEM:     normCert,
		KeyPEM:      normKey,
		CommonName:  leaf.Subject.CommonName,
		DNSNames:    leaf.DNSNames,
		Issuer:      leaf.Issuer.CommonName,
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
		Fingerprint: fingerprintOf(leaf),
	}
	if normKey != "" {
		b.ContentHash = ContentHash(normCert, normKey)
	}
	return b
}

// parseChain decodes every CERTIFICATE block of the input in order and
// returns the parsed chain plus the re-encoded PEM (original order preserved,
// leaf first — callers document that requirement to users).
func parseChain(certPEM string) ([]*x509.Certificate, string, error) {
	rest := []byte(certPEM)
	var chain []*x509.Certificate
	var buf bytes.Buffer
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, "", fmt.Errorf("unexpected PEM block %q in certificate, want CERTIFICATE", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("parse certificate %d of the chain: %w", len(chain)+1, err)
		}
		chain = append(chain, cert)
		if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
			return nil, "", fmt.Errorf("encode certificate %d of the chain: %w", len(chain), err)
		}
	}
	if len(chain) == 0 {
		return nil, "", fmt.Errorf("certificate contains no CERTIFICATE PEM block")
	}
	return chain, buf.String(), nil
}

// parseKey decodes a private key PEM block (PKCS#1 RSA, SEC1 EC or PKCS#8)
// and returns it plus the PKCS#8 re-encoding the platform stores.
func parseKey(keyPEM string) (crypto.PrivateKey, string, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, "", fmt.Errorf("private key contains no PEM block")
	}
	var key crypto.PrivateKey
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, "", fmt.Errorf("unsupported private key PEM block %q, want RSA PRIVATE KEY, EC PRIVATE KEY or PRIVATE KEY", block.Type)
	}
	if err != nil {
		return nil, "", fmt.Errorf("parse private key: %w", err)
	}
	switch key.(type) {
	case *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey:
	default:
		return nil, "", fmt.Errorf("unsupported private key type %T", key)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", fmt.Errorf("normalize private key to PKCS#8: %w", err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		return nil, "", fmt.Errorf("encode private key: %w", err)
	}
	return key, buf.String(), nil
}

// matchKey reports whether the private key belongs to the leaf certificate by
// comparing both public keys' canonical PKIX encodings.
func matchKey(leaf *x509.Certificate, key crypto.PrivateKey) error {
	certPub, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return fmt.Errorf("encode certificate public key: %w", err)
	}
	keyPub, err := x509.MarshalPKIXPublicKey(publicKeyOf(key))
	if err != nil {
		return fmt.Errorf("encode private key's public key: %w", err)
	}
	if !bytes.Equal(certPub, keyPub) {
		return fmt.Errorf("private key does not match the certificate (leaf %q)", leaf.Subject.CommonName)
	}
	return nil
}

func fingerprintOf(leaf *x509.Certificate) string {
	sum := sha256.Sum256(leaf.Raw)
	return hex.EncodeToString(sum[:])
}

// publicKeyOf returns the public side of a parsed private key; nil when the
// key implements no crypto.Signer, which the caller's marshal then rejects.
func publicKeyOf(key crypto.PrivateKey) crypto.PublicKey {
	if signer, ok := key.(crypto.Signer); ok {
		return signer.Public()
	}
	return nil
}
