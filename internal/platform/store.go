package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func atomicFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

type encryptedStore struct {
	path string
	aead cipher.AEAD
}

func newStore(dir string) (*encryptedStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "master.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := os.Stat(filepath.Join(dir, "platform.enc")); e == nil {
			return nil, errors.New("加密数据存在但主密钥缺失；请恢复密钥，不能重建")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		if err = atomicFile(keyPath, key); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &encryptedStore{filepath.Join(dir, "platform.enc"), aead}, nil
}
func (s *encryptedStore) load() (DiskState, error) {
	var out DiskState
	err := s.loadInto(&out)
	if err == nil && out.Schema != 1 {
		err = errors.New("平台状态版本不受支持")
	}
	return out, err
}
func (s *encryptedStore) loadInto(out any) error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	n := s.aead.NonceSize()
	if len(b) < n {
		return errors.New("加密状态已损坏")
	}
	p, err := s.aead.Open(nil, b[:n], b[n:], []byte("kubepilot-platform-v1"))
	if err != nil {
		return errors.New("平台状态无法解密")
	}
	return json.Unmarshal(p, out)
}
func (s *encryptedStore) save(state any) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	return atomicFile(s.path, s.aead.Seal(nonce, nonce, b, []byte("kubepilot-platform-v1")))
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
