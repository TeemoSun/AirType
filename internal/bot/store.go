package bot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/the-yex/wechat-ilink-sdk/login"

	"github.com/TeemoSun/AirType/internal/secret"
)

// dpapiTokenStore 实现 SDK 的 TokenStore：文件布局与 FileTokenStore
// 完全一致（{accountID}.json），但文件内容整体经 DPAPI 加密，
// 微信登录 token 不再明文落盘。旧版明文文件在 Load 时自动迁移。
type dpapiTokenStore struct {
	baseDir string
	mu      sync.Mutex
}

func newDPAPITokenStore(baseDir string) (*dpapiTokenStore, error) {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("bot: 创建数据目录失败: %w", err)
	}
	return &dpapiTokenStore{baseDir: baseDir}, nil
}

func (s *dpapiTokenStore) path(accountID string) string {
	return filepath.Join(s.baseDir, fmt.Sprintf("%s.json", accountID))
}

func (s *dpapiTokenStore) Save(accountID string, token *login.TokenInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(accountID, token)
}

// saveLocked 落盘（须已持锁；Load 的明文迁移路径在持锁状态下复用）。
func (s *dpapiTokenStore) saveLocked(accountID string, token *login.TokenInfo) error {
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	cipher, err := secret.Seal(append(data, '\n'))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.baseDir, accountID+".json.tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(cipher); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path(accountID))
}

func (s *dpapiTokenStore) Load(accountID string) (*login.TokenInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(accountID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 无 token（与 SDK FileTokenStore 语义一致）
		}
		return nil, fmt.Errorf("read token: %w", err)
	}
	plain, sealed, err := secret.Open(data)
	if err != nil {
		// 解不开（换用户/换机器/损坏）：视为无 token，走重新扫码
		return nil, nil
	}
	var token login.TokenInfo
	if err := json.Unmarshal(plain, &token); err != nil {
		return nil, nil // 损坏：走重新扫码
	}
	if !sealed {
		// 旧版明文：立即迁移为加密存储（saveLocked：本方法已持锁）
		if err := s.saveLocked(accountID, &token); err == nil {
			return &token, nil
		}
		// 迁移失败也继续用明文结果，下次再试
	}
	return &token, nil
}

func (s *dpapiTokenStore) Delete(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(accountID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove token: %w", err)
	}
	return nil
}

func (s *dpapiTokenStore) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var accounts []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			accounts = append(accounts, e.Name()[:len(e.Name())-len(".json")])
		}
	}
	return accounts, nil
}
