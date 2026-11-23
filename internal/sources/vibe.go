package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vshulcz/deja-vu/internal/model"
)

// Mistral Vibe (mistralai/mistral-vibe) writes every session under
// $VIBE_HOME/logs/session, normally ~/.vibe/logs/session, in two layouts
// depending on the engine.
//
// The unified engine (the default since 2.26) keeps one event-sourced store
// per session, laid out under unified/<session_id>/ as
//
//	CURRENT                     JSON pointer to the live generation
//	meta.json                   SessionMetadata (cwd, title, clock, lineage)
//	generations/<gen>/manifest.json   files and first journal sequence
//	generations/<gen>/projection-state.json  the projection snapshot
//	chunks/<sha256>.json         pooled entries of the snapshot's history
//	journal/<seq>.jsonl          projection deltas after the snapshot
//
// CURRENT names the generation; that generation's manifest lists the projection
// state document, whose snapshot.history.entries hold the history. A document
// whose transcript the pool holds names, in its place, the ordered chunks whose
// concatenation refills it — each chunk is one JSON array of entries. The
// journal then appends, replaces or removes entries by id until the next
// generation. An entry is a message (role user or assistant, content blocks),
// a piece of reasoning, or an effect — a tool call with its state and display.
// Sub-agents leave no history of their own here: the lead's transcript holds
// what was asked and answered.
//
// The earlier engine writes <session_prefix>_<date>_<time>_<id>/ (prefix
// "session" by default) with a messages.jsonl — one {"role","content"} message
// per line — beside the same meta.json shape. A sub-agent is a session of its
// own under agents/ inside it, which this reader skips.
//
// Read off mistralai/mistral-vibe 3.1.0 (_storage.py, session_index.py,
// session_loader.py, cli-rust/src/server/types.rs): the store formats are
// undocumented and versioned by a minor number a newer writer may raise.

// VibeHome honors VIBE_HOME, the home override the CLI itself implements.
func VibeHome() string {
	if h := strings.TrimSpace(os.Getenv("VIBE_HOME")); h != "" {
		return filepath.Clean(h)
	}
	return filepath.Join(Home(), ".vibe")
}

// VibeRoot is the session store, overridable via DEJA_VIBE_ROOT without
// affecting where Vibe itself writes.
func VibeRoot() string {
	return EnvPath("DEJA_VIBE_ROOT", filepath.Join(VibeHome(), "logs", "session"))
}

// VibeRoots returns VibeRoot() followed by any readable ~/.vibe/logs/session
// stores under DEJA_HOME_ROOTS when DEJA_VIBE_ROOT is not pinned.
func VibeRoots() []string {
	roots := []string{VibeRoot()}
	if os.Getenv("DEJA_VIBE_ROOT") != "" {
		return roots
	}
	seen := map[string]bool{filepath.Clean(roots[0]): true}
	for _, h := range extraHomeRoots() {
		r := filepath.Join(h, ".vibe", "logs", "session")
		if !seen[filepath.Clean(r)] {
			if _, err := os.ReadDir(r); err == nil {
				roots = append(roots, r)
			}
		}
	}
	return roots
}

func underVibeRoot(p string) bool {
	cleanP := filepath.Clean(p)
	for _, r := range VibeRoots() {
		if r != "" && strings.HasPrefix(cleanP, filepath.Clean(r)+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func vibeInAgentsDir(p string) bool {
	return filepath.Base(filepath.Dir(filepath.Dir(filepath.Clean(p)))) == "agents"
}

// IsVibeLegacyTranscript reports whether p is a legacy engine messages.jsonl
// under a known Vibe session store.
func IsVibeLegacyTranscript(p string) bool {
	if !hasBase(p, "messages.jsonl") || !underVibeRoot(p) || vibeInAgentsDir(p) {
		return false
	}
	if filepath.Base(filepath.Dir(filepath.Clean(p))) == "unified" || strings.HasSuffix(filepath.Dir(filepath.Clean(p)), string(filepath.Separator)+"unified") {
		return false
	}
	return fileExists(filepath.Join(filepath.Dir(p), "meta.json"))
}

// IsVibeUnifiedCurrent reports whether p is a unified engine session CURRENT
// pointer under a known Vibe session store.
func IsVibeUnifiedCurrent(p string) bool {
	return hasBase(p, "CURRENT") &&
		filepath.Base(filepath.Dir(filepath.Dir(filepath.Clean(p)))) == "unified" &&
		underVibeRoot(p)
}

// VibeSessionFiles lists the transcripts to index: every legacy messages.jsonl
// and every unified CURRENT pointer, in both cases skipping the agents/ trees
// a sub-agent of the legacy engine keeps.
func VibeSessionFiles() []string {
	var out []string
	for _, root := range VibeRoots() {
		out = append(out, walkFiles(root, func(p string) bool {
			return (IsVibeLegacyTranscript(p) || IsVibeUnifiedCurrent(p)) && !vibeInAgentsDir(p)
		})...)
	}
	return out
}

// LoadVibe reads every Vibe session on this machine.
func LoadVibe() []model.Session {
	return parseFiles(VibeSessionFiles(), ParseVibeFile)
}

// ParseVibeFile parses one Vibe transcript: a unified CURRENT pointer or a
// legacy messages.jsonl. Both parses are shaped by the file, not the store
// root: the kind's Match already holds the root, and a fixture or a hostile
// copy of a single file parses without one.
func ParseVibeFile(path string) ([]model.Session, error) {
	if hasBase(path, "CURRENT") {
		return ParseVibeUnified(path)
	}
	return parseVibeLegacy(path, 0)
}

// ParseVibeFileFromOffset parses a legacy messages.jsonl incrementally. The
// unified store is rewritten generation by generation, so it has no tail to
// resume and is read whole.
func ParseVibeFileFromOffset(path string, offset int64) ([]model.Session, error) {
	return parseVibeLegacy(path, offset)
}

// VibeSessionDir returns the working directory a session's meta.json records,
// for callers that need it to resume or hand off.
func VibeSessionDir(path string) string {
	dir := filepath.Dir(path)
	meta, ok := readVibeMeta(filepath.Join(dir, "meta.json"))
	if !ok {
		return ""
	}
	if cwd := meta.workDir(); cwd != "" {
		return cwd
	}
	return meta.OriginDirectory
}

type vibeMeta struct {
	SessionID       string         `json:"session_id"`
	ParentSessionID string         `json:"parent_session_id"`
	OriginDirectory string         `json:"origin_directory"`
	Title           string         `json:"title"`
	StartTime       any            `json:"start_time"`
	EndTime         any            `json:"end_time"`
	BumpedAt        any            `json:"bumped_at"`
	Environment     map[string]any `json:"environment"`
}

func (m vibeMeta) workDir() string {
	if m.Environment == nil {
		return ""
	}
	cwd, _ := m.Environment["working_directory"].(string)
	return strings.TrimSpace(cwd)
}

func readVibeMeta(path string) (vibeMeta, bool) {
	var meta vibeMeta
	b, err := readJSONFile(path)
	if err != nil {
		return meta, false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&meta); err != nil {
		return meta, false
	}
	return meta, true
}

// vibeBaseSession assembles the parts every Vibe session shares: id, lineage
// and clock from meta.json, project from the directory the session ran in.
func vibeBaseSession(path string, meta vibeMeta, hasMeta bool, fallbackID string) model.Session {
	sessionDir := filepath.Dir(path)
	id := fallbackID
	if id == "" && sessionDir != "" {
		id = filepath.Base(sessionDir)
	}
	s := model.Session{
		Harness: "vibe",
		ID:      id,
		Path:    path,
	}
	if hasMeta {
		if id := strings.TrimSpace(meta.SessionID); id != "" {
			s.ID = id
		}
		if parent := strings.TrimSpace(meta.ParentSessionID); parent != "" && parent != s.ID {
			s.Kind = model.KindSubagent
			s.Parent = parent
		}
		s.Touch(parseTimeAny(meta.StartTime))
		s.Touch(parseTimeAny(meta.EndTime))
		s.Touch(parseTimeAny(meta.BumpedAt))
		if title := strings.TrimSpace(meta.Title); title != "" && s.Kind != model.KindSubagent {
			s.Title = title
		}
		if cwd := meta.workDir(); cwd != "" {
			s.Project = projectName(cwd)
		} else if s.Project == "" && strings.TrimSpace(meta.OriginDirectory) != "" {
			s.Project = projectName(strings.TrimSpace(meta.OriginDirectory))
		}
	}
	if s.Updated.IsZero() {
		if fi, err := os.Stat(path); err == nil {
			s.Touch(fi.ModTime())
		}
	}
	return s
}

// addVibeMessages appends the conversational messages of one parsed turn.
func squeezeVibeText(v string) string { return strings.TrimSpace(v) }

func parseVibeLegacy(path string, offset int64) ([]model.Session, error) {
	sessionDir := filepath.Dir(path)
	meta, hasMeta := readVibeMeta(filepath.Join(sessionDir, "meta.json"))
	s := vibeBaseSession(path, meta, hasMeta, "")
	err := scanJSONLFromOffset(path, offset, func(m map[string]any) {
		role, _ := m["role"].(string)
		text := strings.TrimSpace(textFromContent(m["content"]))
		if text == "" {
			return
		}
		t := parseTimeAny(m["timestamp"])
		if t.IsZero() {
			t = parseTimeAny(m["created_at"])
		}
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "user":
			s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Text: text, Time: t})
			if s.Title == "" && s.Kind != model.KindSubagent {
				s.Title = squeezeVibeText(vibeFirstLine(text))
			}
		case "assistant", "model", "agent":
			s.Messages = append(s.Messages, model.Message{Role: model.RoleAssistant, Text: text, Time: t})
		}
		s.Touch(t)
	})
	if len(s.Messages) == 0 {
		return nil, err
	}
	vibeSpreadMessageTimes(&s)
	if s.Title == "" {
		for _, m := range s.Messages {
			if m.Role == model.RoleAssistant {
				s.Title = squeezeVibeText(vibeFirstLine(m.Text))
				s.AgentTitle = true
				break
			}
		}
	}
	return []model.Session{s}, err
}

// vibeSpreadMessageTimes gives the turns a clock the transcript never wrote.
// Neither engine stamps the messages themselves: the legacy file is saved at
// the end of each step and the unified entries carry no time of their own,
// so read afterwards the events are spread over the session's own span.
func vibeSpreadMessageTimes(s *model.Session) {
	start := s.Started
	end := s.Updated
	if end.IsZero() || !start.Before(end) {
		t := end
		if t.IsZero() {
			t = start
		}
		for i := range s.Messages {
			if s.Messages[i].Time.IsZero() {
				s.Messages[i].Time = t
			}
		}
		return
	}
	span := end.Sub(start)
	vibeSpreadMessageTimesSpan(s, start, span)
}

func vibeSpreadMessageTimesSpan(s *model.Session, start time.Time, span time.Duration) {
	n := len(s.Messages)
	for i := range s.Messages {
		if !s.Messages[i].Time.IsZero() {
			continue
		}
		s.Messages[i].Time = start.Add(span * time.Duration(i+1) / time.Duration(n+1))
	}
}

func vibeFirstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

// ParseVibeUnified replays one unified session store: the CURRENT pointer's
// generation, its projection snapshot (with the chunk pool the snapshot's
// history is held in), and the journal deltas written after the snapshot.
func ParseVibeUnified(path string) ([]model.Session, error) {
	sessionDir := filepath.Dir(filepath.Clean(path))
	meta, hasMeta := readVibeMeta(filepath.Join(sessionDir, "meta.json"))

	currentBytes, err := readJSONFile(path)
	if err != nil {
		return nil, err
	}
	current, err := decodeJSONMap(currentBytes)
	if err != nil {
		return nil, fmt.Errorf("decode Vibe CURRENT %s: %w", path, err)
	}
	id, _ := current["session_id"].(string)
	generation, _ := current["generation"].(string)
	if id != "" {
		meta.SessionID = id
		hasMeta = true
	}
	s := vibeBaseSession(path, meta, hasMeta, sessionDirName(path))

	genDir := filepath.Join(sessionDir, "generations", generation)
	manifestBytes, err := readJSONFile(filepath.Join(genDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read Vibe generation manifest %s: %w", genDir, err)
	}
	manifest, err := decodeJSONMap(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("decode Vibe generation manifest %s: %w", genDir, err)
	}
	snapshotSequence := int64Of(manifest["snapshot_sequence"])

	entries, err := vibeSnapshotEntries(genDir, manifest)
	if err != nil {
		return nil, err
	}
	entries, err = vibeReplayJournal(sessionDir, manifest, snapshotSequence, entries)
	if err != nil {
		return nil, err
	}
	vibeEntriesToMessages(&s, entries)
	if len(s.Messages) == 0 {
		return nil, nil
	}
	vibeSpreadMessageTimes(&s)
	if s.Title == "" {
		for _, m := range s.Messages {
			if m.Role == model.RoleAssistant {
				s.Title = squeezeVibeText(vibeFirstLine(m.Text))
				s.AgentTitle = true
				break
			}
		}
	}
	return []model.Session{s}, nil
}

func sessionDirName(path string) string {
	return filepath.Base(filepath.Dir(filepath.Clean(path)))
}

func decodeJSONMap(b []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// vibeSnapshotEntries reads the projection snapshot's history entries,
// refilling the chunk pool when the snapshot's transcript was detached into it.
func vibeSnapshotEntries(genDir string, manifest map[string]any) ([]any, error) {
	doc, ok := manifest["projection_state"].(map[string]any)
	if !ok {
		return nil, nil
	}
	name, _ := doc["path"].(string)
	chunks, _ := doc["chunks"].([]any)
	if name == "" {
		return nil, nil
	}
	body, err := readJSONFile(filepath.Join(genDir, name))
	if err != nil {
		return nil, fmt.Errorf("read Vibe projection state %s: %w", name, err)
	}
	envelope, err := decodeJSONMap(body)
	if err != nil {
		return nil, fmt.Errorf("decode Vibe projection state %s: %w", name, err)
	}
	entries := envelopeEntries(envelope)
	if len(chunks) == 0 {
		return entries, nil
	}
	full := make([]any, 0, len(entries))
	full = append(full, entries...)
	for _, c := range chunks {
		digest, _ := c.(string)
		if digest == "" {
			continue
		}
		chunkBody, err := readJSONFile(filepath.Join(genDir, "chunks", digest+".json"))
		if err != nil {
			return nil, fmt.Errorf("read Vibe chunk %s: %w", digest, err)
		}
		d := json.NewDecoder(bytes.NewReader(chunkBody))
		d.UseNumber()
		var arr []any
		if err := d.Decode(&arr); err != nil {
			return nil, fmt.Errorf("decode Vibe chunk %s: %w", digest, err)
		}
		full = append(full, arr...)
	}
	return full, nil
}

func envelopeEntries(envelope map[string]any) []any {
	snapshot, _ := envelope["snapshot"].(map[string]any)
	if snapshot == nil {
		return nil
	}
	history, _ := snapshot["history"].(map[string]any)
	if history == nil {
		return nil
	}
	entries, _ := history["entries"].([]any)
	return entries
}

// vibeReplayJournal folds the journal records written after the snapshot over
// the snapshot's entries, in sequence order. A record type deja does not read
// carries no entry ops and is skipped; only the projection deltas matter here.
func vibeReplayJournal(sessionDir string, manifest map[string]any, snapshotSequence int64, entries []any) ([]any, error) {
	segment, _ := manifest["recovery_journal_segment"].(map[string]any)
	journalPath, _ := segment["path"].(string)
	var files []string
	if journalPath != "" {
		files = []string{filepath.Join(sessionDir, filepath.FromSlash(journalPath))}
	} else {
		files = walkFiles(filepath.Join(sessionDir, "journal"), func(p string) bool {
			return strings.HasSuffix(p, ".jsonl")
		})
	}
	replay := func(m map[string]any) {
		var delta any
		switch typ, _ := m["type"].(string); typ {
		case "projection_delta":
			if payload, ok := m["payload"].(map[string]any); ok {
				delta = payload["delta"]
			}
		case "session_relocated":
			if payload, ok := m["payload"].(map[string]any); ok {
				delta = payload["delta"]
			}
		}
		ops, ok := delta.([]any)
		if !ok {
			return
		}
		for _, op := range ops {
			o, ok := op.(map[string]any)
			if !ok {
				continue
			}
			switch kind, _ := o["op"].(string); kind {
			case "append_entry":
				if entry, ok := o["entry"]; ok {
					entries = append(entries, entry)
				}
			case "replace_entry":
				id, _ := o["id"].(string)
				if entry, ok := o["entry"]; ok && id != "" {
					for i, cur := range entries {
						if cm, ok := cur.(map[string]any); ok {
							if cid, _ := cm["id"].(string); cid == id {
								entries[i] = entry
							}
						}
					}
				}
			case "remove_entry":
				id, _ := o["id"].(string)
				if id == "" {
					continue
				}
				kept := entries[:0]
				for _, cur := range entries {
					if cm, ok := cur.(map[string]any); ok {
						if cid, _ := cm["id"].(string); cid == id {
							continue
						}
					}
					kept = append(kept, cur)
				}
				entries = kept
			case "set_history_entries":
				if full, ok := o["entries"].([]any); ok {
					entries = append(entries[:0], full...)
				}
			}
		}
	}
	for _, f := range files {
		_ = scanJSONL(f, func(m map[string]any) {
			if int64Of(m["sequence"]) <= snapshotSequence {
				return
			}
			replay(m)
		})
	}
	return entries, nil
}

// vibeEntriesToMessages turns the replayed public history entries into
// messages. Speech is the message entries; the reasoning the model did out
// loud stays out of the transcript the way it does in every other harness
// here, and an effect is recorded as the tool call it is, labelled by the
// display Vibe itself shows for it.
func vibeEntriesToMessages(s *model.Session, entries []any) {
	for _, it := range entries {
		entry, ok := it.(map[string]any)
		if !ok {
			continue
		}
		t := parseTimeAny(entry["createdAt"])
		if t.IsZero() {
			t = parseTimeAny(entry["created_at"])
		}
		s.Touch(t)
		switch typ, _ := entry["type"].(string); typ {
		case "message":
			role, _ := entry["role"].(string)
			text := strings.TrimSpace(textFromContent(entry["content"]))
			if text == "" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(role)) {
			case "user":
				s.Messages = append(s.Messages, model.Message{Role: model.RoleUser, Text: text, Time: t})
				if s.Title == "" && s.Kind != model.KindSubagent {
					s.Title = squeezeVibeText(vibeFirstLine(text))
				}
			default:
				// An entry with no role is the assistant's: the wire shape
				// marks only the user's turns explicitly.
				s.Messages = append(s.Messages, model.Message{Role: model.RoleAssistant, Text: text, Time: t})
			}
		case "effect":
			vibeEffectToMessage(s, entry, t)
		}
	}
}

func vibeEffectToMessage(s *model.Session, entry map[string]any, t time.Time) {
	display := vibeEffectDisplay(entry)
	if display == nil {
		return
	}
	verb, _ := display["settledVerb"].(string)
	if verb == "" {
		verb, _ = display["verb"].(string)
	}
	message, _ := display["settledMessage"].(string)
	if message == "" {
		message, _ = display["message"].(string)
	}
	verb = strings.TrimSpace(verb)
	message = strings.TrimSpace(message)
	if verb == "" && message == "" {
		return
	}
	failed := false
	if ok, isBool := display["success"].(bool); isBool {
		failed = !ok
	}
	s.AddToolCall(verb, failed)
	if !IndexToolOutput() {
		return
	}
	text := message
	if verb != "" {
		text = verb + ": " + message
	}
	if suffix, _ := display["suffix"].(string); strings.TrimSpace(suffix) != "" {
		text = text + "  → " + strings.TrimSpace(suffix)
	}
	s.Messages = append(s.Messages, model.Message{Role: model.RoleToolOutput, Text: strings.TrimSpace(text), Time: t})
}

func vibeEffectDisplay(entry map[string]any) map[string]any {
	if state, ok := entry["state"].(map[string]any); ok {
		if d, ok := state["display"].(map[string]any); ok {
			return d
		}
	}
	if detail, ok := entry["detail"].(map[string]any); ok {
		if d, ok := detail["display"].(map[string]any); ok {
			return d
		}
	}
	return nil
}
