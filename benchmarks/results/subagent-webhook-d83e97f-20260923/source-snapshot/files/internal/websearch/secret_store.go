package websearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxStoredAPIKeyBytes = 8 << 10

type SecretStore struct{ Home string }

type webSecretFile struct {
	Version int    `json:"version"`
	APIKey  string `json:"api_key"`
}

var secretStoreLocks sync.Map

// ResolveAPIKey gives the explicit environment variable precedence over the
// pk-managed private store. It returns the source label separately so callers
// can show status without ever displaying the credential.
func ResolveAPIKey(home string) (key, source string, err error) {
	if value := strings.TrimSpace(os.Getenv("TINYFISH_API_KEY")); value != "" {
		return value, "environment", nil
	}
	value, err := (SecretStore{Home: home}).Load()
	if err != nil {
		return "", "", err
	}
	if value == "" {
		return "", "none", nil
	}
	return value, "local_store", nil
}

func (store SecretStore) Load() (string, error) {
	if strings.TrimSpace(store.Home) == "" {
		return "", errors.New("pk home directory is required")
	}
	path := filepath.Join(store.Home, "websearch.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("inspect TinyFish credential store")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("TinyFish credential store must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("TinyFish credential store permissions must be 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("open TinyFish credential store")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStoredAPIKeyBytes+1024))
	if err != nil || len(data) > maxStoredAPIKeyBytes+1024 {
		return "", errors.New("read TinyFish credential store")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var stored webSecretFile
	if err := decoder.Decode(&stored); err != nil || decoder.Decode(new(any)) != io.EOF || stored.Version != 1 {
		return "", errors.New("TinyFish credential store is invalid")
	}
	if stored.APIKey == "" {
		return "", nil
	}
	if err := validateAPIKey(stored.APIKey); err != nil {
		return "", errors.New("TinyFish credential store contains an invalid key")
	}
	return stored.APIKey, nil
}

func (store SecretStore) Save(key string) error {
	key = strings.TrimSpace(key)
	if err := validateAPIKey(key); err != nil {
		return err
	}
	return store.withLock(func() error {
		home, err := store.ensureHome()
		if err != nil {
			return err
		}
		path := filepath.Join(home, "websearch.json")
		if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("TinyFish credential store must be a regular file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect TinyFish credential store")
		}
		data, err := json.Marshal(webSecretFile{Version: 1, APIKey: key})
		if err != nil {
			return errors.New("encode TinyFish credential store")
		}
		tmp, err := os.CreateTemp(home, ".websearch-*.tmp")
		if err != nil {
			return errors.New("create TinyFish credential store")
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if err = tmp.Chmod(0o600); err == nil {
			_, err = tmp.Write(append(data, '\n'))
		}
		if err == nil {
			err = tmp.Sync()
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return errors.New("write TinyFish credential store")
		}
		if err := os.Rename(tmpPath, path); err != nil {
			return errors.New("replace TinyFish credential store")
		}
		directory, err := os.Open(home)
		if err != nil {
			return errors.New("sync TinyFish credential store")
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return errors.New("sync TinyFish credential store")
		}
		return nil
	})
}

func (store SecretStore) Clear() error {
	if strings.TrimSpace(store.Home) == "" {
		return errors.New("pk home directory is required")
	}
	return store.withLock(func() error {
		path := filepath.Join(store.Home, "websearch.json")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("TinyFish credential store must be a regular file")
		}
		if err := os.Remove(path); err != nil {
			return errors.New("remove TinyFish credential store")
		}
		return nil
	})
}

func validateAPIKey(key string) error {
	if key == "" || len(key) > maxStoredAPIKeyBytes {
		return fmt.Errorf("TinyFish API key must contain 1 to %d bytes", maxStoredAPIKeyBytes)
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return errors.New("TinyFish API key contains an invalid character")
		}
	}
	return nil
}

func (store SecretStore) withLock(action func() error) error {
	path, err := filepath.Abs(filepath.Join(store.Home, "websearch.lock"))
	if err != nil {
		return errors.New("resolve TinyFish credential store")
	}
	value, _ := secretStoreLocks.LoadOrStore(path, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	return action()
}

func (store SecretStore) ensureHome() (string, error) {
	if strings.TrimSpace(store.Home) == "" {
		return "", errors.New("pk home directory is required")
	}
	home, err := filepath.Abs(store.Home)
	if err != nil {
		return "", errors.New("resolve pk home directory")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", errors.New("create pk home directory")
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("pk home must be a real directory")
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return "", errors.New("secure pk home directory")
	}
	return home, nil
}
