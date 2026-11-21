package webfront

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type teamWrite int

const (
	teamWriteRefused teamWrite = iota
	teamWriteOwn
	teamWritePropose
)

var errTeamWriteRefused = errors.New("This run cannot change team memory or team skills: it runs for a workflow or with no signed-in user. An admin changes them in Settings, Approval rules.")

// runTeamWrite says where a run's write to memory or skills goes. A
// member's run writes the member's own layer. An admin's chat run proposes
// the change for an admin to confirm. A workflow run of an admin, a run
// with no user and a job with no run write nothing that other people load.
func (a *API) runTeamWrite(ctx context.Context, orgID string, runID *string) (teamWrite, *string, error) {
	if runID == nil || *runID == "" {
		return teamWriteRefused, nil, nil
	}
	var userID, role *string
	var scheduled bool
	err := a.DB.QueryRow(ctx, `select actor_user_id, actor_role, exists (select 1 from workflow_run w where w.work_run_id = r.id)
		from work_run r where r.id::text = $1 and r.org_id = $2`, *runID, orgID).Scan(&userID, &role, &scheduled)
	if errors.Is(err, pgx.ErrNoRows) {
		return teamWriteRefused, nil, nil
	}
	if err != nil {
		return teamWriteRefused, nil, err
	}
	switch {
	case deref(userID) == "":
		return teamWriteRefused, nil, nil
	case deref(role) != "admin":
		return teamWriteOwn, userID, nil
	case scheduled:
		return teamWriteRefused, userID, nil
	}
	return teamWritePropose, userID, nil
}

// teamChangeProposed is the answer to a write that now waits for an admin.
type teamChangeProposed struct{ ID string }

func (p teamChangeProposed) Error() string {
	return "An admin must confirm this team change before it takes effect. It waits in Settings, Approval rules."
}

func teamChangeWaits(id string, err error) error {
	if err != nil {
		return err
	}
	return teamChangeProposed{id}
}

func teamWriteResult(err error) (toolResult, bool) {
	var proposed teamChangeProposed
	switch {
	case errors.As(err, &proposed):
		return toolText(jsObjOf("ok", true, "proposed", true, "proposalId", proposed.ID, "note", proposed.Error())), true
	case errors.Is(err, errTeamWriteRefused):
		return toolResult{isError: true, text: jsStringify(jsObjOf("ok", false, "error", err.Error()), false)}, true
	}
	return toolResult{}, false
}

func (a *API) proposeTeamItem(ctx context.Context, orgID, kind, target string, before, after any, baseHash string, userID, runID *string) (string, error) {
	var beforeJSON *string
	if before != nil {
		raw, err := json.Marshal(before)
		if err != nil {
			return "", err
		}
		s := string(raw)
		beforeJSON = &s
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return "", err
	}
	var id string
	if err := a.DB.QueryRow(ctx, `insert into team_item_proposal (org_id, kind, target, before, after, base_hash, proposed_by_user_id, proposed_by_run_id)
		values ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8::uuid) returning id::text`,
		orgID, kind, target, beforeJSON, string(afterJSON), baseHash, userID, runID).Scan(&id); err != nil {
		return "", err
	}
	a.recordAudit(ctx, auditEvent{OrgID: orgID, EntityKind: "team_item_proposal", EntityID: id, Event: "team_item:proposed",
		Payload: map[string]any{"kind": kind, "target": target, "runId": deref(runID), "userId": deref(userID)}})
	return id, nil
}

type memoryProposal struct {
	Kind     string  `json:"kind"`
	Text     string  `json:"text"`
	Scope    string  `json:"scope"`
	ScopeID  *string `json:"scopeId"`
	Pinned   bool    `json:"pinned"`
	ThreadID *string `json:"threadId"`
}

type proposedFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

func fileForProposal(path string, data []byte) proposedFile {
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		return proposedFile{Path: path, Content: string(data), Encoding: "utf8"}
	}
	return proposedFile{Path: path, Content: base64.StdEncoding.EncodeToString(data), Encoding: "base64"}
}

func (f proposedFile) bytes() ([]byte, error) {
	if f.Encoding == "base64" {
		return base64.StdEncoding.DecodeString(f.Content)
	}
	return []byte(f.Content), nil
}

func (f proposedFile) shown() (shownFile, error) {
	if f.Path == "" {
		return shownFile{}, nil
	}
	data, err := f.bytes()
	if err != nil {
		return shownFile{}, unshownFile{f.Path}
	}
	return showProposedFile(f.Path, data)
}

type skillProposal struct {
	Files   []proposedFile    `json:"files"`
	Removed []string          `json:"removed"`
	Base    map[string]string `json:"base"`
}

type skillBefore struct {
	Files []proposedFile `json:"files"`
}

type lessonProposal struct {
	Body     string `json:"body"`
	BaseHash string `json:"baseHash"`
}

// proposeSkillEdit stores a run's change to one team skill. Base holds the
// hash of each path as the run received it, "" for a new file, so a confirm
// after another change to the same file is refused.
func (a *API) proposeSkillEdit(ctx context.Context, orgID, teamRoot, name string, baseline skillFileHashes,
	changed []stagedSkillFile, removed []string, userID *string, runID string) (string, error) {
	after := skillProposal{Files: []proposedFile{}, Removed: []string{}, Base: map[string]string{}}
	before := skillBefore{Files: []proposedFile{}}
	add := func(p string) {
		after.Base[p] = baseline[p]
		if data, err := os.ReadFile(filepath.Join(teamRoot, filepath.FromSlash(p))); err == nil {
			before.Files = append(before.Files, fileForProposal(p, data))
		}
	}
	for _, f := range changed {
		if skillOf(f.Path) == name {
			if _, err := showProposedFile(f.Path, f.Content); err != nil {
				return "", err
			}
			after.Files = append(after.Files, fileForProposal(f.Path, f.Content))
			add(f.Path)
		}
	}
	for _, p := range removed {
		if skillOf(p) == name {
			after.Removed = append(after.Removed, p)
			add(p)
		}
	}
	if len(after.Files) == 0 && len(after.Removed) == 0 {
		return "", nil
	}
	base, _ := json.Marshal(after.Base)
	return a.proposeTeamItem(ctx, orgID, "skill", name, before, after, sha256Hex(string(base)), userID, &runID)
}

type diffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

const diffCellLimit = 4_000_000

// lineDiff is a longest-common-subsequence diff by line. Past the cell
// limit it shows the old text removed and the new text added.
func lineDiff(before, after string) []diffLine {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	x, y := split(before), split(after)
	out := []diffLine{}
	if len(x)*len(y) > diffCellLimit {
		for _, l := range x {
			out = append(out, diffLine{"-", l})
		}
		for _, l := range y {
			out = append(out, diffLine{"+", l})
		}
		return out
	}
	n, m := len(x), len(y)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			out = append(out, diffLine{" ", x[i]})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, diffLine{"-", x[i]})
			i++
		default:
			out = append(out, diffLine{"+", y[j]})
			j++
		}
	}
	return out
}

type proposalFileDiff struct {
	Path       string     `json:"path"`
	Change     string     `json:"change"`
	Binary     bool       `json:"binary"`
	BinaryKind string     `json:"binaryKind,omitempty"`
	Charset    string     `json:"charset,omitempty"`
	Unshown    bool       `json:"unshown,omitempty"`
	Lines      []diffLine `json:"lines"`
}

type teamProposalOut struct {
	ID         string             `json:"id"`
	Kind       string             `json:"kind"`
	Target     string             `json:"target"`
	Title      string             `json:"title"`
	Files      []proposalFileDiff `json:"files"`
	ProposedBy *string            `json:"proposedBy"`
	RunID      *string            `json:"runId"`
	CreatedAt  string             `json:"createdAt"`
}

func textDiff(path, before, after string, existed bool) proposalFileDiff {
	change := "changed"
	if !existed {
		change = "added"
	}
	return proposalFileDiff{Path: path, Change: change, Lines: lineDiff(before, after)}
}

func describeTeamProposal(p *teamProposalOut, before, after []byte) {
	switch p.Kind {
	case "memory":
		var next memoryProposal
		_ = json.Unmarshal(after, &next)
		var prev struct {
			Text string `json:"text"`
		}
		existed := before != nil && json.Unmarshal(before, &prev) == nil
		if existed {
			p.Title = "Change a team memory"
		} else {
			p.Title = "Add a team memory (" + strings.ReplaceAll(next.Kind, "_", " ") + ")"
		}
		p.Files = []proposalFileDiff{textDiff("memory", prev.Text, next.Text, existed)}
	case "skill":
		var next skillProposal
		var prev skillBefore
		_ = json.Unmarshal(after, &next)
		_ = json.Unmarshal(before, &prev)
		old := map[string]proposedFile{}
		for _, f := range prev.Files {
			old[f.Path] = f
		}
		p.Title = "Change the team skill " + p.Target
		if len(prev.Files) == 0 {
			p.Title = "Add the team skill " + p.Target
		}
		for _, f := range next.Files {
			o, existed := old[f.Path]
			shown, err := f.shown()
			prev, prevErr := o.shown()
			d := textDiff(f.Path, prev.Text, shown.Text, existed)
			d.Charset = shown.Charset
			switch {
			case err != nil:
				d.Binary, d.Unshown, d.Lines = true, true, []diffLine{}
			case shown.Binary != "":
				d.Binary, d.BinaryKind, d.Lines = true, shown.Binary, []diffLine{}
			case prevErr != nil || prev.Binary != "":
				d.Lines = lineDiff("", shown.Text)
			}
			p.Files = append(p.Files, d)
		}
		for _, path := range next.Removed {
			o := old[path]
			d := proposalFileDiff{Path: path, Change: "removed", Lines: []diffLine{}}
			if prev, err := o.shown(); err == nil && prev.Binary == "" {
				d.Lines, d.Charset = lineDiff(prev.Text, ""), prev.Charset
			} else {
				d.Binary, d.BinaryKind = true, prev.Binary
			}
			p.Files = append(p.Files, d)
		}
	case "skill_lesson":
		var next lessonProposal
		var prev struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(after, &next)
		_ = json.Unmarshal(before, &prev)
		p.Title = "Add a learned lesson to the skill " + p.Target
		p.Files = []proposalFileDiff{textDiff("LEARNED.md", prev.Body, next.Body, prev.Body != "")}
	}
	if p.Files == nil {
		p.Files = []proposalFileDiff{}
	}
}

// teamProposals is GET /api/v1/admin/team-proposals.
func (a *API) teamProposals(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	rows, err := a.DB.Query(r.Context(), `select p.id::text, p.kind, p.target, p.before, p.after,
		coalesce(u.email, p.proposed_by_user_id), p.proposed_by_run_id::text, p.created_at
		from team_item_proposal p left join app_user u on u.id = p.proposed_by_user_id
		where p.org_id = $1 and p.status = 'pending' order by p.created_at desc`, act.OrgID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (teamProposalOut, error) {
		var p teamProposalOut
		var before, after []byte
		var created time.Time
		err := row.Scan(&p.ID, &p.Kind, &p.Target, &before, &after, &p.ProposedBy, &p.RunID, &created)
		p.CreatedAt = jsTime(created)
		describeTeamProposal(&p, before, after)
		return p, err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if out == nil {
		out = []teamProposalOut{}
	}
	sendJSON(w, http.StatusOK, map[string]any{"proposals": out})
}

type pendingTeamProposal struct {
	id, kind, target, baseHash string
	before, after              []byte
	userID, runID, threadID    *string
}

func (a *API) pendingTeamProposal(w http.ResponseWriter, r *http.Request, orgID string) (*pendingTeamProposal, bool) {
	id := r.PathValue("proposalId")
	p := pendingTeamProposal{id: id}
	err := pgx.ErrNoRows
	if isUUID(id) {
		err = a.DB.QueryRow(r.Context(), `select p.kind, p.target, p.base_hash, p.before, p.after, p.proposed_by_user_id,
			p.proposed_by_run_id::text, w.thread_id::text
			from team_item_proposal p left join work_run w on w.id = p.proposed_by_run_id
			where p.org_id = $1 and p.id = $2 and p.status = 'pending'`, orgID, id).
			Scan(&p.kind, &p.target, &p.baseHash, &p.before, &p.after, &p.userID, &p.runID, &p.threadID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		sendError(w, http.StatusNotFound, "No pending team change has this id.")
		return nil, false
	}
	if err != nil {
		a.fail(w, r, err)
		return nil, false
	}
	return &p, true
}

type staleProposal string

func (s staleProposal) Error() string { return string(s) }

// teamProposalConfirm is POST /api/v1/admin/team-proposals/{id}/confirm. A
// change that another change overtook is marked stale and not applied.
func (a *API) teamProposalConfirm(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	p, ok := a.pendingTeamProposal(w, r, act.OrgID)
	if !ok {
		return
	}
	var err error
	switch p.kind {
	case "memory":
		err = a.applyMemoryProposal(ctx, act.OrgID, p)
	case "skill":
		err = a.applySkillProposal(ctx, act.OrgID, p, act.UserID)
	case "skill_lesson":
		err = a.applyLessonProposal(ctx, act.OrgID, p)
	default:
		err = errors.New("unknown team change kind " + p.kind)
	}
	var unshown unshownFile
	if errors.As(err, &unshown) {
		sendError(w, http.StatusUnprocessableEntity, unshown.Error()+" Reject this change.")
		return
	}
	var stale staleProposal
	if errors.As(err, &stale) {
		if _, err := a.DB.Exec(ctx, `update team_item_proposal set status = 'stale', decided_by_user_id = $2, decided_at = now() where id = $1`,
			p.id, act.UserID); err != nil {
			a.fail(w, r, err)
			return
		}
		sendError(w, http.StatusConflict, stale.Error())
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if _, err := a.DB.Exec(ctx, `update team_item_proposal set status = 'confirmed', decided_by_user_id = $2, decided_at = now() where id = $1`,
		p.id, act.UserID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(ctx, auditEvent{OrgID: act.OrgID, EntityKind: "team_item_proposal", EntityID: p.id, Event: "team_item:confirmed",
		Payload: map[string]any{"kind": p.kind, "target": p.target, "decidedBy": act.UserID}})
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// teamProposalReject is POST /api/v1/admin/team-proposals/{id}/reject.
func (a *API) teamProposalReject(w http.ResponseWriter, r *http.Request) {
	act, ok := a.accessAdmin(w, r)
	if !ok {
		return
	}
	p, ok := a.pendingTeamProposal(w, r, act.OrgID)
	if !ok {
		return
	}
	if _, err := a.DB.Exec(r.Context(), `update team_item_proposal set status = 'rejected', decided_by_user_id = $2, decided_at = now() where id = $1`,
		p.id, act.UserID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordAudit(r.Context(), auditEvent{OrgID: act.OrgID, EntityKind: "team_item_proposal", EntityID: p.id, Event: "team_item:rejected",
		Payload: map[string]any{"kind": p.kind, "target": p.target, "decidedBy": act.UserID}})
	sendJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) applyMemoryProposal(ctx context.Context, orgID string, p *pendingTeamProposal) error {
	var next memoryProposal
	if err := json.Unmarshal(p.after, &next); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		now := jsNow()
		if p.before == nil {
			meta, _ := json.Marshal(map[string]string{"origin": "team_proposal", "proposalId": p.id})
			_, err := a.insertMemory(ctx, tx, orgID, memoryDraft{Kind: next.Kind, Text: next.Text, Scope: next.Scope, ScopeID: next.ScopeID,
				Pinned: next.Pinned, Confidence: 0.8, Metadata: meta, RunID: p.runID, ThreadID: next.ThreadID}, now)
			return err
		}
		found, err := a.collectMemories(ctx, tx, `select `+memoryColumns+` from work_memory where org_id = $1 and id::text = $2
			and user_id is null and archived_at is null for update`, orgID, p.target)
		if err != nil {
			return err
		}
		if len(found) == 0 || sha256Hex(found[0].Text) != p.baseHash {
			return staleProposal("The memory changed or was removed after this change was proposed. Nothing was applied.")
		}
		target := found[0]
		if _, err := tx.Exec(ctx, `update work_memory set text = $3, integrity_hmac = $4, embedding = null, updated_at = $5
			where org_id = $1 and id = $2`, orgID, target.ID, next.Text,
			a.memoryHMAC(orgID, "", target.Kind, target.Scope, deref(target.ScopeID), next.Text), now); err != nil {
			return err
		}
		return memoryEvent(ctx, tx, orgID, &target.ID, p.runID, p.threadID, "update",
			map[string]any{"previousText": target.Text, "text": next.Text, "proposalId": p.id})
	})
}

func (a *API) applySkillProposal(ctx context.Context, orgID string, p *pendingTeamProposal, adminID string) error {
	var next skillProposal
	if err := json.Unmarshal(p.after, &next); err != nil {
		return err
	}
	teamRoot := filepath.Join(orgAgentRoot(orgID), "skills")
	current := hashSkillTree(teamRoot)
	paths := slices.Clone(next.Removed)
	for _, f := range next.Files {
		paths = append(paths, f.Path)
	}
	for _, path := range paths {
		if !safeSkillPath(path) || skillOf(path) != p.target {
			return errors.New("the proposed skill change holds a path outside the skill: " + path)
		}
		if current[path] != next.Base[path] {
			return staleProposal("The skill " + p.target + " changed after this change was proposed (" + path + "). Nothing was applied.")
		}
	}
	for _, f := range next.Files {
		if _, err := f.shown(); err != nil {
			return err
		}
	}
	for _, f := range next.Files {
		data, err := f.bytes()
		if err != nil {
			return err
		}
		target := filepath.Join(teamRoot, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeFileKeepMode(target, data); err != nil {
			return err
		}
	}
	for _, path := range next.Removed {
		if err := os.Remove(filepath.Join(teamRoot, filepath.FromSlash(path))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	run := ""
	if p.runID != nil {
		run = " from run " + jsSlice(*p.runID, 8)
	}
	a.recordConfigChange(ctx, orgID, "Confirmed a change to skill "+p.target+run, "skill", p.target, adminID)
	return nil
}

func (a *API) applyLessonProposal(ctx context.Context, orgID string, p *pendingTeamProposal) error {
	var next lessonProposal
	var prev struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(p.after, &next); err != nil {
		return err
	}
	_ = json.Unmarshal(p.before, &prev)
	root := orgAgentRoot(orgID)
	base, err := fingerprintSkillTree(filepath.Join(root, "skills", p.target))
	existing := readLearnedOverlay(root, p.target)
	body := ""
	if existing != nil {
		body = existing.Body
	}
	if err != nil || base != next.BaseHash || body != prev.Body {
		return staleProposal("The skill " + p.target + " or its lessons changed after this lesson was proposed. Nothing was applied.")
	}
	overlay := &learnedOverlay{SkillName: p.target, BaseHash: next.BaseHash, Status: "applied", Body: next.Body}
	if err := writeLearnedOverlay(root, overlay); err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, `insert into skill_learn_state (org_id, skill_name, enabled, current_base_hash, current_learned_hash)
		values ($1, $2, true, $3, $4) on conflict (org_id, skill_name) do update
		set current_base_hash = excluded.current_base_hash, current_learned_hash = excluded.current_learned_hash, updated_at = now()`,
		orgID, p.target, next.BaseHash, sha256Hex(next.Body))
	return err
}
