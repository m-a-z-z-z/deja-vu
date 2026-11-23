package sources

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vshulcz/deja-vu/internal/model"
)

func vibeWriteFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVibeRootsAndDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("VIBE_HOME", "")
	t.Setenv("DEJA_VIBE_ROOT", "")

	wantDefault := filepath.Join(home, ".vibe")
	if got := VibeHome(); got != wantDefault {
		t.Fatalf("VibeHome() = %q, want %q", got, wantDefault)
	}
	if got := VibeRoot(); got != filepath.Join(wantDefault, "logs", "session") {
		t.Fatalf("VibeRoot() = %q", got)
	}

	customHome := filepath.Join(t.TempDir(), "custom-vibe")
	t.Setenv("VIBE_HOME", customHome)
	if got := VibeRoot(); got != filepath.Join(customHome, "logs", "session") {
		t.Fatalf("VibeRoot() with VIBE_HOME = %q, want %q", got, filepath.Join(customHome, "logs", "session"))
	}
	t.Setenv("VIBE_HOME", "")

	root := t.TempDir()
	t.Setenv("DEJA_VIBE_ROOT", root)

	legacy := filepath.Join(root, "session_20261015_101500_abc123")
	legacySub := filepath.Join(root, "session_20261015_113000_def456", "agents", "child_1")
	stray := filepath.Join(root, "unrelated")
	for _, d := range []string{legacy, legacySub, stray} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacyMsg := vibeWriteFile(t, filepath.Join(legacy, "messages.jsonl"), "{\"role\":\"user\",\"content\":\"hi\"}\n")
	vibeWriteFile(t, filepath.Join(legacy, "meta.json"), `{"session_id":"abc123"}`)
	subMsg := vibeWriteFile(t, filepath.Join(legacySub, "messages.jsonl"), "{\"role\":\"user\",\"content\":\"sub\"}\n")
	vibeWriteFile(t, filepath.Join(legacySub, "meta.json"), `{"session_id":"child_1"}`)
	strayMsg := vibeWriteFile(t, filepath.Join(stray, "messages.jsonl"), "{\"role\":\"user\",\"content\":\"stray\"}\n")

	unified := filepath.Join(root, "unified", "sess01")
	if err := os.MkdirAll(unified, 0o755); err != nil {
		t.Fatal(err)
	}
	unifiedCurrent := vibeWriteFile(t, filepath.Join(unified, "CURRENT"), `{"session_id":"sess01","generation":"0000000000000001","snapshot_sequence":0}`)

	files := VibeSessionFiles()
	if !slices.Contains(files, legacyMsg) {
		t.Fatalf("VibeSessionFiles() = %v, missing legacy transcript %q", files, legacyMsg)
	}
	if !slices.Contains(files, unifiedCurrent) {
		t.Fatalf("VibeSessionFiles() = %v, missing unified CURRENT %q", files, unifiedCurrent)
	}
	if slices.Contains(files, subMsg) {
		t.Fatalf("VibeSessionFiles() = %v, should skip legacy sub-agent trees", files)
	}
	if slices.Contains(files, strayMsg) {
		t.Fatalf("VibeSessionFiles() = %v, a messages.jsonl with no meta.json beside it is not a session", files)
	}

	if !IsVibeLegacyTranscript(legacyMsg) || IsVibeLegacyTranscript(strayMsg) || IsVibeLegacyTranscript(unifiedCurrent) {
		t.Fatalf("IsVibeLegacyTranscript misclassified %v", files)
	}
	if !IsVibeUnifiedCurrent(unifiedCurrent) || IsVibeUnifiedCurrent(legacyMsg) {
		t.Fatal("IsVibeUnifiedCurrent misclassified")
	}
}

func TestParseVibeLegacy(t *testing.T) {
	t.Setenv("DEJA_VIBE_ROOT", t.TempDir())
	root := VibeRoot()
	dir := filepath.Join(root, "session_20261015_101500_abc123")
	vibeWriteFile(t, filepath.Join(dir, "meta.json"), `{
  "session_id": "abc123",
  "title": "Fix retry loop",
  "start_time": "2026-10-15T10:15:00Z",
  "end_time": "2026-10-15T10:40:00Z",
  "environment": {"working_directory": "/Users/alex/payments-api"}
}`)
	msgPath := vibeWriteFile(t, filepath.Join(dir, "messages.jsonl"),
		`{"role":"user","content":"plist content is a string"}`+"\n"+
			`{"role":"assistant","content":[{"type":"text","text":"on it"},{"type":"text","text":"lack"}]}`+"\n")

	ss, err := ParseVibeFile(msgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("got %d sessions, want 1", len(ss))
	}
	s := ss[0]
	if s.Harness != "vibe" || s.ID != "abc123" {
		t.Fatalf("session = %s:%s, want vibe:abc123", s.Harness, s.ID)
	}
	if s.Title != "Fix retry loop" {
		t.Fatalf("title = %q, want meta title", s.Title)
	}
	if s.Project != projectName("/Users/alex/payments-api") {
		t.Fatalf("project = %q, want %q", s.Project, projectName("/Users/alex/payments-api"))
	}
	if !s.Started.Equal(parseTimeAny("2026-10-15T10:15:00Z")) {
		t.Fatalf("started = %v, want 10:15", s.Started)
	}
	if !s.Updated.Equal(parseTimeAny("2026-10-15T10:40:00Z")) {
		t.Fatalf("updated = %v, want 10:40", s.Updated)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(s.Messages))
	}
	if s.Messages[0].Role != model.RoleUser || s.Messages[0].Text != "plist content is a string" {
		t.Fatalf("first message = %s %q", s.Messages[0].Role, s.Messages[0].Text)
	}
	if s.Messages[1].Role != model.RoleAssistant || s.Messages[1].Text != "on it\nlack" {
		t.Fatalf("second message = %s %q", s.Messages[1].Role, s.Messages[1].Text)
	}
}

func TestParseVibeLegacyTitleFromFirstUserLine(t *testing.T) {
	t.Setenv("DEJA_VIBE_ROOT", t.TempDir())
	root := VibeRoot()
	dir := filepath.Join(root, "session_20261015_120000_zzz999")
	vibeWriteFile(t, filepath.Join(dir, "meta.json"), `{"session_id":"zzz999","environment":{"working_directory":"/Users/alex/shop-api"},"start_time":"2026-10-15T12:00:00Z"}`)
	msgPath := vibeWriteFile(t, filepath.Join(dir, "messages.jsonl"),
		`{"role":"user","content":"fix the jwt refresh\nand keep the tests"}`+"\n"+`{"role":"assistant","content":"done"}`+"\n")

	ss, err := ParseVibeFile(msgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("got %d sessions, want 1", len(ss))
	}
	if ss[0].Title != "fix the jwt refresh" || ss[0].AgentTitle {
		t.Fatalf("title = %q agent_title=%v, want first user line", ss[0].Title, ss[0].AgentTitle)
	}
	if ss[0].ID != "zzz999" || ss[0].Project != projectName("/Users/alex/shop-api") {
		t.Fatalf("id=%q project=%q", ss[0].ID, ss[0].Project)
	}
}

func TestParseVibeUnified(t *testing.T) {
	t.Setenv("DEJA_VIBE_ROOT", t.TempDir())
	if err := os.MkdirAll(VibeRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	root := VibeRoot()
	sess := filepath.Join(root, "unified", "sess01vv")
	gen := filepath.Join(sess, "generations", "0000000000000001")
	journalDir := filepath.Join(sess, "journal")
	chunksDir := filepath.Join(gen, "chunks")
	for _, d := range []string{gen, journalDir, chunksDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	vibeWriteFile(t, filepath.Join(sess, "meta.json"), `{
  "session_id": "sess01vv",
  "start_time": "2026-10-16T09:00:00Z",
  "end_time": "2026-10-16T09:30:00Z",
  "environment": {"working_directory": "/Users/alex/deja-vu"},
  "title": "Add a harness"
}`)
	vibeWriteFile(t, filepath.Join(sess, "CURRENT"), `{"store_format":"mistral.vibe.unified-session-store/v1","session_id":"sess01vv","generation":"0000000000000001","snapshot_sequence":2,"manifest_sha256":"deadbeef"}`)

	// The snapshot holds its history in the chunk pool: an envelope whose
	// entries list is empty beside two ordered chunks.
	vibeWriteFile(t, filepath.Join(gen, "projection-state.json"), `{
  "projection_state_version": 1,
  "session_id": "sess01vv",
  "snapshot_sequence": 2,
  "snapshot": {"history": {"entries": []}}
}`)
	vibeWriteFile(t, filepath.Join(chunksDir, "aa11.json"), `[
  {"id":"e1","type":"message","role":"user","content":[{"type":"text","text":"add a vibe harness"}]},
  {"id":"e2","type":"reasoning","text":"thinking out loud"}
]`)
	vibeWriteFile(t, filepath.Join(chunksDir, "bb22.json"), `[
  {"id":"e3","type":"message","content":[{"type":"text","text":"on it"}]}
]`)
	vibeWriteFile(t, filepath.Join(gen, "manifest.json"), `{
  "manifest_version": 1,
  "session_id": "sess01vv",
  "generation": "0000000000000001",
  "created_at": "2026-10-16T09:10:00Z",
  "snapshot_sequence": 2,
  "execution_state": "recoverable",
  "checkpoint": {"path": "checkpoint.json", "sha256": "0f0f0f"},
  "runtime_state": {"path": "runtime-state.json", "sha256": "1e1e1e"},
  "projection_state": {"path": "projection-state.json", "sha256": "2d2d2d", "chunks": ["aa11", "bb22"]},
  "recovery_journal_segment": {"path": "journal/0000000000000003.jsonl", "first_sequence": 3}
}`)
	vibeWriteFile(t, filepath.Join(journalDir, "0000000000000003.jsonl"),
		`{"recovery_journal_record_version":1,"sequence":1,"previous_record_sha256":null,"record_sha256":"x"}`+"\n"+
			`{"recovery_journal_record_version":1,"sequence":3,"type":"projection_delta","payload":{"watermark":3,"delta":[{"op":"append_entry","entry":{"id":"e4","type":"effect","state":{"display":{"verb":"Run","settledVerb":"Ran","message":"go test ./...","settledMessage":"ok","success":true}}}}]}}`+"\n"+
			`{"recovery_journal_record_version":1,"sequence":4,"type":"projection_delta","payload":{"watermark":4,"delta":[{"op":"remove_entry","id":"e4"},{"op":"append_entry","entry":{"id":"e5","type":"effect","state":{"display":{"verb":"Run","settledVerb":"Ran","settledMessage":"go test ./... → ok","success":true}}}}]}}`+"\n"+
			`{"recovery_journal_record_version":1,"sequence":5,"type":"projection_delta","payload":{"watermark":5,"delta":[{"op":"append_entry","entry":{"id":"e6","type":"message","content":[{"type":"text","text":"done"}]}}]}}`+"\n")

	ss, err := ParseVibeUnified(filepath.Join(sess, "CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("got %d sessions, want 1", len(ss))
	}
	s := ss[0]
	if s.Harness != "vibe" || s.ID != "sess01vv" {
		t.Fatalf("session = %s:%s, want vibe:sess01vv", s.Harness, s.ID)
	}
	if s.Title != "Add a harness" {
		t.Fatalf("title = %q, want meta title", s.Title)
	}
	if s.Project != projectName("/Users/alex/deja-vu") {
		t.Fatalf("project = %q, want deja-vu", s.Project)
	}
	var roles []string
	for _, m := range s.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,tool-output,assistant" {
		t.Fatalf("roles = %v, want user,assistant,tool,assistant", roles)
	}
	if s.Messages[2].Text != "Ran: go test ./... → ok" {
		t.Fatalf("effect text = %q", s.Messages[2].Text)
	}
	if s.Messages[3].Text != "done" {
		t.Fatalf("last message = %q", s.Messages[3].Text)
	}
}
