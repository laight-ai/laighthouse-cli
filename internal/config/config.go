// Package config는 환경(dev/prod) 선택과 설정 파일 위치를 담당한다.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Env는 접속할 laighthouse 환경 이름이다.
type Env string

const (
	EnvDev  Env = "dev"
	EnvProd Env = "prod"
)

var baseURLs = map[Env]string{
	EnvDev:  "https://dev-app.laight.ai",
	EnvProd: "https://app.laight.ai",
}

// ParseEnv는 사용자가 적은 환경 이름을 검증한다.
func ParseEnv(s string) (Env, error) {
	e := Env(s)
	if _, ok := baseURLs[e]; !ok {
		return "", fmt.Errorf("알 수 없는 환경 %q (dev 또는 prod)", s)
	}
	return e, nil
}

// BaseURL은 환경의 공개 주소를 돌려준다. 백엔드는 {BaseURL}/api, 인증 서비스는 {BaseURL}/auth에 있다.
func (e Env) BaseURL() string { return baseURLs[e] }

// Dir은 설정·자격증명 파일이 놓이는 디렉터리다. $XDG_CONFIG_HOME/laighthouse, 없으면 ~/.config/laighthouse.
func Dir() (string, error) {
	if d := os.Getenv("LAIGHTHOUSE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "laighthouse"), nil
}

// Brand는 `brands use`로 정한 기본 브랜드다. 이름은 화면에 보여 주려고 함께 저장한다.
type Brand struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// File은 config.json에 저장하는 사용자 설정이다.
type File struct {
	DefaultBrand map[Env]Brand `json:"default_brand,omitempty"`
	// DefaultBrandByScope는 scope(cli.Options.Scope)마다 따로 정한 기본 브랜드다.
	DefaultBrandByScope map[string]map[Env]Brand `json:"default_brand_by_scope,omitempty"`
}

// DefaultBrandIn은 scope의 기본 브랜드를 돌려준다. scope가 비면 DefaultBrand를 쓴다. scope에 정한 것이
// 없어도 DefaultBrand로 넘어가지 않는다. scope가 다르면 브랜드도 다른 조직의 것일 수 있어서다.
func (f File) DefaultBrandIn(scope string, env Env) (Brand, bool) {
	if scope == "" {
		b, ok := f.DefaultBrand[env]
		return b, ok
	}
	b, ok := f.DefaultBrandByScope[scope][env]
	return b, ok
}

// SetDefaultBrandIn은 scope의 기본 브랜드를 정한다. scope가 비면 DefaultBrand에 쓴다.
func (f *File) SetDefaultBrandIn(scope string, env Env, b Brand) {
	if scope == "" {
		f.DefaultBrand[env] = b
		return
	}
	if f.DefaultBrandByScope == nil {
		f.DefaultBrandByScope = map[string]map[Env]Brand{}
	}
	if f.DefaultBrandByScope[scope] == nil {
		f.DefaultBrandByScope[scope] = map[Env]Brand{}
	}
	f.DefaultBrandByScope[scope][env] = b
}

func filePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load는 config.json을 읽는다. 파일이 없으면 빈 설정을 돌려준다.
func Load() (File, error) {
	f := File{DefaultBrand: map[Env]Brand{}}
	p, err := filePath()
	if err != nil {
		return f, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s 파일을 읽을 수 없습니다: %w", p, err)
	}
	if f.DefaultBrand == nil {
		f.DefaultBrand = map[Env]Brand{}
	}
	return f, nil
}

// Save는 설정을 config.json에 쓴다.
func (f File) Save() error {
	p, err := filePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}
