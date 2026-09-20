// Package signing applies Auto's stable Developer ID signature without using a keychain.
package signing

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/darrenoakey/daz-secrets/pkg/dazsecrets"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

const (
	certificateService = "app-publish"
	p12Account         = "developer_id_application_p12"
	passwordAccount    = "developer_id_application_p12_password"
	expectedTeamId     = "58MF5GATL7"
	expectedCommonName = "Developer ID Application: Inside Mind Pty Ltd (58MF5GATL7)"
	expectedBundleId   = "com.darrenoakey.auto"
	executableName     = "auto"
	rcodesignPath      = "/Users/darrenoakey/.cargo/bin/rcodesign"
	plutilPath         = "/usr/bin/plutil"
)

type secretReference struct {
	service string
	account string
}

type credentialReferences struct {
	p12      secretReference
	password secretReference
}

type credentials struct {
	p12      []byte
	password []byte
	pem      []byte
}

// SignBundle signs Auto.app with the one production Developer ID identity.
func SignBundle(context context.Context, bundlePath string) error {
	return signBundle(context, bundlePath, productionCredentialReferences())
}

// VerifyBundle proves Auto's main Mach-O has the expected stable identity.
func VerifyBundle(context context.Context, bundlePath string) error {
	return verify(context, bundlePath)
}

func productionCredentialReferences() credentialReferences {
	return credentialReferences{
		p12:      secretReference{service: certificateService, account: p12Account},
		password: secretReference{service: certificateService, account: passwordAccount},
	}
}

func signBundle(context context.Context, bundlePath string, references credentialReferences) error {
	loaded, err := loadCredentials(context, references)
	if err != nil {
		return err
	}
	defer loaded.clear()
	if loaded.pem, err = validateIdentity(loaded); err != nil {
		return err
	}
	if err = sign(context, bundlePath, loaded); err != nil {
		return err
	}
	return verify(context, bundlePath)
}

func loadCredentials(context context.Context, references credentialReferences) (*credentials, error) {
	client, err := dazsecrets.NewDefaultClient()
	if err != nil {
		return nil, fmt.Errorf("initialize signing secret provider: %w", err)
	}
	p12Secret, err := readRequired(context, client, references.p12)
	if err != nil {
		return nil, err
	}
	passwordSecret, err := readRequired(context, client, references.password)
	if err != nil {
		clear(p12Secret)
		return nil, err
	}
	return &credentials{p12: p12Secret, password: passwordSecret}, nil
}

func readRequired(context context.Context, client *dazsecrets.Client, reference secretReference) ([]byte, error) {
	secret, err := client.Get(context, reference.service, reference.account)
	if err != nil {
		return nil, fmt.Errorf("read signing secret %s/%s: %w", reference.service, reference.account, err)
	}
	if len(secret.Value) == 0 {
		return nil, fmt.Errorf("read signing secret %s/%s: empty value", reference.service, reference.account)
	}
	return secret.Value, nil
}

func validateIdentity(loaded *credentials) ([]byte, error) {
	privateKey, certificate, chain, err := pkcs12.DecodeChain(loaded.p12, string(loaded.password))
	if err != nil {
		return nil, fmt.Errorf("decode Developer ID identity: invalid PKCS#12 or password")
	}
	if err = validateCertificate(certificate); err != nil {
		return nil, err
	}
	if err = validateKeyPair(privateKey, certificate); err != nil {
		return nil, err
	}
	return encodeIdentity(privateKey, certificate, chain)
}

func validateCertificate(certificate *x509.Certificate) error {
	now := time.Now()
	if certificate.Subject.CommonName != expectedCommonName || !slices.Contains(certificate.Subject.OrganizationalUnit, expectedTeamId) {
		return fmt.Errorf("developer ID certificate does not belong to expected team %s", expectedTeamId)
	}
	if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
		return fmt.Errorf("developer ID certificate is not currently valid")
	}
	return nil
}

func validateKeyPair(privateKey crypto.PrivateKey, certificate *x509.Certificate) error {
	signer, ok := privateKey.(crypto.Signer)
	if !ok {
		return fmt.Errorf("developer ID private key does not match its certificate")
	}
	privatePublic, privateErr := x509.MarshalPKIXPublicKey(signer.Public())
	certificatePublic, certificateErr := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if privateErr != nil || certificateErr != nil || !bytes.Equal(privatePublic, certificatePublic) {
		return fmt.Errorf("developer ID private key does not match its certificate")
	}
	return nil
}

func encodeIdentity(privateKey crypto.PrivateKey, certificate *x509.Certificate, chain []*x509.Certificate) ([]byte, error) {
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode Developer ID private key")
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	clear(keyBytes)
	encoded = append(encoded, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})...)
	for _, authority := range chain {
		encoded = append(encoded, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authority.Raw})...)
	}
	return encoded, nil
}

func sign(context context.Context, bundlePath string, loaded *credentials) error {
	arguments := []string{"sign", "-C", "/dev/null", "--pem-file", "/dev/fd/3", "--team-name", expectedTeamId, "--code-signature-flags", "runtime", bundlePath}
	_, err := runProcess(context, rcodesignPath, arguments, loaded.pem)
	if err != nil {
		return fmt.Errorf("sign Auto.app: %w", err)
	}
	return nil
}

func verify(context context.Context, bundlePath string) error {
	if err := verifyBundleMetadata(context, bundlePath); err != nil {
		return err
	}
	executablePath := filepath.Join(bundlePath, "Contents", "MacOS", executableName)
	if _, err := runProcess(context, rcodesignPath, []string{"verify", "-C", "/dev/null", executablePath}); err != nil {
		return fmt.Errorf("verify Auto Mach-O signature: %w", err)
	}
	output, err := runProcess(context, rcodesignPath, []string{"print-signature-info", "-C", "/dev/null", executablePath})
	if err != nil {
		return fmt.Errorf("inspect Auto Mach-O signature: %w", err)
	}
	return verifySignatureInfo(output)
}

func verifyBundleMetadata(context context.Context, bundlePath string) error {
	infoPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	if err := verifyPlistValue(context, infoPath, "CFBundleIdentifier", expectedBundleId); err != nil {
		return err
	}
	return verifyPlistValue(context, infoPath, "CFBundleExecutable", executableName)
}

func verifyPlistValue(context context.Context, path, key, expected string) error {
	arguments := []string{"-extract", key, "raw", "-o", "-", path}
	output, err := runProcess(context, plutilPath, arguments)
	if err != nil {
		return fmt.Errorf("read Auto.app %s: %w", key, err)
	}
	if strings.TrimSpace(output) != expected {
		return fmt.Errorf("bundle metadata %s does not equal %s", key, expected)
	}
	return nil
}

func verifySignatureInfo(output string) error {
	if !signatureFieldMatches(output, "team_name", expectedTeamId) {
		return fmt.Errorf("signed Auto Mach-O does not report expected team %s", expectedTeamId)
	}
	if !signatureFieldMatches(output, "identifier", expectedBundleId) {
		return fmt.Errorf("signed Auto Mach-O does not preserve bundle identifier %s", expectedBundleId)
	}
	if !stableDesignatedRequirement(output) {
		return fmt.Errorf("signed Auto Mach-O lacks the stable Apple/team/bundle designated requirement")
	}
	if !strings.Contains(output, "apple_certificate_profile: developer-id-application") || !strings.Contains(output, "signature_verifies: true") {
		return fmt.Errorf("signed Auto Mach-O lacks a verified Developer ID Application signature")
	}
	return nil
}

func stableDesignatedRequirement(output string) bool {
	requirement := signatureLineContaining(output, "designated(3):")
	required := []string{
		`identifier "` + expectedBundleId + `"`,
		"anchor apple generic",
		"certificate 1[field.1.2.840.113635.100.6.2.6]",
		"certificate leaf[field.1.2.840.113635.100.6.1.13]",
		`certificate leaf[subject.OU] = "` + expectedTeamId + `"`,
	}
	for _, value := range required {
		if !strings.Contains(requirement, value) {
			return false
		}
	}
	return !strings.Contains(strings.ToLower(requirement), "cdhash")
}

func signatureLineContaining(output, value string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, value) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func signatureFieldMatches(output, field, expected string) bool {
	return signatureField(output, field) == field+": "+expected
}

func signatureField(output, field string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= len(field)+1 && line[:len(field)] == field {
			return line
		}
	}
	return ""
}

func (loaded *credentials) clear() {
	clear(loaded.p12)
	clear(loaded.password)
	clear(loaded.pem)
}
