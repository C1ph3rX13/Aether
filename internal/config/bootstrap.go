package config

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const BootstrapSchemaV1 = "aether.bootstrap/v1"

type BootstrapManifest struct {
	Schema     string              `toml:"schema"`
	Deployment BootstrapDeployment `toml:"deployment"`
	Operator   BootstrapOperator   `toml:"operator"`
	Server     BootstrapServer     `toml:"server"`
	Dispatcher BootstrapDispatcher `toml:"dispatcher"`
}

type BootstrapDeployment struct {
	ID string `toml:"id"`
}

type BootstrapOperator struct {
	Name string `toml:"name"`
}

type BootstrapServer struct {
	ListenHost   string `toml:"listen_host"`
	ListenPort   int    `toml:"listen_port"`
	DatabasePath string `toml:"database_path"`
}

type BootstrapDispatcher struct {
	ControlPlaneURL string `toml:"control_plane_url"`
	DockerHost      string `toml:"docker_host"`
}

type bootstrapDocument struct {
	Schema     *string                      `toml:"schema"`
	Deployment *bootstrapDeploymentDocument `toml:"deployment"`
	Operator   *bootstrapOperatorDocument   `toml:"operator"`
	Server     *bootstrapServerDocument     `toml:"server"`
	Dispatcher *bootstrapDispatcherDocument `toml:"dispatcher"`
}

type bootstrapDeploymentDocument struct {
	ID *string `toml:"id"`
}

type bootstrapOperatorDocument struct {
	Name *string `toml:"name"`
}

type bootstrapServerDocument struct {
	ListenHost   *string `toml:"listen_host"`
	ListenPort   *int    `toml:"listen_port"`
	DatabasePath *string `toml:"database_path"`
}

type bootstrapDispatcherDocument struct {
	ControlPlaneURL *string `toml:"control_plane_url"`
	DockerHost      *string `toml:"docker_host"`
}

// LoadBootstrap reads one explicit Bootstrap Manifest. Relative manifest paths
// resolve once against the process working directory.
func LoadBootstrap(path string) (BootstrapManifest, error) {
	if strings.TrimSpace(path) == "" {
		return BootstrapManifest{}, fmt.Errorf("Bootstrap Manifest path is required")
	}
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return BootstrapManifest{}, fmt.Errorf("resolve Bootstrap Manifest path: %w", err)
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return BootstrapManifest{}, fmt.Errorf("read Bootstrap Manifest: %w", err)
	}
	if err := validateBootstrapKeys(data); err != nil {
		return BootstrapManifest{}, err
	}
	var document bootstrapDocument
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&document); err != nil {
		return BootstrapManifest{}, fmt.Errorf("parse Bootstrap Manifest: %w", err)
	}
	return bootstrapManifestFromDocument(document)
}

func validateBootstrapKeys(data []byte) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse Bootstrap Manifest keys: %w", err)
	}
	known := map[string]map[string]struct{}{
		"":           {"schema": {}, "deployment": {}, "operator": {}, "server": {}, "dispatcher": {}},
		"deployment": {"id": {}},
		"operator":   {"name": {}},
		"server":     {"listen_host": {}, "listen_port": {}, "database_path": {}},
		"dispatcher": {"control_plane_url": {}, "docker_host": {}},
	}
	for key, value := range raw {
		if key == "schema" {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("Bootstrap Manifest schema must be a string")
			}
			continue
		}
		if _, ok := known[""][key]; !ok {
			return fmt.Errorf("unknown Bootstrap Manifest field %q", key)
		}
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("Bootstrap Manifest table %q must be a table", key)
		}
		for field := range object {
			if _, ok := known[key][field]; !ok {
				return fmt.Errorf("unknown Bootstrap Manifest field %s.%s", key, field)
			}
		}
	}
	return nil
}

func bootstrapManifestFromDocument(document bootstrapDocument) (BootstrapManifest, error) {
	if document.Schema == nil {
		return BootstrapManifest{}, fmt.Errorf("schema is required")
	}
	if *document.Schema != BootstrapSchemaV1 {
		return BootstrapManifest{}, fmt.Errorf("unsupported Bootstrap Manifest schema %q", *document.Schema)
	}
	if document.Deployment == nil {
		return BootstrapManifest{}, fmt.Errorf("deployment table is required")
	}
	if document.Operator == nil {
		return BootstrapManifest{}, fmt.Errorf("operator table is required")
	}
	if document.Server == nil {
		return BootstrapManifest{}, fmt.Errorf("server table is required")
	}
	if document.Dispatcher == nil {
		return BootstrapManifest{}, fmt.Errorf("dispatcher table is required")
	}
	if document.Deployment.ID == nil {
		return BootstrapManifest{}, fmt.Errorf("deployment.id is required")
	}
	if document.Operator.Name == nil {
		return BootstrapManifest{}, fmt.Errorf("operator.name is required")
	}
	if document.Server.ListenHost == nil {
		return BootstrapManifest{}, fmt.Errorf("server.listen_host is required")
	}
	if document.Server.ListenPort == nil {
		return BootstrapManifest{}, fmt.Errorf("server.listen_port is required")
	}
	if document.Server.DatabasePath == nil {
		return BootstrapManifest{}, fmt.Errorf("server.database_path is required")
	}
	if document.Dispatcher.ControlPlaneURL == nil {
		return BootstrapManifest{}, fmt.Errorf("dispatcher.control_plane_url is required")
	}
	if document.Dispatcher.DockerHost == nil {
		return BootstrapManifest{}, fmt.Errorf("dispatcher.docker_host is required")
	}

	manifest := BootstrapManifest{
		Schema:     BootstrapSchemaV1,
		Deployment: BootstrapDeployment{ID: strings.TrimSpace(*document.Deployment.ID)},
		Operator:   BootstrapOperator{Name: strings.TrimSpace(*document.Operator.Name)},
		Server: BootstrapServer{
			ListenHost:   strings.TrimSpace(*document.Server.ListenHost),
			ListenPort:   *document.Server.ListenPort,
			DatabasePath: *document.Server.DatabasePath,
		},
		Dispatcher: BootstrapDispatcher{
			ControlPlaneURL: strings.TrimSpace(*document.Dispatcher.ControlPlaneURL),
			DockerHost:      strings.TrimSpace(*document.Dispatcher.DockerHost),
		},
	}
	if err := validateBootstrapManifest(manifest); err != nil {
		return BootstrapManifest{}, err
	}
	manifest.Server.DatabasePath = filepath.Clean(manifest.Server.DatabasePath)
	return manifest, nil
}

func validateBootstrapManifest(manifest BootstrapManifest) error {
	for name, value := range map[string]string{
		"deployment.id":                manifest.Deployment.ID,
		"operator.name":                manifest.Operator.Name,
		"server.listen_host":           manifest.Server.ListenHost,
		"server.database_path":         manifest.Server.DatabasePath,
		"dispatcher.control_plane_url": manifest.Dispatcher.ControlPlaneURL,
		"dispatcher.docker_host":       manifest.Dispatcher.DockerHost,
	} {
		if value == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
		if strings.Contains(value, "$") {
			return fmt.Errorf("%s must not contain environment expansion", name)
		}
	}
	if manifest.Server.ListenPort < 1 || manifest.Server.ListenPort > 65535 {
		return fmt.Errorf("server.listen_port must be between 1 and 65535, got %d", manifest.Server.ListenPort)
	}
	if err := validateHost(manifest.Server.ListenHost); err != nil {
		return fmt.Errorf("server.listen_host: %w", err)
	}
	if !filepath.IsAbs(manifest.Server.DatabasePath) || strings.HasPrefix(manifest.Server.DatabasePath, "~") ||
		strings.Contains(manifest.Server.DatabasePath, "://") {
		return fmt.Errorf("server.database_path must be an absolute SQLite path")
	}
	if err := validateHTTPControlPlaneURL(manifest.Dispatcher.ControlPlaneURL); err != nil {
		return fmt.Errorf("dispatcher.control_plane_url: %w", err)
	}
	if err := validateDockerHost(manifest.Dispatcher.DockerHost); err != nil {
		return fmt.Errorf("dispatcher.docker_host: %w", err)
	}
	return nil
}

func validateHost(value string) error {
	if net.ParseIP(value) != nil {
		return nil
	}
	if strings.ContainsAny(value, " \t\r\n:/[]") || strings.Contains(value, "\\") {
		return fmt.Errorf("invalid listen host")
	}
	if strings.Contains(value, ".") {
		allNumeric := true
		for _, label := range strings.Split(value, ".") {
			if label == "" {
				allNumeric = false
				break
			}
			for _, character := range label {
				if character < '0' || character > '9' {
					allNumeric = false
					break
				}
			}
		}
		if allNumeric {
			return fmt.Errorf("invalid listen host")
		}
	}
	if len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return fmt.Errorf("invalid listen host")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid listen host")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return fmt.Errorf("invalid listen host")
			}
		}
	}
	return nil
}

func validateHTTPControlPlaneURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Opaque != "" || parsed.Path != "/api/v1" {
		return fmt.Errorf("must be an http(s) URL ending in /api/v1 without credentials, query, or fragment")
	}
	if err := validateURLHost(parsed.Hostname()); err != nil {
		return err
	}
	if port := parsed.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("URL port is invalid")
		}
	}
	return nil
}

func validateURLHost(value string) error {
	if value == "" {
		return fmt.Errorf("URL host is required")
	}
	return validateHost(value)
}

func validateDockerHost(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || (parsed.Host == "" && parsed.Path == "") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("must be a complete Docker client endpoint")
	}
	switch parsed.Scheme {
	case "unix":
		if parsed.Host != "" || !filepath.IsAbs(parsed.Path) || strings.HasPrefix(parsed.Path, "~") {
			return fmt.Errorf("unix Docker socket path must be absolute")
		}
	case "tcp":
		if parsed.Path != "" {
			return fmt.Errorf("tcp Docker endpoint must not contain a path")
		}
		if err := validateURLHost(parsed.Hostname()); err != nil {
			return err
		}
		port := parsed.Port()
		if port == "" {
			return fmt.Errorf("tcp Docker endpoint port is required")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("Docker endpoint port is invalid")
		}
	default:
		return fmt.Errorf("Docker endpoint scheme %q is unsupported", parsed.Scheme)
	}
	return nil
}
