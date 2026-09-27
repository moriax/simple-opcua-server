package opcuaserver

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	certBits     = 2048 // Basic256Sha256 requires an RSA key of 2048 bits or more.
	certLifetime = 10 * 365 * 24 * time.Hour
)

// KeyPair is the server's certificate and private key, plus where they live.
type KeyPair struct {
	CertDER  []byte // the certificate in DER form, as OPC UA transmits it
	Key      *rsa.PrivateKey
	Cert     *x509.Certificate
	CertPath string
	KeyPath  string
	DERPath  string // a DER copy, written when the pair is generated
	Created  bool   // true if this run generated the pair
}

// NonConformant reports why the certificate is not a usable OPC UA application
// instance certificate, or an empty string when it is fine.
func (k *KeyPair) NonConformant() string {
	switch {
	case k.Cert == nil:
		return ""
	case k.Cert.IsCA:
		return "it is marked as a certificate authority (CA:TRUE); clients file such a certificate as an authority rather than as this server's own, and then reject it for having no revocation list"
	case len(k.Cert.URIs) == 0:
		return "it carries no URI subject alternative name, so no client can match it against this server's ApplicationUri"
	}
	return ""
}

// Thumbprint is the SHA-1 hash of the certificate in uppercase hex. OPC UA
// clients name the file they store a certificate in after this, so it is how
// you find this server's certificate in a client's rejected or trusted folder.
func (k *KeyPair) Thumbprint() string {
	sum := sha1.Sum(k.CertDER)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// Describe summarises the certificate for the startup output.
func (k *KeyPair) Describe() []string {
	lines := []string{"thumbprint (SHA-1) " + k.Thumbprint()}
	if k.Cert == nil {
		return lines
	}
	lines = append(lines, fmt.Sprintf("subject             %s", k.Cert.Subject))
	if len(k.Cert.URIs) > 0 {
		lines = append(lines, fmt.Sprintf("application URI     %s", k.Cert.URIs[0]))
	}
	var sans []string
	sans = append(sans, k.Cert.DNSNames...)
	for _, ip := range k.Cert.IPAddresses {
		sans = append(sans, ip.String())
	}
	if len(sans) > 0 {
		lines = append(lines, fmt.Sprintf("valid for           %s", strings.Join(sans, ", ")))
	}
	lines = append(lines, fmt.Sprintf("expires             %s", k.Cert.NotAfter.UTC().Format(time.RFC3339)))
	return lines
}

// LoadOrCreateKeyPair returns the certificate and key at certPath/keyPath,
// generating a self-signed pair if either file is missing. appURI must be the
// server's ApplicationUri: OPC UA requires it to appear as a URI subject
// alternative name, and clients reject the endpoint when the two disagree.
func LoadOrCreateKeyPair(certPath, keyPath, appURI, commonName string, hosts []string) (*KeyPair, error) {
	if fileExists(certPath) && fileExists(keyPath) {
		kp, err := loadKeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("cannot load the existing certificate: %w", err)
		}
		return kp, nil
	}

	certPEM, keyPEM, certDER, err := generateSelfSigned(appURI, commonName, hosts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the certificate directory: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", certPath, err)
	}
	// The private key must not be world readable.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", keyPath, err)
	}

	// A DER copy, which is the form a client's trust store wants.
	derPath := strings.TrimSuffix(certPath, filepath.Ext(certPath)) + ".der"
	if err := os.WriteFile(derPath, certDER, 0o644); err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", derPath, err)
	}

	kp, err := loadKeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	kp.Created = true
	kp.DERPath = derPath
	return kp, nil
}

func loadKeyPair(certPath, keyPath string) (*KeyPair, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	key, ok := pair.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s holds a %T, but OPC UA security needs an RSA key", keyPath, pair.PrivateKey)
	}
	if n := key.N.BitLen(); n < certBits {
		return nil, fmt.Errorf("%s holds a %d bit key, but Basic256Sha256 needs at least %d", keyPath, n, certBits)
	}
	if len(pair.Certificate) == 0 {
		return nil, fmt.Errorf("%s contains no certificate", certPath)
	}
	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable certificate: %w", certPath, err)
	}
	return &KeyPair{
		CertDER:  pair.Certificate[0],
		Key:      key,
		Cert:     parsed,
		CertPath: certPath,
		KeyPath:  keyPath,
	}, nil
}

// generateSelfSigned builds a certificate usable as an OPC UA application
// instance certificate: the ApplicationUri as a URI SAN, every name and address
// the server answers on as DNS and IP SANs, and the key usages the handshake
// needs for Sign and SignAndEncrypt.
func generateSelfSigned(appURI, commonName string, hosts []string) (certPEM, keyPEM, certDER []byte, err error) {
	uri, err := url.Parse(appURI)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("application URI %q is not a valid URI: %w", appURI, err)
	}

	key, err := rsa.GenerateKey(rand.Reader, certBits)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cannot generate a private key: %w", err)
	}

	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cannot generate a serial number: %w", err)
	}

	// RFC 5280 method 1: the SHA-1 of the public key bit string. Setting it
	// explicitly also gives the certificate an Authority Key Identifier, which
	// clients use to build and check the chain.
	ski := sha1.Sum(x509.MarshalPKCS1PublicKey(&key.PublicKey))

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"simpleopcuaserver"},
		},
		// A little slack before now so a client with a skewed clock still
		// accepts a certificate generated seconds ago.
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(certLifetime),
		KeyUsage: x509.KeyUsageDigitalSignature |
			x509.KeyUsageContentCommitment |
			x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDataEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		// An OPC UA application instance certificate is an end entity
		// certificate, so CA:FALSE and no keyCertSign. A client that is handed
		// a CA:TRUE certificate to trust files it as a certificate authority
		// instead of as this application's own certificate, and then rejects it
		// for having no revocation list.
		BasicConstraintsValid: true,
		IsCA:                  false,
		SubjectKeyId:          ski[:],
		// Go omits the authority key identifier on a self-signed certificate,
		// where it equals the subject's. Setting it keeps the extension present
		// for clients that expect one when building a chain.
		AuthorityKeyId: ski[:],
		URIs:           []*url.URL{uri},
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cannot create the certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, der, nil
}

// certHosts is every name and address to put in the certificate so that a
// client checking the host it dialled against the SANs is satisfied.
func certHosts(advertised []string, hostname string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if hostname != "" {
		hosts = append(hosts, hostname)
	}
	for _, h := range advertised {
		if h != "" {
			hosts = append(hosts, h)
		}
	}

	seen := map[string]bool{}
	out := hosts[:0]
	for _, h := range hosts {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
