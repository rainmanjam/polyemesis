package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type pinFile struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

type pinSource struct {
	Name    string    `yaml:"name"`
	Repo    string    `yaml:"repo"`
	URL     string    `yaml:"url"`
	Licence string    `yaml:"licence"`
	SHA     string    `yaml:"sha"`
	Dir     string    `yaml:"dir"`
	Files   []pinFile `yaml:"files"`
}

type pinUnicode struct {
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA256  string `yaml:"sha256"`
}

type pins struct {
	Sources []pinSource `yaml:"sources"`
	Unicode pinUnicode  `yaml:"unicode"`
}

func loadPins(path string) (pins, error) {
	var p pins
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	for _, s := range p.Sources {
		if len(s.SHA) != 40 {
			return p, fmt.Errorf("%s: source %q needs a full 40-character commit sha", path, s.Name)
		}
	}
	return p, nil
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func httpGet(url string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// fetchSources downloads every pinned file at its pinned commit, checks the
// SHA-256, and writes it under sources/. A mismatch is an error and writes
// nothing: the pin is the contract.
func fetchSources(p pins, dir string) error {
	for _, s := range p.Sources {
		for _, f := range s.Files {
			url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", s.Repo, s.SHA, f.Path)
			b, err := httpGet(url)
			if err != nil {
				return err
			}
			if got := hashHex(b); got != f.SHA256 {
				return fmt.Errorf("%s/%s: sha256 %s does not match the pin %s", s.Dir, f.Path, got, f.SHA256)
			}
			dst := filepath.Join(dir, s.Dir, f.Path)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func fetchConfusables(u pinUnicode) ([]byte, error) {
	b, err := httpGet(u.URL)
	if err != nil {
		return nil, err
	}
	if got := hashHex(b); got != u.SHA256 {
		return nil, fmt.Errorf("confusables.txt: sha256 %s does not match the pin %s", got, u.SHA256)
	}
	return b, nil
}
