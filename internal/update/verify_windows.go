package update

import (
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os/exec"
	"strings"
)

//go:embed foxcloud-codesign.cer
var signingCert []byte

// VerifySignatures checks that every file carries an Authenticode signature
// from the project's pinned certificate and has not been altered since. The
// certificate is self-signed, so Windows reports it as untrusted; what is
// checked is the signer's thumbprint and that the file still matches it.
func VerifySignatures(files map[string]string) error {
	thumb := thumbprint(signingCert)
	const script = `$s = Get-AuthenticodeSignature -LiteralPath $args[0]; ` +
		`if ($s.SignerCertificate) { $s.SignerCertificate.Thumbprint } else { 'none' }; $s.Status`
	for name, path := range files {
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script, path).Output()
		if err != nil {
			return fmt.Errorf("checking the signature of %s: %w", name, err)
		}
		fields := strings.Fields(string(out))
		if len(fields) < 2 {
			return fmt.Errorf("checking the signature of %s: unexpected output %q", name, out)
		}
		got, status := strings.ToUpper(fields[0]), fields[1]
		switch {
		case got != thumb:
			return fmt.Errorf("%s is not signed with the FoxCloud certificate (signer %s)", name, got)
		case status == "HashMismatch" || status == "NotSigned":
			return fmt.Errorf("%s has an invalid signature (%s)", name, status)
		}
	}
	return nil
}

// thumbprint is the SHA-1 of the DER certificate, as Windows displays it.
func thumbprint(cert []byte) string {
	if b, _ := pem.Decode(cert); b != nil {
		cert = b.Bytes
	}
	sum := sha1.Sum(cert)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
