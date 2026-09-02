package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGitHubWorkflowsKeepChannelIsolatedPublishJobs(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = raw
	}
	if _, ok := files["development.yml"]; !ok {
		t.Fatal("missing .github/workflows/development.yml")
	}
	if _, ok := files["main.yml"]; !ok {
		t.Fatal("missing .github/workflows/main.yml")
	}

	dev := parseWorkflowFile(t, "development.yml", files["development.yml"])
	mainWF := parseWorkflowFile(t, "main.yml", files["main.yml"])

	assertPushBranches(t, "development.yml", dev, "development")
	assertPushBranches(t, "main.yml", mainWF, "main")
	if containsStringFold(dev.pushBranches, "main") {
		t.Fatal("development.yml must not run on push to main")
	}
	if containsStringFold(mainWF.pushBranches, "development") {
		t.Fatal("main.yml must not run on push to development")
	}

	devPublish := publishJobs(t, "development.yml", dev)
	mainPublish := publishJobs(t, "main.yml", mainWF)
	if len(devPublish) == 0 {
		t.Fatal("development.yml must have a publish job")
	}
	if len(mainPublish) == 0 {
		t.Fatal("main.yml must have a publish/release job")
	}

	for _, job := range devPublish {
		if !strings.Contains(job.ifExpr, "github.event_name == 'push'") || !strings.Contains(job.ifExpr, "refs/heads/development") {
			t.Fatalf("development.yml job %q publish/release if must be push to development, got %q", job.name, job.ifExpr)
		}
		if metadataTagEnabled(job.imageTags, "latest") || hardcodedImageTag(job.pushTags, "latest") {
			t.Fatalf("development.yml job %q must not publish GHCR tag latest: meta=%q push=%q", job.name, job.imageTags, job.pushTags)
		}
		if job.createsRelease && !job.prerelease {
			t.Fatalf("development.yml job %q must not create a non-prerelease GitHub Release", job.name)
		}
	}
	for _, job := range mainPublish {
		if !strings.Contains(job.ifExpr, "github.event_name == 'push'") || !strings.Contains(job.ifExpr, "refs/heads/main") {
			t.Fatalf("main.yml job %q publish/release if must be push to main, got %q", job.name, job.ifExpr)
		}
		if metadataTagEnabled(job.imageTags, "dev") || metadataTagEnabled(job.imageTags, "development") || hardcodedImageTag(job.pushTags, "dev") || hardcodedImageTag(job.pushTags, "development") {
			t.Fatalf("main.yml job %q must not publish GHCR tags dev or development: meta=%q push=%q", job.name, job.imageTags, job.pushTags)
		}
		if job.createsRelease && job.prerelease {
			t.Fatalf("main.yml job %q must create a non-prerelease GitHub Release", job.name)
		}
	}
}

type parsedWorkflow struct {
	pushBranches []string
	jobs         map[string]parsedJob
}

type parsedJob struct {
	name           string
	ifExpr         string
	imageTags      string
	pushTags       string
	createsRelease bool
	prerelease     bool
}

func parseWorkflowFile(t *testing.T, name string, raw []byte) parsedWorkflow {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	on, _ := doc["on"].(map[string]any)
	if on == nil {
		t.Fatalf("%s: missing on:", name)
	}
	push, _ := on["push"].(map[string]any)
	if push == nil {
		t.Fatalf("%s: missing on.push", name)
	}
	parsed := parsedWorkflow{jobs: map[string]parsedJob{}}
	parsed.pushBranches = stringSlice(push["branches"])
	jobs, _ := doc["jobs"].(map[string]any)
	if len(jobs) == 0 {
		t.Fatalf("%s: missing jobs", name)
	}
	for jobName, rawJob := range jobs {
		jobMap, _ := rawJob.(map[string]any)
		if jobMap == nil {
			continue
		}
		job := parsedJob{name: jobName}
		if expr, ok := jobMap["if"].(string); ok {
			job.ifExpr = expr
		}
		steps, _ := jobMap["steps"].([]any)
		for _, rawStep := range steps {
			step, _ := rawStep.(map[string]any)
			if step == nil {
				continue
			}
			uses, _ := step["uses"].(string)
			run, _ := step["run"].(string)
			with, _ := step["with"].(map[string]any)
			if strings.Contains(uses, "docker/metadata-action") && with != nil {
				if tags, ok := with["tags"].(string); ok {
					job.imageTags += tags + "\n"
				}
			}
			if strings.Contains(uses, "docker/build-push-action") && with != nil {
				if tags, ok := with["tags"].(string); ok {
					job.pushTags += tags + "\n"
				}
			}
			if strings.Contains(run, "gh release create") {
				job.createsRelease = true
				job.prerelease = strings.Contains(run, "--prerelease")
			}
		}
		parsed.jobs[jobName] = job
	}
	return parsed
}

func publishJobs(t *testing.T, file string, wf parsedWorkflow) []parsedJob {
	t.Helper()
	var out []parsedJob
	for _, job := range wf.jobs {
		if strings.TrimSpace(job.imageTags) == "" && !job.createsRelease {
			continue
		}
		out = append(out, job)
	}
	if len(out) == 0 {
		t.Fatalf("%s: no publish/release job found", file)
	}
	return out
}

func assertPushBranches(t *testing.T, file string, wf parsedWorkflow, want string) {
	t.Helper()
	if !containsStringFold(wf.pushBranches, want) {
		t.Fatalf("%s on.push.branches = %#v, want %q", file, wf.pushBranches, want)
	}
}

func hardcodedImageTag(tags, value string) bool {
	for _, line := range strings.Split(tags, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == value || strings.HasSuffix(line, ":"+value) {
			return true
		}
	}
	return false
}

func metadataTagEnabled(tags, value string) bool {
	for _, line := range strings.Split(tags, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "value="+value) {
			continue
		}
		if i := strings.Index(line, "enable="); i >= 0 {
			enable := strings.TrimSpace(line[i+len("enable="):])
			if enable == "false" {
				continue
			}
		}
		return true
	}
	return false
}

func stringSlice(raw any) []string {
	switch value := raw.(type) {
	case string:
		return []string{value}
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func containsStringFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
