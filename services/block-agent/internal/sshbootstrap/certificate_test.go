package sshbootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSigner(t *testing.T) SSHSigner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca")
	command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate CA: %v: %s", err, output)
	}
	signer, err := LoadSSHSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func newUserKey(t *testing.T) ED25519AuthorizedKey {
	t.Helper()
	path := filepath.Join(t.TempDir(), "user")
	command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "test@example", "-f", path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate user key: %v: %s", err, output)
	}
	value, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ParseED25519AuthorizedKey(strings.TrimSpace(string(value)))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCertificateIssuerExactTTLAndPrincipal(t *testing.T) {
	issuer, err := NewCertificateIssuer(newSigner(t))
	if err != nil {
		t.Fatal(err)
	}
	userKey := newUserKey(t)
	validAfter := time.Date(2026, 7, 30, 8, 0, 0, 987654321, time.UTC)
	issued, err := issuer.Issue(userKey, "debug", "018f0000-0000-4000-8000-000000000001", validAfter)
	if err != nil {
		t.Fatal(err)
	}

	if issued.ValidAfter.Nanosecond() != 0 || issued.ValidBefore.Sub(issued.ValidAfter) != CertificateTTL {
		t.Fatalf("reported validity = %s..%s", issued.ValidAfter, issued.ValidBefore)
	}

	certificatePath := filepath.Join(t.TempDir(), "issued-cert.pub")
	if err := os.WriteFile(certificatePath, []byte(issued.AuthorizedKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("ssh-keygen", "-L", "-f", certificatePath).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect certificate: %v: %s", err, output)
	}
	inspection := string(output)
	for _, expected := range []string{
		"Type: ssh-ed25519-cert-v01@openssh.com user certificate",
		"Key ID: \"018f0000-0000-4000-8000-000000000001\"",
		"Principals:",
		"debug",
	} {
		if !strings.Contains(inspection, expected) {
			t.Fatalf("certificate inspection omitted %q:\n%s", expected, inspection)
		}
	}
}

func TestCertificateIssuerRejectsRootAndNonED25519(t *testing.T) {
	issuer, err := NewCertificateIssuer(newSigner(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Issue(newUserKey(t), "root", "request-id", time.Now()); err == nil {
		t.Fatal("root principal unexpectedly accepted")
	}
}

func TestED25519AuthorizedKeyAllowsContractComment(t *testing.T) {
	key := newUserKey(t)
	fields := strings.Fields(key.Line)
	value := fields[0] + " " + fields[1] + " release operator"
	parsed, err := ParseED25519AuthorizedKey(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Line != value {
		t.Fatalf("parsed line = %q", parsed.Line)
	}
}
