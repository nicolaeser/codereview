package review

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

const (
	skipThreadReply = "Skipped as a false positive after @mention."
	helpMarker      = "<!-- codereview-help -->"
	askMarker       = "<!-- codereview-ask -->"
	skipMarker      = "<!-- codereview-skip -->"
	disabledMarker  = "<!-- codereview-disabled -->"
	maxMentionExtra = 4000
)

// SkipReplyBody is the inline reply used when a finding is marked a false positive.
func SkipReplyBody() string {
	return skipThreadReply + "\n" + skipMarker
}

type skippedThreads struct {
	discussionIDs map[string]struct{}
	findingIDs    map[string]struct{}
	lines         map[string]struct{}
	contents      map[string]struct{}
	titles        map[string]string
	titlesByKey   map[string]string
}

func newSkippedThreads() skippedThreads {
	return skippedThreads{
		discussionIDs: map[string]struct{}{},
		findingIDs:    map[string]struct{}{},
		lines:         map[string]struct{}{},
		contents:      map[string]struct{}{},
		titles:        map[string]string{},
		titlesByKey:   map[string]string{},
	}
}

func (s skippedThreads) count() int {
	return len(s.discussionIDs)
}

func (s skippedThreads) hasFinding(finding Finding, diffs []PreparedDiff) bool {
	if _, ok := s.findingIDs[FindingID(finding)]; ok {
		return true
	}
	if _, ok := s.lines[findingLineKey(finding.Path, finding.Line)]; ok {
		return true
	}
	for _, key := range contentKeysAroundFinding(finding, diffs) {
		if _, ok := s.contents[key]; ok {
			return true
		}
	}
	return s.nearbySkippedLine(finding)
}

func (s skippedThreads) nearbySkippedLine(finding Finding) bool {
	if finding.Path == "" || finding.Line <= 0 {
		return false
	}
	nearby := 0
	titled := 0
	empty := 0
	for key := range s.lines {
		path, line, ok := splitFindingLineKey(key)
		if !ok || path != finding.Path {
			continue
		}
		if lineDistance(line, finding.Line) > neighborLineWindow {
			continue
		}
		nearby++
		title := s.titles[key]
		if title == "" {
			empty++
			continue
		}
		if titlesLikelySame(title, finding.Title) {
			titled++
		}
	}
	if titled > 0 {
		return true
	}
	if nearby == 0 {
		return false
	}
	if empty == 0 {
		return false
	}
	return nearby == 1
}

func splitFindingLineKey(key string) (path string, line int, ok bool) {
	i := strings.IndexByte(key, 0)
	if i <= 0 || i+1 >= len(key) {
		return "", 0, false
	}
	n, err := strconv.Atoi(key[i+1:])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return key[:i], n, true
}

func (s skippedThreads) hasDiscussion(discussion ownedDiscussion) bool {
	if _, ok := s.discussionIDs[discussion.ID]; ok {
		return true
	}
	if discussion.FindingID != "" {
		if _, ok := s.findingIDs[discussion.FindingID]; ok {
			return true
		}
	}
	if discussion.Path != "" && discussion.LineContentID != "" {
		if _, ok := s.contents[contentKey(discussion.Path, discussion.LineContentID)]; ok {
			return true
		}
	}
	return false
}

func (s skippedThreads) backfillFromDiffs(diffs []PreparedDiff) {
	for key := range s.lines {
		path, line, ok := splitFindingLineKey(key)
		if !ok {
			continue
		}
		id := lineContentIDFromDiffs(diffs, path, line)
		if id == "" {
			continue
		}
		content := contentKey(path, id)
		s.contents[content] = struct{}{}
		if title := s.titles[key]; title != "" {
			s.titlesByKey[content] = title
		}
	}
}

func (s skippedThreads) indexCurrentLines(diffs []PreparedDiff) {
	for key := range s.contents {
		path, id, ok := splitContentKey(key)
		if !ok {
			continue
		}
		prepared, found := findPreparedDiff(diffs, path)
		if !found || prepared.AddedLines == nil {
			continue
		}
		for line, text := range prepared.AddedLines {
			if lineContentID(text) == id {
				lineKey := findingLineKey(path, line)
				s.lines[lineKey] = struct{}{}
				if title := s.titlesByKey[key]; title != "" {
					s.titles[lineKey] = title
				}
			}
		}
	}
}

func splitContentKey(key string) (path, id string, ok bool) {
	i := strings.IndexByte(key, 0)
	if i <= 0 || i+1 >= len(key) {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

func collectSkippedThreads(discussions []gitlab.Discussion, aliases []string) skippedThreads {
	skipped := newSkippedThreads()
	for _, discussion := range discussions {
		if _, ok := discussionFindingID(discussion); !ok {
			continue
		}
		if discussionHasSkip(discussion, aliases) {
			skipped.add(discussion)
		}
	}
	return skipped
}

func discussionHasSkip(discussion gitlab.Discussion, aliases []string) bool {
	for _, note := range discussion.Notes {
		if ownedSkipNote(note.Body) {
			return true
		}
		if strings.Contains(note.Body, "Skipped as a false positive") || strings.Contains(note.Body, "Skipped after @mention") {
			return true
		}
	}
	return humanThreadCommand(discussion, aliases) == CommandSkip
}

func ownedSkipNote(body string) bool {
	return strings.Contains(body, skipMarker)
}

func (s skippedThreads) add(discussion gitlab.Discussion) {
	s.discussionIDs[discussion.ID] = struct{}{}
	title := discussionFindingTitle(discussion)
	if id, ok := discussionFindingID(discussion); ok {
		s.findingIDs[id] = struct{}{}
	}
	if path, line, ok := discussionLocation(discussion); ok {
		lineKey := findingLineKey(path, line)
		s.lines[lineKey] = struct{}{}
		if title != "" {
			s.titles[lineKey] = title
		}
	}
	if id, ok := discussionLineContentID(discussion); ok {
		if path, _, locOK := discussionLocation(discussion); locOK {
			key := contentKey(path, id)
			s.contents[key] = struct{}{}
			if title != "" {
				s.titlesByKey[key] = title
			}
		}
	}
}

func discussionLineContentID(discussion gitlab.Discussion) (string, bool) {
	for _, note := range discussion.Notes {
		if id, ok := parseLineContentID(note.Body); ok {
			return id, true
		}
	}
	return "", false
}

func (s *Service) threadCommandAliases() []string {
	if len(s.cfg.Review.MentionAliases) > 0 {
		return s.cfg.Review.MentionAliases
	}
	mention := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s.cfg.Review.Mention, "@")))
	if mention == "" {
		return nil
	}
	return []string{mention}
}

func (s *Service) applyThreadCommands(ctx context.Context, job Job, notices []string) []string {
	discussions, err := s.gitlab.ListDiscussions(ctx, job.ProjectID, job.MergeRequest)
	if err != nil {
		s.logger.Warn("failed to list discussions for thread commands", "request_id", job.RequestID, "error", err)
		return append(notices, "Existing inline discussions could not be listed, so thread skip commands were not applied.")
	}
	_, discussions, notices = s.applyThreadCommandsTo(ctx, job, discussions, notices)
	return s.applyTopLevelMentions(ctx, job, discussions, notices)
}

func (s *Service) applyTopLevelMentions(ctx context.Context, job Job, discussions []gitlab.Discussion, notices []string) []string {
	aliases := s.threadCommandAliases()
	for _, discussion := range discussions {
		if _, ok := discussionFindingID(discussion); ok {
			continue
		}
		req := lastHumanMention(discussion, aliases)
		switch req.Command {
		case CommandHelp:
			notices = s.replyHelpIfNeeded(ctx, job, discussion, notices)
		case CommandAsk:
			notices = s.replyAskIfNeeded(ctx, job, discussion, req.Extra, notices)
		case CommandResolve:
			if s.cfg.Target.DryRun {
				s.logger.Info("dry-run: would resolve owned finding threads after @mention", "request_id", job.RequestID)
				continue
			}
			n, err := s.ResolveOwnedFindings(ctx, job.ProjectID, job.MergeRequest)
			if err != nil {
				notices = append(notices, "Could not resolve CodeReview finding threads after @mention.")
				continue
			}
			if n > 0 {
				notices = append(notices, fmt.Sprintf("Resolved %d CodeReview finding thread(s) after @mention.", n))
			}
		case CommandReview, CommandFullReview:
			s.logger.Info("ignoring top-level review @mention in the one-shot CLI; use the webhook listener or --force", "request_id", job.RequestID, "command", req.Command.String())
		}
	}
	return notices
}

func lastHumanMention(discussion gitlab.Discussion, aliases []string) MentionRequest {
	for i := len(discussion.Notes) - 1; i >= 0; i-- {
		note := discussion.Notes[i]
		if gitlab.IsAutomationUser(note.Author) {
			continue
		}
		return ParseMentionRequest(note.Body, aliases)
	}
	return MentionRequest{}
}

func (s *Service) ResolveOwnedFindings(ctx context.Context, projectID, iid int64) (int, error) {
	discussions, err := s.gitlab.ListDiscussions(ctx, projectID, iid)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, discussion := range ownedInlineDiscussions(discussions, nil) {
		if !discussion.Unresolved {
			continue
		}
		if s.cfg.Target.DryRun {
			n++
			continue
		}
		if err := s.gitlab.ResolveDiscussion(ctx, projectID, iid, discussion.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Service) applyThreadCommandsTo(ctx context.Context, job Job, discussions []gitlab.Discussion, notices []string) (skippedThreads, []gitlab.Discussion, []string) {
	skipped := newSkippedThreads()
	aliases := s.threadCommandAliases()
	for i, discussion := range discussions {
		if !discussionUnresolved(discussion) {
			continue
		}
		if _, ok := discussionFindingID(discussion); !ok {
			continue
		}
		req := humanThreadMention(discussion, aliases)
		if req.Command == CommandHelp {
			notices = s.replyHelpIfNeeded(ctx, job, discussion, notices)
			continue
		}
		if req.Command == CommandAsk {
			notices = s.replyAskIfNeeded(ctx, job, discussion, req.Extra, notices)
			continue
		}
		if req.Command == CommandReview || req.Command == CommandFullReview || req.Command == CommandResolve {
			s.logger.Info("ignoring inline @mention that is not skip, help, or ask; use a merge-request comment for review/resolve", "request_id", job.RequestID, "discussion_id", discussion.ID, "command", req.Command.String())
			continue
		}
		if req.Command != CommandSkip {
			continue
		}
		if s.cfg.Target.DryRun {
			s.logger.Info("dry-run: would skip inline discussion after @mention", "request_id", job.RequestID, "discussion_id", discussion.ID)
			skipped.add(discussion)
			continue
		}
		if _, err := s.gitlab.ReplyToDiscussion(ctx, job.ProjectID, job.MergeRequest, discussion.ID, SkipReplyBody()); err != nil {
			s.logger.Warn("failed to reply to skip @mention", "request_id", job.RequestID, "discussion_id", discussion.ID, "error", err)
			notices = append(notices, "Could not skip an inline discussion after @mention; it remains open.")
			continue
		}
		if err := s.gitlab.ResolveDiscussion(ctx, job.ProjectID, job.MergeRequest, discussion.ID); err != nil {
			s.logger.Warn("failed to resolve skipped inline discussion", "request_id", job.RequestID, "discussion_id", discussion.ID, "error", err)
			notices = append(notices, "Replied to a skip @mention but could not resolve the inline discussion; it remains open.")
			continue
		}
		skipped.add(discussion)
		discussions[i] = markDiscussionResolved(discussion)
	}
	if n := skipped.count(); n > 0 && !s.cfg.Target.DryRun {
		notices = append(notices, fmt.Sprintf("Skipped %d inline discussion(s) as false positives after @mention.", n))
	}
	return skipped, discussions, notices
}

func humanThreadMention(discussion gitlab.Discussion, aliases []string) MentionRequest {
	firstFinding := -1
	for i, note := range discussion.Notes {
		if _, ok := parseFindingID(note.Body); ok {
			firstFinding = i
			break
		}
	}
	if firstFinding < 0 {
		return MentionRequest{}
	}
	var foundReview, foundAsk, foundHelp MentionRequest
	for _, note := range discussion.Notes[firstFinding+1:] {
		if gitlab.IsAutomationUser(note.Author) {
			continue
		}
		req := ParseMentionRequest(note.Body, aliases)
		switch req.Command {
		case CommandSkip:
			return req
		case CommandReview:
			foundReview = req
		case CommandAsk:
			foundAsk = req
		case CommandHelp:
			foundHelp = req
		}
	}
	if foundReview.Command != CommandNone {
		return foundReview
	}
	if foundAsk.Command != CommandNone {
		return foundAsk
	}
	if foundHelp.Command != CommandNone {
		return foundHelp
	}
	return MentionRequest{}
}

func humanThreadCommand(discussion gitlab.Discussion, aliases []string) ThreadCommand {
	return humanThreadMention(discussion, aliases).Command
}

func (s *Service) replyHelpIfNeeded(ctx context.Context, job Job, discussion gitlab.Discussion, notices []string) []string {
	if discussionHasHelpReply(discussion) {
		return notices
	}
	if s.cfg.Target.DryRun {
		s.logger.Info("dry-run: would post command list after @mention", "request_id", job.RequestID, "discussion_id", discussion.ID)
		return notices
	}
	if _, err := s.gitlab.ReplyToDiscussion(ctx, job.ProjectID, job.MergeRequest, discussion.ID, HelpMarkdown(s.cfg.Review.Mention)); err != nil {
		s.logger.Warn("failed to post command list after @mention", "request_id", job.RequestID, "discussion_id", discussion.ID, "error", err)
		return append(notices, "Could not post the CodeReview command list after @mention.")
	}
	return notices
}

func discussionHasHelpReply(discussion gitlab.Discussion) bool {
	for _, note := range discussion.Notes {
		if ownedHelpNote(note.Body) {
			return true
		}
	}
	return false
}

func markDiscussionResolved(discussion gitlab.Discussion) gitlab.Discussion {
	notes := append([]gitlab.Note(nil), discussion.Notes...)
	for i := range notes {
		if notes[i].Resolvable {
			notes[i].Resolved = true
		}
	}
	discussion.Notes = notes
	return discussion
}

// ThreadCommand is a user instruction found in an MR discussion note
// that @mentions CodeReview.
type ThreadCommand int

const (
	CommandNone ThreadCommand = iota
	CommandSkip
	CommandReview
	CommandFullReview
	CommandResolve
	CommandHelp
	CommandAsk
)

func (c ThreadCommand) String() string {
	switch c {
	case CommandSkip:
		return "skip"
	case CommandReview:
		return "review"
	case CommandFullReview:
		return "full-review"
	case CommandResolve:
		return "resolve"
	case CommandHelp:
		return "help"
	case CommandAsk:
		return "ask"
	default:
		return "none"
	}
}

// MentionRequest is a classified @mention plus leftover untrusted text.
type MentionRequest struct {
	Command ThreadCommand
	Extra   string
}

// NoteMentionsBot reports whether body contains @alias for any configured mention.
func NoteMentionsBot(body string, aliases []string) bool {
	haystack := strings.ToLower(body)
	for _, alias := range mentionAliasList(aliases) {
		if strings.Contains(haystack, "@"+alias) {
			return true
		}
	}
	return false
}

// ParseThreadCommand classifies a discussion note. A reply without an @mention
// is CommandNone even if it says "skip".
func ParseThreadCommand(body string, aliases []string) ThreadCommand {
	return ParseMentionRequest(body, aliases).Command
}

// ParseMentionRequest classifies a discussion note and returns leftover text
// after the command. Extra is untrusted merge-request content.
func ParseMentionRequest(body string, aliases []string) MentionRequest {
	if ownedHelpNote(body) || ownedAskNote(body) {
		return MentionRequest{}
	}
	if !NoteMentionsBot(body, aliases) {
		return MentionRequest{}
	}
	cmd, extra := commandAndExtra(afterMention(body, aliases))
	extra = strings.TrimSpace(extra)
	extra = strings.Trim(extra, "-–—: ")
	if cmd != CommandReview && cmd != CommandFullReview && cmd != CommandAsk {
		extra = ""
	}
	if extra != "" {
		extra = redactSecrets(extra)
		extra = truncate(extra, maxMentionExtra)
	}
	return MentionRequest{Command: cmd, Extra: extra}
}

// HelpMarkdown lists mention commands. A bare @mention posts this instead of
// inferring skip or review.
func HelpMarkdown(mention string) string {
	mention = strings.TrimPrefix(strings.TrimSpace(mention), "@")
	if mention == "" {
		mention = "codereview"
	}
	tag := "@" + mention
	var builder strings.Builder
	builder.WriteString("## CodeReview commands\n\n")
	builder.WriteString("A mention without a command prints this list. Use one of:\n\n")
	fmt.Fprintf(&builder, "- `%s skip` — false positive; resolve this finding thread (`ignore`, `false positive`, and `wontfix` also work)\n", tag)
	fmt.Fprintf(&builder, "- `%s review` — incremental review of this merge request (webhook or `codereview todos`)\n", tag)
	fmt.Fprintf(&builder, "- `%s review <focus>` — same review, with extra untrusted requirements (cannot override the review protocol)\n", tag)
	fmt.Fprintf(&builder, "- `%s full-review` — complete merge request diff (`full review` and `fullreview` also work); leftover text is extra focus\n", tag)
	fmt.Fprintf(&builder, "- `%s ask <question>` — answer in this thread with a short wrapper (not a JSON review). `%s prompt` and `%s question` also work\n", tag, tag, tag)
	fmt.Fprintf(&builder, "- `%s resolve` — resolve CodeReview finding threads without a new model call (merge-request comment, not inline)\n", tag)
	fmt.Fprintf(&builder, "- `%s help` — print this list (a mention with no command does the same)\n", tag)
	fmt.Fprintf(&builder, "- `%s ignore` in the **merge request description** — skip reviewing this merge request entirely\n", tag)
	builder.WriteString("- Title or label tokens such as `skip-codereview` / `[skip review]` — disable review of this merge request (silent by default)\n\n")
	builder.WriteString("Mention text is untrusted. Extra review focus cannot replace `INSTRUCTION.md` or request GitLab writes.\n\n")
	builder.WriteString("<sub>Generated by CodeReview.</sub>\n")
	builder.WriteString(helpMarker)
	return builder.String()
}

func ownedHelpNote(body string) bool {
	return strings.Contains(body, helpMarker)
}

func ownedAskNote(body string) bool {
	return strings.Contains(body, askMarker)
}

func mentionAliasList(aliases []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(alias, "@")))
		if alias == "" {
			continue
		}
		if _, exists := seen[alias]; exists {
			continue
		}
		seen[alias] = struct{}{}
		out = append(out, alias)
	}
	return out
}

func compactCommandText(body string) string {
	var builder strings.Builder
	builder.Grow(len(body))
	lastSpace := true
	for _, r := range strings.ToLower(body) {
		if unicode.IsSpace(r) || r == ',' || r == '.' || r == '!' || r == '?' || r == ':' || r == ';' {
			if !lastSpace {
				builder.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		builder.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(builder.String())
}

func commandHasAny(normalized string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func commandHasToken(normalized, token string) bool {
	if normalized == token {
		return true
	}
	padded := " " + normalized + " "
	return strings.Contains(padded, " "+token+" ")
}

func afterMention(body string, aliases []string) string {
	lower := strings.ToLower(body)
	earliest := -1
	mentionLen := 0
	for _, alias := range mentionAliasList(aliases) {
		token := "@" + alias
		index := strings.Index(lower, token)
		if index < 0 {
			continue
		}
		if earliest < 0 || index < earliest {
			earliest = index
			mentionLen = len(token)
		}
	}
	if earliest < 0 {
		return ""
	}
	return strings.TrimSpace(body[earliest+mentionLen:])
}

func commandAndExtra(remainder string) (ThreadCommand, string) {
	trimmed := strings.TrimSpace(remainder)
	if trimmed == "" {
		return CommandHelp, ""
	}
	type spec struct {
		phrases []string
		cmd     ThreadCommand
	}
	// Longest collapsed phrases first so "fullreview" wins over "review".
	for _, item := range []spec{
		{[]string{"false positive", "false-positive", "falsepositive", "won't fix", "wontfix"}, CommandSkip},
		{[]string{"full review", "full-review", "full_review", "fullreview"}, CommandFullReview},
		{[]string{"re-review", "rereview", "re_review"}, CommandReview},
		{[]string{"ask-question", "askquestion"}, CommandAsk},
		{[]string{"skip", "ignore"}, CommandSkip},
		{[]string{"resolve"}, CommandResolve},
		{[]string{"help"}, CommandHelp},
		{[]string{"question", "prompt", "ask"}, CommandAsk},
		{[]string{"review"}, CommandReview},
	} {
		for _, phrase := range item.phrases {
			if extra, ok := extraAfterCommandPrefix(trimmed, phrase); ok {
				return item.cmd, extra
			}
		}
	}
	normalized := compactCommandText(trimmed)
	if commandHasAny(normalized, "false positive", "false-positive", "wontfix", "won't fix") {
		return CommandSkip, ""
	}
	if commandHasToken(normalized, "skip") || commandHasToken(normalized, "ignore") {
		return CommandSkip, ""
	}
	return CommandHelp, ""
}

func extraAfterCommandPrefix(remainder, phrase string) (string, bool) {
	original := strings.TrimSpace(remainder)
	lower := strings.ToLower(original)
	if strings.HasPrefix(lower, "please ") {
		lower = strings.TrimSpace(lower[len("please "):])
		original = strings.TrimSpace(original[len("please "):])
	}
	want := collapseCommandToken(phrase)
	if want == "" {
		return "", false
	}
	matched := 0
	consumed := 0
	for i, r := range lower {
		if matched == len(want) {
			if commandBoundary(r) {
				return strings.TrimSpace(original[i:]), true
			}
			return "", false
		}
		if r == ' ' || r == '-' || r == '_' || r == '\'' {
			continue
		}
		if r != rune(want[matched]) {
			return "", false
		}
		matched++
		consumed = i + len(string(r))
	}
	if matched == len(want) {
		return strings.TrimSpace(original[consumed:]), true
	}
	return "", false
}

func collapseCommandToken(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range strings.ToLower(value) {
		if unicode.IsSpace(r) || r == '-' || r == '_' || r == '\'' {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func commandBoundary(r rune) bool {
	return unicode.IsSpace(r) || r == ',' || r == '.' || r == '!' || r == '?' || r == ':' || r == ';' || r == '-'
}
