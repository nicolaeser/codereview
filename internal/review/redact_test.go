package review

import (
	"strings"
	"testing"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
)

// Split so GitHub push protection does not treat the fixture as a live Slack token.
const slackFixture = "xox" + "b-fixture00-fixture00-NotARealSlackToken"

func TestRedactSecretsPatterns(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		secret string
	}{
		{
			name:   "pem private key",
			in:     "key follows\n-----BEGIN RSA PRIVATE KEY-----\nFAKESECRET_k1l2m3n4o5p6q7r8s9t0u1v2\n-----END RSA PRIVATE KEY-----\nend",
			secret: "FAKESECRET_k1l2m3n4o5p6q7r8s9t0u1v2",
		},
		{
			name:   "aws access key",
			in:     "aws_access_key_id=AKIA0000000000FAKE00",
			secret: "AKIA0000000000FAKE00",
		},
		{
			name:   "bearer token",
			in:     "Authorization: Bearer 0000000000FakeBearerTokenValue",
			secret: "0000000000FakeBearerTokenValue",
		},
		{
			name:   "github pat ghp",
			in:     "token=ghp_0000000000FakePat00000000",
			secret: "ghp_0000000000FakePat00000000",
		},
		{
			name:   "github pat fine grained",
			in:     "token=github_pat_0000000000FakeExample",
			secret: "github_pat_0000000000FakeExample",
		},
		{
			name:   "gitlab pat",
			in:     "token=glpat-0000000000FakeTokenXX",
			secret: "glpat-0000000000FakeTokenXX",
		},
		{
			name:   "slack token",
			in:     "slack=" + slackFixture,
			secret: slackFixture,
		},
		{
			name:   "api_key assignment",
			in:     `api_key = "0000000000FakeApiKeyValue"`,
			secret: "0000000000FakeApiKeyValue",
		},
		{
			name:   "api-key assignment",
			in:     `api-key="0000000000FakeApiKeyValue"`,
			secret: "0000000000FakeApiKeyValue",
		},
		{
			name:   "secret assignment",
			in:     "secret: 0000000000FakeSecretValue",
			secret: "0000000000FakeSecretValue",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactSecrets(tt.in)
			if strings.Contains(got, tt.secret) {
				t.Fatalf("redacted output still contains secret %q:\n%s", tt.secret, got)
			}
			if !strings.Contains(got, redactedPlaceholder) {
				t.Fatalf("expected placeholder %q in:\n%s", redactedPlaceholder, got)
			}
		})
	}
}

func TestRedactSecretsLeavesPlainText(t *testing.T) {
	in := "Do not commit a secret to git. Document Bearer authentication and api_key rotation in prose."
	if got := redactSecrets(in); got != in {
		t.Fatalf("plain text changed:\n%s", got)
	}
}

func TestRedactThenTruncateDoesNotLeakSecretPrefix(t *testing.T) {
	secret := "AKIA0000000000FAKE00"
	text := strings.Repeat("x", 100) + secret + strings.Repeat("y", 100)
	got := truncate(redactSecrets(text), 105)
	if strings.Contains(got, secret) || strings.Contains(got, "AKIA") {
		t.Fatalf("truncated redacted text leaked secret material:\n%s", got)
	}
}

func TestBuildReviewRequestsRedactsUntrustedSecrets(t *testing.T) {
	secret := "AKIA0000000000FAKE00"
	instruction := "Trusted INSTRUCTION.md may mention " + secret + " as an operator example."
	mr := gitlab.MergeRequest{
		Title:       "Add login",
		Description: "token " + secret,
		Author:      gitlab.User{Username: "alice"},
	}
	diffs := []PreparedDiff{{Diff: gitlab.Diff{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n+" + secret + "\n"}}}
	ctx := RepositoryContext{
		Files:  map[string]string{"README.md": "key " + secret},
		Issues: []gitlab.Issue{{IID: 1, Title: "Leak " + secret, Description: "body " + secret}},
		Tree:   []gitlab.TreeEntry{{Path: "src/" + secret, Type: "blob"}},
	}
	reqs := BuildReviewRequests(instruction, mr, diffs, ctx, []config.PathInstruction{
		{Path: "a.go", Instructions: "Keep this secret handling guidance."},
	}, 20000, false, ModeStandard, false, "")
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if !strings.Contains(reqs[0].System, secret) {
		t.Fatal("trusted instruction content must not be redacted")
	}
	if !strings.Contains(reqs[0].System, "Keep this secret handling guidance.") {
		t.Fatal("trusted path instructions must remain intact")
	}
	if strings.Contains(reqs[0].User, secret) {
		t.Fatalf("untrusted user content still contains secret:\n%s", reqs[0].User)
	}
	if !strings.Contains(reqs[0].User, redactedPlaceholder) {
		t.Fatal("expected redaction placeholder in user content")
	}
}

func TestBuildReviewRequestsRedactsEveryBatch(t *testing.T) {
	secret := "ghp_0000000000FakePat00000000"
	chunk := strings.Repeat("x", 4000) + " " + secret
	diffs := []PreparedDiff{
		{Diff: gitlab.Diff{OldPath: "a.go", NewPath: "a.go", Diff: chunk}},
		{Diff: gitlab.Diff{OldPath: "b.go", NewPath: "b.go", Diff: chunk}},
	}
	reqs := BuildReviewRequests("instr", gitlab.MergeRequest{Title: "t"}, diffs, RepositoryContext{}, nil, 5000, false, ModeStandard, false, "")
	if len(reqs) < 2 {
		t.Fatalf("expected multiple batches, got %d", len(reqs))
	}
	for i, req := range reqs {
		if strings.Contains(req.User, secret) {
			t.Fatalf("batch %d user content still contains secret", i)
		}
		if !strings.Contains(req.User, redactedPlaceholder) {
			t.Fatalf("batch %d missing redaction placeholder", i)
		}
	}
}

func TestBuildReviewRequestsRedactsIssueDescriptionBeforeTruncate(t *testing.T) {
	secret := "AKIA0000000000FAKE00"
	desc := strings.Repeat("z", 5990) + secret
	diffs := []PreparedDiff{{Diff: gitlab.Diff{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n+ok\n"}}}
	reqs := BuildReviewRequests("instr", gitlab.MergeRequest{Title: "t"}, diffs, RepositoryContext{
		Issues: []gitlab.Issue{{IID: 9, Title: "issue", Description: desc}},
	}, nil, 100000, false, ModeStandard, false, "")
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if strings.Contains(reqs[0].User, secret) || strings.Contains(reqs[0].User, "AKIA") {
		t.Fatalf("issue truncation leaked secret material:\n%s", reqs[0].User)
	}
}
