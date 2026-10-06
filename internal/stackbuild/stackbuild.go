package stackbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"tessera/internal/api"

	"gopkg.in/yaml.v3"
)

type Run func(context.Context, string, ...string) ([]byte, error)

type Spec struct {
	Base           string   `yaml:"base"`
	Repository     string   `yaml:"repository"`
	Context        string   `yaml:"context"`
	PackageManager string   `yaml:"package_manager"`
	Packages       []string `yaml:"packages"`
	Command        []string `yaml:"command"`
	Push           bool     `yaml:"push"`
}

var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var packageName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9+_.:=-]*$`)
var pushDigest = regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`)

func Prepare(ctx context.Context, manifestPath string, body []byte, run Run) ([]byte, []string, error) {
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	var docs []map[string]any
	var builds []struct {
		index int
		spec  Spec
	}
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if len(doc) == 0 {
			continue
		}
		if raw, ok := doc["build"]; ok {
			if doc["kind"] != "App" {
				return nil, nil, fmt.Errorf("build requires kind: App")
			}
			encoded, err := yaml.Marshal(raw)
			if err != nil {
				return nil, nil, err
			}
			var spec Spec
			specDecoder := yaml.NewDecoder(bytes.NewReader(encoded))
			specDecoder.KnownFields(true)
			if err := specDecoder.Decode(&spec); err != nil {
				return nil, nil, err
			}
			if err := spec.Validate(); err != nil {
				return nil, nil, fmt.Errorf("app %v build: %w", doc["name"], err)
			}
			builds = append(builds, struct {
				index int
				spec  Spec
			}{index: len(docs), spec: spec})
		}
		docs = append(docs, doc)
	}
	if len(builds) == 0 {
		return body, nil, nil
	}
	for _, build := range builds {
		docs[build.index]["image"] = "tessera-pending-build"
		delete(docs[build.index], "build")
	}
	validationBody, err := encodeDocs(docs)
	if err != nil {
		return nil, nil, err
	}
	if _, err := api.DecodeAll(validationBody); err != nil {
		return nil, nil, err
	}
	var images []string
	for _, build := range builds {
		image, err := build.spec.Build(ctx, manifestPath, run)
		if err != nil {
			return nil, nil, err
		}
		docs[build.index]["image"] = image
		images = append(images, image)
	}
	out, err := encodeDocs(docs)
	return out, images, err
}

func encodeDocs(docs []map[string]any) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	defer encoder.Close()
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func (s Spec) Validate() error {
	if !safeAtom(s.Base) || !safeAtom(s.Repository) || strings.Contains(s.Repository, "@") || strings.Contains(s.Repository[strings.LastIndex(s.Repository, "/")+1:], ":") {
		return fmt.Errorf("base and untagged repository are required")
	}
	if len(s.Command) == 0 {
		return fmt.Errorf("command is required")
	}
	if s.Command[0] == "" {
		return fmt.Errorf("command executable is required")
	}
	for _, arg := range s.Command {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("command contains a NUL byte")
		}
	}
	if s.Context != "" && (strings.ContainsAny(s.Context, "\n\r\x00") || strings.HasPrefix(s.Context, "-")) {
		return fmt.Errorf("invalid context path")
	}
	if len(s.Packages) > 0 && s.PackageManager != "apk" && s.PackageManager != "apt" {
		return fmt.Errorf("packages require package_manager: apk or apt")
	}
	for _, pkg := range s.Packages {
		if !packageName.MatchString(pkg) {
			return fmt.Errorf("invalid package %q", pkg)
		}
	}
	return nil
}

func (s Spec) Build(ctx context.Context, manifestPath string, run Run) (string, error) {
	contextDir := s.Context
	if contextDir == "" {
		contextDir = "."
	}
	if !filepath.IsAbs(contextDir) && manifestPath != "-" {
		contextDir = filepath.Join(filepath.Dir(manifestPath), contextDir)
	}
	info, err := os.Stat(contextDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("build context is not a directory: %s", contextDir)
	}
	temp, err := os.MkdirTemp("", "tessera-build-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	dockerfile := filepath.Join(temp, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte(s.Dockerfile()), 0o600); err != nil {
		return "", err
	}
	temporaryTag := "tessera-build:" + filepath.Base(temp)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = run(cleanup, "docker", "image", "rm", temporaryTag)
	}()
	if output, err := run(ctx, "docker", "build", "-f", dockerfile, "-t", temporaryTag, contextDir); err != nil {
		return "", fmt.Errorf("docker build: %w: %s", err, strings.TrimSpace(string(output)))
	}
	output, err := run(ctx, "docker", "image", "inspect", "--format={{.Id}}", temporaryTag)
	if err != nil {
		return "", fmt.Errorf("docker inspect: %w: %s", err, strings.TrimSpace(string(output)))
	}
	id := strings.TrimSpace(string(output))
	if !imageID.MatchString(id) {
		return "", fmt.Errorf("invalid Docker image ID %q", id)
	}
	image := s.Repository + ":sha256-" + strings.TrimPrefix(id, "sha256:")
	if output, err := run(ctx, "docker", "tag", temporaryTag, image); err != nil {
		return "", fmt.Errorf("docker tag: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if !s.Push {
		return image, nil
	}
	output, err = run(ctx, "docker", "push", image)
	if err != nil {
		return "", fmt.Errorf("docker push: %w: %s", err, strings.TrimSpace(string(output)))
	}
	digest := pushDigest.FindSubmatch(output)
	if len(digest) != 2 {
		return "", fmt.Errorf("docker push did not report an image digest")
	}
	return s.Repository + "@" + string(digest[1]), nil
}

func (s Spec) Dockerfile() string {
	var out strings.Builder
	fmt.Fprintf(&out, "FROM %s\n", s.Base)
	if len(s.Packages) > 0 {
		if s.PackageManager == "apk" {
			fmt.Fprintf(&out, "RUN apk add --no-cache %s\n", strings.Join(s.Packages, " "))
		} else {
			fmt.Fprintf(&out, "RUN apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*\n", strings.Join(s.Packages, " "))
		}
	}
	out.WriteString("WORKDIR /app\nCOPY . .\n")
	command, _ := json.Marshal(s.Command)
	fmt.Fprintf(&out, "CMD %s\n", command)
	return out.String()
}

func safeAtom(value string) bool {
	return value != "" && !strings.ContainsAny(value, " \\\t\n\r\x00") && !strings.HasPrefix(value, "-")
}
