package opcuaserver

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateKeyPairGeneratesAndReuses(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	const uri = "urn:testhost:simpleopcuaserver"

	first, err := LoadOrCreateKeyPair(cert, key, uri, "SimpleOPCUAServer@testhost", []string{"localhost", "127.0.0.1", "testhost"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created {
		t.Error("the first call should have generated a pair")
	}

	// OPC UA requires the ApplicationUri as a URI SAN; clients reject the
	// endpoint when it disagrees with the ApplicationUri they are told.
	if len(first.Cert.URIs) != 1 || first.Cert.URIs[0].String() != uri {
		t.Errorf("certificate URIs = %v, want just %s", first.Cert.URIs, uri)
	}
	// An application instance certificate is an end entity certificate. A
	// CA:TRUE one is filed by a client as an authority rather than as this
	// server's own certificate, and then rejected for having no CRL.
	if first.Cert.IsCA {
		t.Error("an application instance certificate must not be marked CA:TRUE")
	}
	if first.Cert.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Error("an application instance certificate must not carry keyCertSign")
	}
	for _, want := range []x509.KeyUsage{
		x509.KeyUsageDigitalSignature, x509.KeyUsageContentCommitment,
		x509.KeyUsageKeyEncipherment, x509.KeyUsageDataEncipherment,
	} {
		if first.Cert.KeyUsage&want == 0 {
			t.Errorf("certificate is missing key usage %b", want)
		}
	}
	if len(first.Cert.SubjectKeyId) == 0 || len(first.Cert.AuthorityKeyId) == 0 {
		t.Error("certificate needs both a subject and an authority key identifier")
	}
	if first.NonConformant() != "" {
		t.Errorf("a freshly generated certificate reports itself non-conformant: %s", first.NonConformant())
	}
	if !fileExists(first.DERPath) {
		t.Errorf("no DER copy was written at %s", first.DERPath)
	}
	if first.Key.N.BitLen() < 2048 {
		t.Errorf("key is %d bits, Basic256Sha256 needs 2048", first.Key.N.BitLen())
	}

	wantDNS := map[string]bool{"localhost": true, "testhost": true}
	for _, n := range first.Cert.DNSNames {
		delete(wantDNS, n)
	}
	if len(wantDNS) != 0 {
		t.Errorf("certificate is missing DNS names %v", wantDNS)
	}
	var hasLoopback bool
	for _, ip := range first.Cert.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			hasLoopback = true
		}
	}
	if !hasLoopback {
		t.Error("certificate is missing the 127.0.0.1 address")
	}

	second, err := LoadOrCreateKeyPair(cert, key, uri, "SimpleOPCUAServer@testhost", []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created {
		t.Error("the second call should have reused the pair, not made a new one")
	}
	if second.Thumbprint() != first.Thumbprint() {
		t.Error("reusing the pair produced a different certificate")
	}
}

// Clients name the file they store a certificate in after its SHA-1 hash, so
// the thumbprint printed at startup has to be exactly that.
func TestThumbprintIsSHA1OfTheDER(t *testing.T) {
	dir := t.TempDir()
	kp, err := LoadOrCreateKeyPair(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"),
		"urn:testhost:simpleopcuaserver", "SimpleOPCUAServer@testhost", []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(kp.CertDER)
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	if got := kp.Thumbprint(); got != want {
		t.Errorf("Thumbprint() = %s, want %s", got, want)
	}
	if len(kp.Thumbprint()) != 40 {
		t.Errorf("thumbprint %q is not 40 hex characters", kp.Thumbprint())
	}
}

func TestCertHostsDeduplicates(t *testing.T) {
	got := certHosts([]string{"127.0.0.1", "localhost", "myhost"}, "myhost")
	seen := map[string]int{}
	for _, h := range got {
		seen[h]++
	}
	for h, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times in %v", h, n, got)
		}
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", "myhost"} {
		if seen[want] == 0 {
			t.Errorf("%s is missing from %v", want, got)
		}
	}
}
