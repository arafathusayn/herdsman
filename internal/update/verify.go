// Package update installs a new herdsman release. Nothing is replaced until
// the release's checksum list carries a signature from a key this binary
// trusts and the downloaded archive matches that list.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The ways a release is refused.
var (
	ErrBadSignature = errors.New("the release signature does not verify")
	ErrNoKeys       = errors.New("this build carries no release key")
	ErrBadChecksum  = errors.New("the archive does not match the signed checksums")
	ErrDowngrade    = errors.New("that release is older than this binary")
	ErrManaged      = errors.New("this binary belongs to a package manager")
)

const (
	scheme = "ed25519"
	// purpose is signed with the data, so that a signature made with the
	// release key for anything else can never pass as a release signature.
	purpose = "herdsman release checksums v1\n"
	// binaryName is the one archive entry that gets installed.
	binaryName = "herdsman"
)

//go:embed release_keys.txt
var releaseKeys string

// Key is a public key the binary trusts for release signatures.
type Key struct {
	ID  string
	Pub ed25519.PublicKey
}

// TrustedKeys returns the release keys compiled into this binary. More than
// one is normal while a key is being replaced.
func TrustedKeys() ([]Key, error) { return ParseKeys(releaseKeys) }

func keyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PublicKeyLine is the form a public key takes in the trusted key file.
func PublicKeyLine(pub ed25519.PublicKey) string {
	return scheme + ":" + base64.StdEncoding.EncodeToString(pub)
}

// ParseKeys reads one key per line; blank lines and # comments are skipped.
func ParseKeys(text string) ([]Key, error) {
	var keys []Key
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		encoded, ok := strings.CutPrefix(line, scheme+":")
		if !ok {
			return nil, fmt.Errorf("release key %q does not name the %s scheme", line, scheme)
		}
		pub, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("release key %q is not a %s public key", line, scheme)
		}
		keys = append(keys, Key{ID: keyID(pub), Pub: pub})
	}
	return keys, nil
}

func encodeSig(sig []byte) string { return base64.StdEncoding.EncodeToString(sig) }

func signedMessage(data []byte) []byte { return append([]byte(purpose), data...) }

// Sign returns the signature line for data: the scheme, the key's id and the
// signature, so that a stronger scheme can later be added beside this one.
func Sign(priv ed25519.PrivateKey, data []byte) string {
	sig := ed25519.Sign(priv, signedMessage(data))
	return fmt.Sprintf("%s %s %s\n", scheme, keyID(priv.Public().(ed25519.PublicKey)), encodeSig(sig))
}

// VerifySignature checks a signature line against the trusted keys.
func VerifySignature(keys []Key, data, line []byte) error {
	if len(keys) == 0 {
		return ErrNoKeys
	}
	fields := strings.Fields(string(line))
	if len(fields) != 3 {
		return fmt.Errorf("%w: the signature line is malformed", ErrBadSignature)
	}
	if fields[0] != scheme {
		return fmt.Errorf("%w: unknown scheme %q", ErrBadSignature, fields[0])
	}
	sig, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: the signature is malformed", ErrBadSignature)
	}
	for _, k := range keys {
		if k.ID == fields[1] {
			if !ed25519.Verify(k.Pub, signedMessage(data), sig) {
				return ErrBadSignature
			}
			return nil
		}
	}
	return fmt.Errorf("%w: key %s is not trusted", ErrBadSignature, fields[1])
}

// ParseChecksums reads goreleaser's checksums.txt into name -> lowercase hex.
func ParseChecksums(data []byte) (map[string]string, error) {
	sums := map[string]string{}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("checksum line %q is malformed", strings.TrimSpace(line))
		}
		sum, name := fields[0], fields[1]
		if raw, err := hex.DecodeString(sum); err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("checksum of %s is not sha256", name)
		}
		if _, dup := sums[name]; dup {
			return nil, fmt.Errorf("checksum of %s is listed twice", name)
		}
		sums[name] = strings.ToLower(sum)
	}
	return sums, nil
}

// VerifyArchive checks data against the signed checksum of name.
func VerifyArchive(sums map[string]string, name string, data []byte) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%w: %s is not listed", ErrBadChecksum, name)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%w: %s", ErrBadChecksum, name)
	}
	return nil
}

// ExtractBinary returns the regular file named herdsman at the top of a
// .tar.gz, reading at most limit bytes of it.
func ExtractBinary(archive []byte, limit int64) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive holds no %s binary", binaryName)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if h.Name != binaryName || h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > limit {
			return nil, fmt.Errorf("the %s binary is larger than %d bytes", binaryName, limit)
		}
		data, err := io.ReadAll(io.LimitReader(tr, limit))
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		return data, nil
	}
}
