package testenv_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCIFFmpegDownloadUsesAuthenticatedFallback(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, ".github", "workflows", "ci.yml")), &wf); err != nil {
		t.Fatalf("parse .github/workflows/ci.yml: %v", err)
	}

	const asset = "ffmpeg-n8.1-latest-linux64-gpl-8.1.tar.xz"
	var checked int
	for job, j := range wf.Jobs {
		for _, s := range j.Steps {
			if !strings.Contains(s.Run, asset) {
				continue
			}
			checked++
			if got := s.Env["GH_TOKEN"]; got != "${{ github.token }}" {
				t.Fatalf("job %s step %q sets GH_TOKEN=%q, want ${{ github.token }}", job, s.Name, got)
			}
			if !strings.Contains(s.Run, "gh release download latest --repo BtbN/FFmpeg-Builds") {
				t.Fatalf("job %s step %q downloads %s without the authenticated gh fallback", job, s.Name, asset)
			}
			if !strings.Contains(s.Run, "curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL") {
				t.Fatalf("job %s step %q downloads %s without the hardened curl fallback", job, s.Name, asset)
			}
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d FFmpeg download step(s), want 2 so the assertion cannot pass by examining too little", checked)
	}
}
