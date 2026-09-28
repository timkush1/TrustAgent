package authz

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// HKDF info strings of the master key's subkeys (SPEC §7.5).
const (
	InfoSession     = "trustagent/session/v1"
	InfoCredentials = "trustagent/credentials/v1"
)

// MasterKeySize is the master key length in bytes.
const MasterKeySize = 32

// Master key sources.
const (
	MasterFromEnv       = "env"
	MasterFromFile      = "file"
	MasterGenerated     = "generated"
	MasterEphemeralOnly = "ephemeral"
)

// MasterKey is the instance master key (SPEC §7.5). The P4 credential store derives
// InfoCredentials from the same key; sessions derive InfoSession.
type MasterKey struct {
	key []byte
	// ID is the first 16 hex characters of sha256(master) (credentials.key_id).
	ID string
	// Source is env, file, generated or ephemeral.
	Source string
	// Path is the key file (file and generated sources).
	Path string
}

func newMasterKey(key []byte, source, path string) *MasterKey {
	sum := sha256.Sum256(key)
	return &MasterKey{key: key, ID: hex.EncodeToString(sum[:])[:16], Source: source, Path: path}
}

// Derive returns the 32-byte HKDF-SHA256 subkey for info.
func (m *MasterKey) Derive(info string) []byte {
	k, err := hkdf.Key(sha256.New, m.key, nil, info, 32)
	if err != nil { // only for impossible lengths
		panic(err)
	}
	return k
}

// EphemeralMasterKey is a random key that lives as long as the process: sessions signed with it
// end at the next restart.
func EphemeralMasterKey() *MasterKey {
	return newMasterKey(randomBytes(MasterKeySize), MasterEphemeralOnly, "")
}

// LoadMasterKey returns the master key: TRUTHTABLE_SECRET_KEY (standard base64 of 32 bytes) when
// set; otherwise the key in path (TRUTHTABLE_SECRET_KEY_FILE, the same encoding); otherwise a new
// key generated into path (file mode 0600, directory 0700). A file that is readable by group or
// others is refused.
func LoadMasterKey(envValue, path string) (*MasterKey, error) {
	if envValue = strings.TrimSpace(envValue); envValue != "" {
		key, err := decodeMasterKey(envValue)
		if err != nil {
			return nil, fmt.Errorf("TRUTHTABLE_SECRET_KEY: %w", err)
		}
		return newMasterKey(key, MasterFromEnv, ""), nil
	}
	if path == "" {
		return nil, errors.New("no TRUTHTABLE_SECRET_KEY and no TRUTHTABLE_SECRET_KEY_FILE")
	}
	key, err := readMasterKeyFile(path)
	if err == nil {
		return newMasterKey(key, MasterFromFile, path), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key = randomBytes(MasterKeySize)
	if err := writeMasterKeyFile(path, key); err != nil {
		if errors.Is(err, fs.ErrExist) { // another process generated it first
			key, rerr := readMasterKeyFile(path)
			if rerr != nil {
				return nil, rerr
			}
			return newMasterKey(key, MasterFromFile, path), nil
		}
		return nil, err
	}
	return newMasterKey(key, MasterGenerated, path), nil
}

func decodeMasterKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(key) != MasterKeySize {
		return nil, fmt.Errorf("want %d bytes in standard base64 (openssl rand -base64 32)", MasterKeySize)
	}
	return key, nil
}

func readMasterKeyFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("master key file %s is readable by other users (mode %04o); chmod 600 it", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the operator names the key file (TRUTHTABLE_SECRET_KEY_FILE)
	if err != nil {
		return nil, err
	}
	key, err := decodeMasterKey(string(b))
	if err != nil {
		return nil, fmt.Errorf("master key file %s: %w", path, err)
	}
	return key, nil
}

// writeMasterKeyFile creates path with mode 0600 in a 0700 directory, failing with fs.ErrExist
// when it already exists. The key is written to a temporary file first and published with a hard
// link, so another process (a second replica on the same volume) never reads a half-written key,
// and a crash never leaves an empty key file behind. Filesystems without hard links fall back to
// an exclusive create.
func writeMasterKeyFile(path string, key []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("master key directory: %w", err)
	}
	data := base64.StdEncoding.EncodeToString(key) + "\n"
	tmp, err := os.CreateTemp(dir, ".secret-key-*") // mode 0600
	if err != nil {
		return writeMasterKeyExclusive(path, data)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // best-effort cleanup; the link (if any) keeps the data
	if _, err := tmp.WriteString(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return err
		}
		return writeMasterKeyExclusive(path, data)
	}
	return nil
}

// writeMasterKeyExclusive creates path exclusively with mode 0600 and writes data.
func writeMasterKeyExclusive(path, data string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- see readMasterKeyFile
	if err != nil {
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
