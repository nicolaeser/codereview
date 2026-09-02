package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestImageEntrypointIsWebhook(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	want := `ENTRYPOINT ["/usr/local/bin/codereview-webhook"]`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("Dockerfile must start the webhook binary, want %s", want)
	}
}

func TestComposeWebhookIsDefaultService(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "codereview-webhook:") {
		t.Fatal("docker-compose.yml must define codereview-webhook")
	}
}

func TestShippedConfigExamples(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"docs/CONFIGURATION.md",
		"examples/codereview.yml",
		"examples/ai-keys.json",
		"examples/.codereviewignore",
		"docker-compose.yml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("required config example missing: %s", rel)
		}
	}
}

func TestConsumerGitLabCIExamplesRemoved(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"SECURITY.md",
		"docs/SECURITY.md",
		"docs/ARCHITECTURE.md",
		"LICENSE",
		"examples/gitlab-ci-codereview.yml",
		"examples/gitlab-ci-include.yml",
		"examples/component/codereview.yml",
		"examples/EXPLAIN.md",
		"examples/AGENTS.md",
		"examples/renovate",
		"examples/webhook",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Fatalf("%s must not be shipped", rel)
		}
	}
}

func TestGrokIsNotPublished(t *testing.T) {
	root := repoRoot(t)
	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gitignore), ".grok") {
		t.Fatal(".gitignore must ignore .grok")
	}
	dockerignore, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerignore), ".grok") {
		t.Fatal(".dockerignore must exclude .grok")
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(dockerfile), "\n") {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "COPY") && !strings.HasPrefix(trim, "ADD") {
			continue
		}
		if strings.Contains(trim, ".grok") {
			t.Fatalf("Dockerfile must not copy .grok: %s", trim)
		}
	}
}
