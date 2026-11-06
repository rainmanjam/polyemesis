package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// THE IMAGES' RUNTIME TAIL, ONE FACT FOUR TIMES. Dockerfile, Dockerfile.cuda
// and Dockerfile.vaapi each end with the same ports, volume, user, health probe
// and command, and docker-compose.yml repeats the ports and the probe. Docker
// has no include, and a build arg cannot carry an instruction, so the tail is
// copied -- and a copy drifts: a port added to one image, a probe tuned in
// another, a uid changed in a third, and the operator who switches variants
// finds a different container. Sharing it through a base stage or build args
// would change the layers of the shipped images; this keeps them byte-identical
// and fails when the copies disagree.

type dockerTail struct {
	expose      []string
	volume      string
	user        string
	workdir     string
	healthcheck string
	entrypoint  string
	cmd         string
	uid         string
}

func readDockerTail(t *testing.T, path string) dockerTail {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Join continuation lines, drop comments.
	var logical []string
	var cur string
	for _, ln := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") && cur == "" {
			continue
		}
		if strings.HasSuffix(ln, "\\") {
			cur += strings.TrimSuffix(ln, "\\") + " "
			continue
		}
		logical = append(logical, strings.Join(strings.Fields(cur+ln), " "))
		cur = ""
	}
	var d dockerTail
	uid := regexp.MustCompile(`(?:--uid|-u) (\d+) .*polyemesis`)
	for _, ln := range logical {
		kw, rest, _ := strings.Cut(ln, " ")
		switch kw {
		case "EXPOSE":
			d.expose = append(d.expose, rest)
		case "VOLUME":
			d.volume = rest
		case "USER":
			d.user = rest
		case "WORKDIR":
			d.workdir = rest // the last one is the one the process runs in
		case "HEALTHCHECK":
			d.healthcheck = rest
		case "ENTRYPOINT":
			d.entrypoint = rest
		case "CMD":
			d.cmd = rest
		case "RUN":
			if m := uid.FindStringSubmatch(rest); m != nil {
				d.uid = m[1]
			}
		}
	}
	sort.Strings(d.expose)
	return d
}

func TestTheDockerfilesShareOneRuntimeTail(t *testing.T) {
	root := repoRootFromTest(t)
	base := readDockerTail(t, filepath.Join(root, "Dockerfile"))
	if len(base.expose) != 4 || base.volume == "" || base.user == "" || base.healthcheck == "" ||
		base.entrypoint == "" || base.cmd == "" || base.uid == "" {
		t.Fatalf("read an incomplete tail from Dockerfile (%+v); the parser is looking at the wrong thing", base)
	}
	if base.uid != "10001" {
		t.Errorf("Dockerfile creates the runtime user with uid %s; the images and the documented volume ownership say 10001", base.uid)
	}
	for _, name := range []string{"Dockerfile.cuda", "Dockerfile.vaapi"} {
		got := readDockerTail(t, filepath.Join(root, name))
		if strings.Join(got.expose, ",") != strings.Join(base.expose, ",") {
			t.Errorf("%s exposes %v, Dockerfile exposes %v", name, got.expose, base.expose)
		}
		for field, pair := range map[string][2]string{
			"VOLUME": {got.volume, base.volume}, "USER": {got.user, base.user},
			"WORKDIR": {got.workdir, base.workdir}, "HEALTHCHECK": {got.healthcheck, base.healthcheck},
			"ENTRYPOINT": {got.entrypoint, base.entrypoint}, "CMD": {got.cmd, base.cmd},
			"uid": {got.uid, base.uid},
		} {
			if pair[0] != pair[1] {
				t.Errorf("%s's %s is %q, Dockerfile's is %q", name, field, pair[0], pair[1])
			}
		}
	}
}

// docker-compose.yml's published ports are the images' exposed ones, and its
// health probe is the image's.
func TestComposeAgreesWithTheImagesRuntimeTail(t *testing.T) {
	root := repoRootFromTest(t)
	base := readDockerTail(t, filepath.Join(root, "Dockerfile"))
	raw, err := os.ReadFile(filepath.Join(root, "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// The first service only: the commented-out variants below it are the same
	// lines again and are not config.
	var live []string
	for _, ln := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		live = append(live, ln)
	}
	text := strings.Join(live, "\n")

	exposed := map[string]bool{}
	for _, e := range base.expose {
		if !strings.Contains(e, "/") {
			e += "/tcp"
		}
		exposed[e] = true
	}
	ports := regexp.MustCompile(`(?m)^\s+- "(\d+):(\d+)(/udp|/tcp)?"`).FindAllStringSubmatch(text, -1)
	if len(ports) == 0 {
		t.Fatal("found no published ports in docker-compose.yml; the pattern is looking at the wrong thing")
	}
	for _, p := range ports {
		proto := p[3]
		if proto == "" {
			proto = "/tcp"
		}
		if p[1] != p[2] {
			t.Errorf("compose maps host %s to container %s; every documented port is the same on both sides", p[1], p[2])
		}
		if !exposed[p[2]+proto] {
			t.Errorf("compose publishes %s%s, which the Dockerfiles do not EXPOSE (%v)", p[2], proto, base.expose)
		}
	}

	probe := regexp.MustCompile(`http://127\.0\.0\.1:\d+/\S*[a-z]`).FindString(base.healthcheck)
	if probe == "" {
		t.Fatalf("no probe URL in the Dockerfile HEALTHCHECK %q", base.healthcheck)
	}
	if !strings.Contains(text, `"`+probe+`"`) {
		t.Errorf("compose's healthcheck does not probe %s, which the Dockerfiles' HEALTHCHECK does", probe)
	}
	for _, kv := range [][2]string{{"interval", "30s"}, {"timeout", "3s"}, {"start_period", "10s"}} {
		flag := strings.ReplaceAll(kv[0], "_", "-")
		if !strings.Contains(base.healthcheck, "--"+flag+"="+kv[1]) || !strings.Contains(text, kv[0]+": "+kv[1]) {
			t.Errorf("health %s is not %s in both the Dockerfile and compose", kv[0], kv[1])
		}
	}
}
