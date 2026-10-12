package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C1ph3rX13/Aether/internal/config"
)

func TestBootstrapManifestContract(t *testing.T) {
	example, err := os.ReadFile("../../configs/bootstrap-deployment.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("explicit relative path uses only CWD", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "deployment.toml"), example, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		manifest, err := config.LoadBootstrap("deployment.toml")
		if err != nil {
			t.Fatal(err)
		}
		if manifest.Deployment.ID != "aether-local" || manifest.Operator.Name != "local-operator" ||
			manifest.Server.ListenPort != 8000 || manifest.Server.DatabasePath != "/var/lib/aether/aether.db" ||
			manifest.Dispatcher.DockerHost != "unix:///var/run/docker.sock" {
			t.Fatalf("manifest = %+v", manifest)
		}
		if _, err := config.LoadBootstrap(""); err == nil {
			t.Fatal("missing explicit path discovered a manifest")
		}
	})
	for _, test := range []struct{ name, old, replacement string }{
		{"unknown field", "[deployment]", "[deployment]\nunknown = true"},
		{"unknown empty table", "[deployment]", "[unknown]\n[deployment]"},
		{"missing schema", `schema = "aether.bootstrap/v1"`, ""},
		{"unsupported schema", `schema = "aether.bootstrap/v1"`, `schema = "aether.bootstrap/v2"`},
		{"missing identity", `id = "aether-local"`, ""},
		{"missing operator", `name = "local-operator"`, ""},
		{"missing host", `listen_host = "127.0.0.1"`, ""},
		{"missing port", `listen_port = 8000`, ""},
		{"missing database", `database_path = "/var/lib/aether/aether.db"`, ""},
		{"missing control plane", `control_plane_url = "http://127.0.0.1:8000/api/v1"`, ""},
		{"missing Docker", `docker_host = "unix:///var/run/docker.sock"`, ""},
		{"wrong type", `listen_port = 8000`, `listen_port = "8000"`},
		{"zero port", `listen_port = 8000`, `listen_port = 0`},
		{"large port", `listen_port = 8000`, `listen_port = 65536`},
		{"negative port", `listen_port = 8000`, `listen_port = -1`},
		{"duplicate field", `listen_port = 8000`, "listen_port = 8000\nlisten_port = 8001"},
		{"field alias", `listen_port = 8000`, `Listen_Port = 8000`},
		{"relative database", `database_path = "/var/lib/aether/aether.db"`, `database_path = "data/aether.db"`},
		{"database environment", `database_path = "/var/lib/aether/aether.db"`, `database_path = "/data/${DB}.db"`},
		{"database home", `database_path = "/var/lib/aether/aether.db"`, `database_path = "~/aether.db"`},
		{"database URI", `database_path = "/var/lib/aether/aether.db"`, `database_path = "file:/data/aether.db?mode=memory"`},
		{"host with port", `listen_host = "127.0.0.1"`, `listen_host = "127.0.0.1:8000"`},
		{"invalid IP", `listen_host = "127.0.0.1"`, `listen_host = "999.1.1.1"`},
		{"invalid hostname", `listen_host = "127.0.0.1"`, `listen_host = "bad host"`},
		{"URL port", `control_plane_url = "http://127.0.0.1:8000/api/v1"`, `control_plane_url = "http://localhost:65536/api/v1"`},
		{"URL credentials", `control_plane_url = "http://127.0.0.1:8000/api/v1"`, `control_plane_url = "http://user:pass@localhost/api/v1"`},
		{"URL path", `control_plane_url = "http://127.0.0.1:8000/api/v1"`, `control_plane_url = "http://localhost"`},
		{"URL query", `control_plane_url = "http://127.0.0.1:8000/api/v1"`, `control_plane_url = "http://localhost/api/v1?"`},
		{"relative socket", `docker_host = "unix:///var/run/docker.sock"`, `docker_host = "unix://docker.sock"`},
		{"socket environment", `docker_host = "unix:///var/run/docker.sock"`, `docker_host = "unix:///${SOCKET}"`},
		{"invalid Docker port", `docker_host = "unix:///var/run/docker.sock"`, `docker_host = "tcp://localhost:0"`},
		{"Docker TCP path", `docker_host = "unix:///var/run/docker.sock"`, `docker_host = "tcp://localhost:2375/api"`},
		{"Docker TCP root path", `docker_host = "unix:///var/run/docker.sock"`, `docker_host = "tcp://localhost:2375/"`},
		{"Docker force query", `docker_host = "unix:///var/run/docker.sock"`, `docker_host = "tcp://localhost:2375?"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "deployment.toml")
			if err := os.WriteFile(path, []byte(strings.Replace(string(example), test.old, test.replacement, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.LoadBootstrap(path); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}
