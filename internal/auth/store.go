// Package auth는 로그인, 토큰 보관, 요청에 토큰을 붙이고 만료되면 갱신하는 일을 맡는다.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/laight-ai/laighthouse-cli/internal/config"
)

// Tokens는 한 환경에 로그인해 받은 토큰 쌍이다.
// ClientID가 비어 있으면 비밀번호 로그인으로 쿠키에서 받은 토큰이고, 있으면 그 OAuth 클라이언트로
// 브라우저 로그인해 받은 토큰이다. 갱신과 로그아웃 경로가 이 값에 따라 갈린다.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id,omitempty"`
}

// ErrNotLoggedIn은 해당 환경에 저장된 토큰이 없을 때 돌려준다.
var ErrNotLoggedIn = errors.New("로그인이 필요합니다")

// Store는 credentials.json에 환경별 토큰을 읽고 쓴다.
type Store struct {
	Path string
}

// DefaultStore는 설정 디렉터리의 credentials.json을 쓰는 Store다.
func DefaultStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return &Store{Path: filepath.Join(dir, "credentials.json")}, nil
}

func (s *Store) readAll() (map[config.Env]Tokens, error) {
	all := map[config.Env]Tokens{}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("%s 파일을 읽을 수 없습니다: %w", s.Path, err)
	}
	return all, nil
}

func (s *Store) writeAll(all map[config.Env]Tokens) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	// 토큰 파일은 소유자만 읽어야 한다.
	return os.WriteFile(s.Path, b, 0o600)
}

// Load는 env의 토큰을 돌려준다. 없으면 ErrNotLoggedIn.
func (s *Store) Load(env config.Env) (Tokens, error) {
	all, err := s.readAll()
	if err != nil {
		return Tokens{}, err
	}
	t, ok := all[env]
	if !ok || t.AccessToken == "" {
		return Tokens{}, ErrNotLoggedIn
	}
	return t, nil
}

// Save는 env의 토큰을 덮어쓴다.
func (s *Store) Save(env config.Env, t Tokens) error {
	all, err := s.readAll()
	if err != nil {
		return err
	}
	all[env] = t
	return s.writeAll(all)
}

// Delete는 env의 토큰을 지운다. 없어도 오류가 아니다.
func (s *Store) Delete(env config.Env) error {
	all, err := s.readAll()
	if err != nil {
		return err
	}
	delete(all, env)
	return s.writeAll(all)
}
