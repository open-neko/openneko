// Deterministic external-model fixture; GraphJin and database execution remain real.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

func main() {
	var mu sync.Mutex
	counts := map[string]int{}
	delay := 0
	effectFences := false
	proposal := false
	upload := false
	artifact := false
	processTask := false
	processFail := false
	processLarge := false
	processCancel := false
	processOversize := false
	processFlood := false
	processOffice := false
	officeOutput := "xlsx"
	management := false
	audit := false
	auditDenied := false
	uploadedLibrary := false
	sourceConfig := false
	workflowSave := false
	workflowEdit := false
	workflowDelete := false
	workflowDeleteDenied := false
	workflowWhen := false
	workflowWhenEdit := false
	workflowWatch := false
	workflowBadTrigger := false
	ruleSave := false
	ruleEdit := false
	clarification := false
	card := false
	skill := false
	answerClarification := false
	continuation := false
	pauseResponder := false
	triageChoice := "multi_step"
	http.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(counts)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		var c struct {
			Delay                int    `json:"delay"`
			EffectFences         bool   `json:"effect_fences"`
			Proposal             bool   `json:"proposal"`
			Upload               bool   `json:"upload"`
			Artifact             bool   `json:"artifact"`
			Process              bool   `json:"process"`
			ProcessFail          bool   `json:"process_fail"`
			ProcessLarge         bool   `json:"process_large"`
			ProcessCancel        bool   `json:"process_cancel"`
			ProcessOversize      bool   `json:"process_oversize"`
			ProcessFlood         bool   `json:"process_flood"`
			ProcessOffice        bool   `json:"process_office"`
			OfficeOutput         string `json:"office_output"`
			Management           bool   `json:"management"`
			Audit                bool   `json:"audit"`
			AuditDenied          bool   `json:"audit_denied"`
			UploadedLibrary      bool   `json:"uploaded_library"`
			SourceConfig         bool   `json:"source_config"`
			WorkflowSave         bool   `json:"workflow_save"`
			WorkflowEdit         bool   `json:"workflow_edit"`
			WorkflowDelete       bool   `json:"workflow_delete"`
			WorkflowDeleteDenied bool   `json:"workflow_delete_denied"`
			WorkflowWhen         bool   `json:"workflow_when"`
			WorkflowWhenEdit     bool   `json:"workflow_when_edit"`
			WorkflowWatch        bool   `json:"workflow_watch"`
			WorkflowBadTrigger   bool   `json:"workflow_bad_trigger"`
			RuleSave             bool   `json:"rule_save"`
			RuleEdit             bool   `json:"rule_edit"`
			Clarification        bool   `json:"clarification"`
			Card                 bool   `json:"card"`
			Skill                bool   `json:"skill"`
			AnswerClarification  bool   `json:"answer_clarification"`
			Continue             bool   `json:"continue"`
			PauseResponder       bool   `json:"pause_responder"`
			TriageChoice         string `json:"triage_choice"`
		}
		if json.NewDecoder(r.Body).Decode(&c) != nil || c.Delay < 0 || c.Delay > 30 ||
			(c.OfficeOutput != "" && c.OfficeOutput != "xlsx" && c.OfficeOutput != "docx") ||
			(c.TriageChoice != "" && c.TriageChoice != "short_answer" && c.TriageChoice != "multi_step" && c.TriageChoice != "artifact_pipeline" && c.TriageChoice != "uncertain") {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		if !c.Continue {
			counts = map[string]int{}
		}
		continuation = c.Continue
		pauseResponder = c.PauseResponder
		triageChoice = c.TriageChoice
		if triageChoice == "" {
			triageChoice = "multi_step"
		}
		delay = c.Delay
		effectFences = c.EffectFences
		proposal = c.Proposal
		upload = c.Upload
		artifact = c.Artifact
		processTask = c.Process
		processFail = c.ProcessFail
		processLarge = c.ProcessLarge
		processCancel = c.ProcessCancel
		processOversize = c.ProcessOversize
		processFlood = c.ProcessFlood
		processOffice = c.ProcessOffice
		officeOutput = c.OfficeOutput
		if officeOutput == "" {
			officeOutput = "xlsx"
		}
		management = c.Management
		audit = c.Audit
		auditDenied = c.AuditDenied
		uploadedLibrary = c.UploadedLibrary
		sourceConfig = c.SourceConfig
		workflowSave = c.WorkflowSave
		workflowEdit = c.WorkflowEdit
		workflowDelete = c.WorkflowDelete
		workflowDeleteDenied = c.WorkflowDeleteDenied
		workflowWhen = c.WorkflowWhen
		workflowWhenEdit = c.WorkflowWhenEdit
		workflowWatch = c.WorkflowWatch
		workflowBadTrigger = c.WorkflowBadTrigger
		ruleSave = c.RuleSave
		ruleEdit = c.RuleEdit
		clarification = c.Clarification
		card = c.Card
		skill = c.Skill
		answerClarification = c.AnswerClarification
		mu.Unlock()
		w.WriteHeader(204)
	})
	http.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		vector := make([]float64, 384)
		vector[0] = 1
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "Xenova/all-MiniLM-L6-v2", "dimensions": 384, "vector": vector})
	})
	http.HandleFunc("/route/triage/v1/systemone", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-m6-triage" {
			http.Error(w, "triage credential mismatch", http.StatusForbidden)
			return
		}
		var request struct {
			Model string `json:"model"`
			State struct {
				TaskSummary string `json:"task_summary"`
			} `json:"state"`
			Questions map[string]struct {
				Type string `json:"type"`
			} `json:"questions"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "jev-fixture" ||
			request.Questions["workload"].Type != "choice" || request.State.TaskSummary == "" || len(request.State.TaskSummary) > 2048 {
			http.Error(w, "invalid bounded Typesafe request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		counts["jev-fixture"]++
		choice := triageChoice
		mu.Unlock()
		probabilities := map[string]float64{"short_answer": 0.05, "multi_step": 0.8, "artifact_pipeline": 0.1, "uncertain": 0.05}
		confidence := 0.8
		if choice != "multi_step" {
			probabilities = map[string]float64{"short_answer": 0.04, "multi_step": 0.04, "artifact_pipeline": 0.04, "uncertain": 0.04}
			probabilities[choice] = 0.88
			confidence = 0.88
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-fixture", "answers": map[string]any{"workload": map[string]any{
			"type": "choice", "choice": choice, "confidence": confidence,
			"probabilities": probabilities}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5}})
	})
	modelHandler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/route/") {
			stage := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/route/"), "/v1/chat/completions")
			responses := map[string]string{
				"context":          `{"javascriptCode":"final('Use the approved executor route',{})"}`,
				"context-spare":    `{"javascriptCode":"final('Use the approved executor route',{})"}`,
				"skill":            `{"selected":"reference-check"}`,
				"context-lookup":   `{"javascriptCode":"final('Find the seeded reference',{})"}`,
				"executor":         `{"javascriptCode":"final('Answer the routing check',{})"}`,
				"executor-lookup":  "",
				"executor-base":    `{"javascriptCode":"throw new Error('retry this actor step');"}`,
				"executor-strong":  `{"javascriptCode":"final('Answer the routing check',{})"}`,
				"responder":        `{"answer":"ROUTED-OK"}`,
				"responder-lookup": `{"answer":"The seeded reference is REF-42."}`,
			}
			response, ok := responses[stage]
			if stage == "context-503" || stage == "context-403" || stage == "context-429" {
				ok = true
			}
			if !ok || r.URL.Path != "/route/"+stage+"/v1/chat/completions" ||
				r.Header.Get("Authorization") != "Bearer synthetic-m6-"+stage {
				http.Error(w, "route or broker credential mismatch", http.StatusForbidden)
				return
			}
			var req struct {
				Model string `json:"model"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "harness-route-"+stage {
				http.Error(w, "model route mismatch", http.StatusBadRequest)
				return
			}
			mu.Lock()
			n := counts[req.Model]
			counts[req.Model]++
			mu.Unlock()
			if stage == "executor-lookup" {
				if n == 0 {
					response = `{"javascriptCode":"const evidence=lookup('Find the seeded reference'); final('Verify the reference',{evidence});"}`
				} else {
					response = `{"javascriptCode":"const evidence=harnessSavedOperation(1); if(!JSON.stringify(evidence).includes('REF-42')) throw Error('lookup evidence missing'); final('Report the reference',{reference:'REF-42'});"}`
				}
			}
			if stage == "context-503" {
				http.Error(w, "synthetic transient failure", http.StatusServiceUnavailable)
				return
			}
			if stage == "context-403" {
				http.Error(w, "synthetic policy denial", http.StatusForbidden)
				return
			}
			if stage == "context-429" {
				http.Error(w, "synthetic rate limit", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
			return
		}
		var req struct {
			Model    string          `json:"model"`
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		wait := delay
		effects := effectFences
		propose := proposal
		readUpload := upload
		writeArtifact := artifact
		runProcess := processTask
		failProcess := processFail
		runLargeProcess := processLarge
		runCancelProcess := processCancel
		runOversizeProcess := processOversize
		runFloodProcess := processFlood
		runOfficeProcess := processOffice
		selectedOfficeOutput := officeOutput
		readManagement := management
		readAudit := audit
		readUploadedLibrary := uploadedLibrary
		readSourceConfig := sourceConfig
		createWorkflow := workflowSave
		editWorkflow := workflowEdit
		deleteWorkflow := workflowDelete
		denyWorkflowDelete := workflowDeleteDenied
		createWorkflowWhen := workflowWhen
		editWorkflowWhen := workflowWhenEdit
		createWorkflowWatch := workflowWatch
		rejectWorkflowTrigger := workflowBadTrigger
		createRule := ruleSave
		editRule := ruleEdit
		expectAuditDenial := auditDenied
		askClarification := clarification
		renderCard := card
		followSkill := skill
		answerQuestion := answerClarification
		resume := continuation
		compactionFixture := req.Model == "harness-compaction-output-fixture" || req.Model == "harness-compaction-approval-fixture"
		compactionSummary := compactionFixture && strings.Contains(string(req.Messages), "You are an internal AxAgent trajectory summarizer")
		n := counts[req.Model]
		if req.Model == "harness-trigger-crash-fixture" {
			if len(req.Messages) > counts["max-request:"+req.Model] {
				counts["max-request:"+req.Model] = len(req.Messages)
			}
			if copies := strings.Count(string(req.Messages), "LEAD-42"); copies > counts["max-csv-markers:"+req.Model] {
				counts["max-csv-markers:"+req.Model] = copies
			}
		}
		if compactionFixture {
			if len(req.Messages) > counts["max-request:"+req.Model] {
				counts["max-request:"+req.Model] = len(req.Messages)
			}
			if compactionSummary {
				counts["summary-at:"+req.Model] = counts["ordinary:"+req.Model]
				counts["summary:"+req.Model]++
			} else {
				n = counts["ordinary:"+req.Model]
				counts["ordinary:"+req.Model]++
			}
		}
		if pauseResponder && ((req.Model == "harness-fixture" && n == 2) || (req.Model == "harness-job-child-crash-fixture" && n == 4) || (req.Model == "harness-trigger-crash-fixture" && n == 3)) {
			// Keep the ordinary worker-death request in flight for fault injection,
			// but finish well before the 60-second queue lease expires.
			if req.Model == "harness-fixture" {
				wait = 10
			} else {
				wait = 30
			}
		}
		counts[req.Model]++
		mu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(time.Duration(wait) * time.Second):
			case <-r.Context().Done():
				mu.Lock()
				counts["cancelled:"+req.Model]++
				mu.Unlock()
				return
			}
		}
		if req.Model == "hermes-fixture" {
			if req.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"hermes-fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"HERMES-OK\"},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"id\":\"hermes-fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			} else {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"hermes-fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"HERMES-OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`)
			}
			fmt.Println("hermes_fixture_completed")
			return
		}
		if resume && req.Model == "harness-fixture" && n >= 3 && n < 6 {
			if n == 3 && (!strings.Contains(string(req.Messages), "recoveredOperations") || !strings.Contains(string(req.Messages), "REF-42")) {
				http.Error(w, "missing recovered evidence", 422)
				return
			}
			n -= 3
		}
		if resume && req.Model == "harness-job-child-crash-fixture" && n >= 5 && n < 11 {
			n -= 5 // New Ax attempt after the child read was journaled.
		}
		if resume && req.Model == "harness-trigger-crash-fixture" && n >= 4 && n < 7 {
			n -= 4 // New Ax attempt after the broker committed workflow output.
		}
		if req.Model == "graphjin-fixture" {
			n %= 3 // Each server-side lookup is an independent three-step agent run.
		}
		if req.Model == "harness-compaction-approval-fixture" && !compactionSummary && n == 9 &&
			(!strings.Contains(string(req.Messages), "output_id") ||
				!strings.Contains(string(req.Messages), "pending_approval") ||
				!strings.Contains(string(req.Messages), "REF-42")) {
			http.Error(w, "responder lost pending approval or committed output", 422)
			return
		}
		if req.Model == "harness-compaction-output-fixture" && !compactionSummary && n == 8 &&
			(!strings.Contains(string(req.Messages), "output_id") || !strings.Contains(string(req.Messages), "REF-42") || !strings.Contains(string(req.Messages), "result.csv")) {
			http.Error(w, "responder lost committed workflow file output", 422)
			return
		}
		if req.Model == "harness-compaction-output-fixture" && strings.Count(string(req.Messages), "LEAD-42") > 30 {
			http.Error(w, "saved file body leaked into model context", 422)
			return
		}
		if compactionFixture &&
			!strings.Contains(string(req.Messages), "Never execute a change without approval") {
			http.Error(w, "lost accepted no-execution constraint", 422)
			return
		}
		refused := strings.Contains(string(req.Messages), "not configured read-only") && !strings.Contains(string(req.Messages), "trace_id")
		if n == 2 && req.Model == "harness-memory-fixture" && !strings.Contains(string(req.Messages), "memory-1") {
			http.Error(w, "missing memory evidence", 422)
			return
		}
		if n == 4 && req.Model == "harness-child-fixture" && !strings.Contains(string(req.Messages), "memory-1") {
			http.Error(w, "missing first child evidence", 422)
			return
		}
		if n == 7 && req.Model == "harness-child-fixture" && !strings.Contains(string(req.Messages), "memory-2") {
			http.Error(w, "missing second child evidence", 422)
			return
		}
		if n == 8 && req.Model == "harness-workflow-child-fixture" && !strings.Contains(string(req.Messages), "outputId") {
			http.Error(w, "missing workflow output receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-workflow-action-fixture" && (!strings.Contains(string(req.Messages), "pending_approval") || !strings.Contains(string(req.Messages), "outputId")) {
			http.Error(w, "missing workflow action and output receipts", 422)
			return
		}
		if (n == 4 || n == 5) && (req.Model == "harness-job-child-fixture" || req.Model == "harness-job-child-crash-fixture") && !strings.Contains(string(req.Messages), "REF-42") {
			http.Error(w, "missing agent-job child evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-memory-save-fixture" && !strings.Contains(string(req.Messages), "memoryId") {
			http.Error(w, "missing saved memory receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-skill-create-fixture" && !strings.Contains(string(req.Messages), "fixture-lead-review/SKILL.md") {
			http.Error(w, "missing published skill receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-skill-read-fixture" && !strings.Contains(string(req.Messages), "Read the selected CSV") {
			http.Error(w, "missing staged skill content", 422)
			return
		}
		if n == 2 && req.Model == "harness-skill-update-fixture" && !strings.Contains(string(req.Messages), "fixture-lead-review/SKILL.md") {
			http.Error(w, "missing updated skill receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-skill-read-updated-fixture" && !strings.Contains(string(req.Messages), "Verify the lead owner") {
			http.Error(w, "missing updated staged skill", 422)
			return
		}
		if n == 2 && req.Model == "harness-workflow-list-fixture" && !strings.Contains(string(req.Messages), "Fixture workflow") {
			http.Error(w, "missing workflow list evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-library-fixture" && !strings.Contains(string(req.Messages), "TERMS-42") {
			http.Error(w, "missing library evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-records-fixture" && (!strings.Contains(string(req.Messages), "apps") || !strings.Contains(string(req.Messages), "crm")) {
			http.Error(w, "missing records catalog evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-records-data-fixture" && (!strings.Contains(string(req.Messages), "loan-42") || !strings.Contains(string(req.Messages), "loan-deleted-42")) {
			http.Error(w, "missing populated records evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-records-action-fixture" && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing records action approval receipt", 422)
			return
		}
		if n == 2 && (req.Model == "harness-records-queue-fixture" || req.Model == "harness-records-create-fixture" || req.Model == "harness-records-delete-fixture" || req.Model == "harness-records-restore-fixture") && !strings.Contains(string(req.Messages), "pending_approval") {
			mu.Lock()
			counts["rejected:queue_pending_receipt"]++
			mu.Unlock()
			http.Error(w, "missing queued records approval receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-installed-plugin-fixture" && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing installed plugin approval receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-user-admin-fixture" && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing user administration approval receipt", 422)
			return
		}
		if n == 2 && (req.Model == "harness-user-deactivate-fixture" || req.Model == "harness-user-reactivate-fixture") && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing user state approval receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-user-promote-fixture" && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing role promotion approval receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-data-source-admin-fixture" && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing data-source administration approval receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-group-admin-fixture" && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing group administration approval receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-group-member-fixture" && !strings.Contains(string(req.Messages), "pending_approval") {
			http.Error(w, "missing group membership approval receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && readUpload && !strings.Contains(string(req.Messages), "LEAD-42") {
			http.Error(w, "missing uploaded file evidence", 422)
			return
		}
		artifactHash := sha256.Sum256([]byte("lead_id\nLEAD-42\n"))
		if n == 2 && req.Model == "harness-fixture" && writeArtifact && !strings.Contains(string(req.Messages), hex.EncodeToString(artifactHash[:])) {
			http.Error(w, "missing created artifact receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && runProcess &&
			(!strings.Contains(string(req.Messages), "process-1/result.csv") || !strings.Contains(string(req.Messages), hex.EncodeToString(artifactHash[:]))) {
			http.Error(w, "missing isolated process artifact receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && failProcess && !strings.Contains(string(req.Messages), "outcome unknown") {
			http.Error(w, "missing failed isolated process result", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && runLargeProcess &&
			(!strings.Contains(string(req.Messages), "process-1/large.bin") || !strings.Contains(string(req.Messages), "8388608")) {
			http.Error(w, "missing large process artifact receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && runOfficeProcess &&
			(!strings.Contains(string(req.Messages), "OFFICE-SKILL-MARKER") || !strings.Contains(string(req.Messages), "process-2/leads.xlsx") || !strings.Contains(string(req.Messages), "process-2/summary.docx")) {
			http.Error(w, "missing Office process artifact receipts", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && readManagement &&
			(!strings.Contains(string(req.Messages), "harness-management-rule") || !strings.Contains(string(req.Messages), "users") || !strings.Contains(string(req.Messages), "sources")) {
			http.Error(w, "missing management catalog evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && readAudit {
			messages := string(req.Messages)
			if expectAuditDenial {
				if !strings.Contains(messages, "audit trail is admin-only") || strings.Contains(messages, "harness-audit-marker") {
					http.Error(w, "missing audit denial or leaked audit trail", 422)
					return
				}
			} else if !strings.Contains(messages, "harness-audit-marker") {
				http.Error(w, "missing admin audit evidence", 422)
				return
			}
		}
		if n == 2 && req.Model == "harness-fixture" && readUploadedLibrary && (!strings.Contains(string(req.Messages), "UPLOAD-LIBRARY-42") || !strings.Contains(string(req.Messages), "fixture/uploaded-policy.md")) {
			http.Error(w, "missing uploaded library search evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && readSourceConfig && (!strings.Contains(string(req.Messages), "SYNTHETIC_DB") || !strings.Contains(string(req.Messages), "Fixture source API")) {
			http.Error(w, "missing source configuration read evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && (createWorkflow || editWorkflow || deleteWorkflow || createWorkflowWhen || editWorkflowWhen || createWorkflowWatch) && !strings.Contains(string(req.Messages), "workflowId") {
			http.Error(w, "missing workflow save receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && rejectWorkflowTrigger && !strings.Contains(string(req.Messages), "mutation_loop") {
			http.Error(w, "missing trigger rollback receipt", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && denyWorkflowDelete && !strings.Contains(string(req.Messages), "confirmation_required") {
			http.Error(w, "missing workflow delete confirmation denial", 422)
			return
		}
		if n == 3 && req.Model == "harness-trigger-fixture" && (!strings.Contains(string(req.Messages), "operation_id") || !strings.Contains(string(req.Messages), "output_id")) {
			http.Error(w, "missing host-committed workflow state at responder", 422)
			return
		}
		if resume && n == 0 && req.Model == "harness-trigger-crash-fixture" && (!strings.Contains(string(req.Messages), "hostState") || !strings.Contains(string(req.Messages), "output_id")) {
			mu.Lock()
			counts["reject:resume-state"]++
			mu.Unlock()
			http.Error(w, "missing resumed host workflow state", 422)
			return
		}
		if req.Model == "harness-trigger-crash-fixture" && strings.Count(string(req.Messages), "LEAD-42") > 150 {
			mu.Lock()
			counts["reject:csv-context"]++
			mu.Unlock()
			http.Error(w, "saved CSV body leaked into model context", 422)
			return
		}
		if n == 2 && req.Model == "harness-fixture" && (createRule || editRule) && !strings.Contains(string(req.Messages), "ruleId") {
			http.Error(w, "missing rule save receipt", 422)
			return
		}
		if n == 2 && req.Model != "harness-workflow-office-fixture" && req.Model != "harness-stream-fixture" && req.Model != "harness-budget-short-fixture" && req.Model != "harness-compaction-approval-fixture" && req.Model != "harness-compaction-output-fixture" && req.Model != "harness-finalizer-output-fixture" && req.Model != "harness-finalizer-empty-fixture" && req.Model != "harness-memory-fixture" && req.Model != "harness-child-fixture" && req.Model != "harness-workflow-child-fixture" && req.Model != "harness-workflow-action-fixture" && req.Model != "harness-trigger-fixture" && req.Model != "harness-trigger-crash-fixture" && req.Model != "harness-job-child-fixture" && req.Model != "harness-job-child-crash-fixture" && req.Model != "harness-job-model-only-fixture" && req.Model != "harness-memory-save-fixture" && req.Model != "harness-skill-create-fixture" && req.Model != "harness-skill-read-fixture" && req.Model != "harness-skill-update-fixture" && req.Model != "harness-skill-read-updated-fixture" && req.Model != "harness-user-admin-fixture" && req.Model != "harness-user-deactivate-fixture" && req.Model != "harness-user-reactivate-fixture" && req.Model != "harness-user-promote-fixture" && req.Model != "harness-data-source-admin-fixture" && req.Model != "harness-group-admin-fixture" && req.Model != "harness-group-member-fixture" && req.Model != "harness-workflow-list-fixture" && req.Model != "harness-library-fixture" && req.Model != "harness-records-fixture" && req.Model != "harness-records-data-fixture" && req.Model != "harness-records-action-fixture" && req.Model != "harness-records-queue-fixture" && req.Model != "harness-records-create-fixture" && req.Model != "harness-records-delete-fixture" && req.Model != "harness-records-restore-fixture" && req.Model != "harness-installed-plugin-fixture" && !readUpload && !writeArtifact && !runProcess && !failProcess && !runLargeProcess && !runCancelProcess && !runOversizeProcess && !runFloodProcess && !runOfficeProcess && !readManagement && !readAudit && !readUploadedLibrary && !readSourceConfig && !createWorkflow && !editWorkflow && !deleteWorkflow && !denyWorkflowDelete && !createWorkflowWhen && !editWorkflowWhen && !createWorkflowWatch && !rejectWorkflowTrigger && !createRule && !editRule && !propose && !answerQuestion && !renderCard && !followSkill && !refused && (!strings.Contains(string(req.Messages), "REF-42") || (req.Model != "graphjin-fixture" && !strings.Contains(string(req.Messages), "trace_id"))) {
			http.Error(w, "missing real lookup evidence", 422)
			return
		}
		var responses []string
		if req.Model == "graphjin-fixture" {
			responses = []string{
				`{"javascriptCode":"const schema=query_catalog({id:'table:default:public.references'}); console.log(schema);"}`,
				`{"javascriptCode":"const evidence=execute_graphql({query:'query { references { id label } }'}); final({status:'answered',answer:'The reference is REF-42.',data:evidence.data},{evidence});"}`,
				`{"status":"answered","answer":"The reference is REF-42.","data":{"references":[{"id":42,"label":"REF-42"}]},"evidence":[],"actions":[],"next":[]}`,
			}
		} else if req.Model == "harness-memory-fixture" {
			responses = []string{`{"javascriptCode":"final('Search saved memory', {})"}`, `{"javascriptCode":"const memory=mcp_memory_search({query:'find policy'}); final('Report memory',{memory});"}`, `{"answer":"Saved policy found in memory-1."}`}
		} else if req.Model == "harness-child-fixture" {
			responses = []string{
				`{"javascriptCode":"final('Investigate both memory topics',{})"}`,
				`{"javascriptCode":"const one=team.researcher({question:'Find the policy memory'}); const two=team.researcher({question:'Find the exception memory'}); final('Report both investigations',{one,two});"}`,
				`{"javascriptCode":"final('Find the policy memory',{})"}`,
				`{"javascriptCode":"const memory=mcp_memory_search({query:'find policy'}); final('Report policy',{memory});"}`,
				`{"answer":"Policy evidence memory-1."}`,
				`{"javascriptCode":"final('Find the exception memory',{})"}`,
				`{"javascriptCode":"const memory=mcp_memory_search({query:'find exception'}); final('Report exception',{memory});"}`,
				`{"answer":"Exception evidence memory-2."}`,
				`{"answer":"The policy is memory-1 and the exception is memory-2."}`,
			}
		} else if req.Model == "harness-workflow-child-fixture" {
			if propose {
				responses = []string{
					`{"javascriptCode":"final('Propose the installed action and emit a finding',{})"}`,
					`{"javascriptCode":"const request=propose({action:'harness_workflow_effect_fixture',arguments:{value:42},summary:'Update the synthetic reference'}); if(request.status!=='pending_approval') throw Error('approval missing'); const output=workflow_output_emit({kind:'finding',title:'Action proposed',body:'The synthetic action awaits approval.'}); final('Report the pending action and finding',{request,output});"}`,
					`{"answer":"Recorded one finding and one action request pending approval."}`,
				}
			} else {
				responses = []string{
					`{"javascriptCode":"final('Check two references and emit the finding',{})"}`,
					`{"javascriptCode":"const one=team.researcher({question:'Find the first seeded reference'}); const two=team.researcher({question:'Independently verify the seeded reference'}); if(!JSON.stringify(one).includes('REF-42')||!JSON.stringify(two).includes('REF-42')) throw Error('child evidence missing'); const receipt=workflow_output_emit({kind:'finding',title:'Two reference checks',body:'Both independent checks found REF-42.',payload:{reference:'REF-42'}}); final('Report recorded workflow finding',{receipt});"}`,
					`{"javascriptCode":"final('Find the first reference',{})"}`,
					`{"javascriptCode":"const evidence=lookup('Find the first seeded reference'); final('Report first reference',{evidence});"}`,
					`{"answer":"First reference REF-42."}`,
					`{"javascriptCode":"final('Verify the second reference',{})"}`,
					`{"javascriptCode":"const evidence=lookup('Independently verify the seeded reference'); final('Report second reference',{evidence});"}`,
					`{"answer":"Second reference REF-42."}`,
					`{"answer":"Recorded a finding supported by two child investigations: REF-42."}`,
				}
			}
		} else if req.Model == "harness-workflow-office-fixture" {
			const uploadedLead = `with open('lead.csv', newline='') as source:
    rows = list(csv.DictReader(source))
assert len(rows) == 1 and rows[0]['lead_id'] == 'LEAD-42'
lead = escape(rows[0]['lead_id'])`
			script := strings.Replace(officeScript, uploadedLead, "lead = 'LEAD-42'", 1)
			if script == officeScript {
				http.Error(w, "Office workflow fixture could not isolate the upload", 500)
				return
			}
			name := "leads.xlsx"
			if selectedOfficeOutput == "docx" {
				name = "summary.docx"
			}
			call := fmt.Sprintf("const result=process_run({language:'python',script:%q,uploads:[],outputs:['leads.xlsx','summary.docx']}); const output=workflow_output_emit({kind:'file',title:'Office package',artifactPath:'process-1/%s'}); final('Report the recorded Office package',{result,output});", script, name)
			encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
			responses = []string{`{"javascriptCode":"final('Create and record the Office package',{})"}`, string(encoded), fmt.Sprintf(`{"answer":"Recorded %s as a workflow file output."}`, name)}
		} else if req.Model == "harness-compaction-approval-fixture" {
			if compactionSummary {
				responses = []string{"Objective: Report the saved REF-42 finding and pending action request.\nCurrent state and artifacts: a workflow finding is committed and an action request awaits approval.\nEvidence: REF-42; pending_approval.\nUser constraints and preferences: Never execute a change without approval.\nNext step: report the saved output and pending approval without executing it."}
				n = 0
			} else {
				responses = []string{
					`{"javascriptCode":"final('Inspect the reference, propose a governed update, and report the pending request',{})"}`,
					`{"javascriptCode":"const row=lookup('Find the seeded reference'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const approval=propose({action:'harness_workflow_effect_fixture',arguments:{value:42},summary:'Update the synthetic reference'}); if(approval.status!=='pending_approval') throw Error('approval missing'); console.log('noise-'.repeat(3000),approval);"}`,
					`{"javascriptCode":"const out=workflow_output_emit({kind:'finding',title:'Compacted pending reference',body:'The source-change reference is REF-42; proposed update awaits approval.',payload:{reference:'REF-42'}}); console.log('noise-'.repeat(3000),out);"}`,
					`{"javascriptCode":"const row=lookup('Verify the seeded reference independently'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const row=lookup('Check reference evidence again'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const row=lookup('Recheck the reference'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const row=lookup('Confirm reference one last time'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const approval=harnessSavedOperation(2),out=harnessSavedOperation(3); if(!JSON.stringify(approval).includes('pending_approval')||!JSON.stringify(out).includes('outputId')) throw Error('saved approval or output missing'); final('Report both committed receipts',{approval,out});"}`,
					`{"answer":"The REF-42 finding is recorded. The proposed update remains pending approval; it was not executed."}`,
				}
			}
		} else if req.Model == "harness-compaction-output-fixture" {
			if compactionSummary {
				responses = []string{"Objective: Report the saved REF-42 CSV after reading several references.\nCurrent state and artifacts: result.csv has been written and a workflow file output has been committed.\nEvidence: REF-42.\nUser constraints and preferences: Never execute a change without approval.\nNext step: verify the saved file and output receipts and report them."}
				n = 0
			} else {
				responses = []string{
					`{"javascriptCode":"final('Read references, create a CSV, and record one file output without executing changes',{})"}`,
					`{"javascriptCode":"const row=lookup('Find the seeded reference'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const written=file_write({path:'result.csv',content:'lead_id\\n'+'LEAD-42\\n'.repeat(6500)}); console.log('noise-'.repeat(3000),written);"}`,
					`{"javascriptCode":"const read=file_read({path:'result.csv'}); if(!read.reference) throw Error('large file read was not referenced'); const full=harnessSavedOperation(3).result; if(full.content.length!==52008||!full.content.endsWith('LEAD-42\\n')) throw Error('CSV content lost'); console.log('noise-'.repeat(3000),{path:full.path,version:full.version,length:full.content.length});"}`,
					`{"javascriptCode":"const out=workflow_output_emit({kind:'file',title:'Compacted reference CSV',body:'The source-change reference is REF-42.',artifactPath:'result.csv',payload:{reference:'REF-42'}}); console.log('noise-'.repeat(3000),out);"}`,
					`{"javascriptCode":"const row=lookup('Verify the seeded reference independently'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const row=lookup('Check reference evidence again'); console.log('noise-'.repeat(3000),row);"}`,
					`{"javascriptCode":"const row=lookup('Recheck the reference'); const saved=harnessSavedOperation(4),read=harnessSavedOperation(3); if(!JSON.stringify(saved).includes('outputId')||!JSON.stringify(saved).includes('result.csv')) throw Error('saved file output receipt missing'); if(read.result.content.length!==52008||!read.result.content.endsWith('LEAD-42\\n')) throw Error('saved CSV content missing'); console.log('noise-'.repeat(3000),{row,readBytes:read.result.content.length}); final('Report the committed CSV',{saved});"}`,
					`{"answer":"The REF-42 CSV is recorded as result.csv; no change was executed."}`,
				}
			}
		} else if req.Model == "harness-finalizer-output-fixture" || req.Model == "harness-finalizer-empty-fixture" {
			responses = []string{`{"javascriptCode":"final('Record the reference finding',{})"}`}
			for step := 0; step < 8; step++ {
				code := `{"javascriptCode":"console.log('Still investigating');"}`
				if step == 0 && req.Model == "harness-finalizer-output-fixture" {
					code = `{"javascriptCode":"const receipt=workflow_output_emit({kind:'finding',title:'Bounded finalizer reference',body:'The source-change reference is REF-42.',payload:{reference:'REF-42'}}); console.log(receipt);"}`
				}
				responses = append(responses, code)
			}
			responses = append(responses, `{"answer":"Recorded the committed REF-42 workflow finding."}`)
		} else if req.Model == "harness-stream-fixture" {
			responses = []string{
				`{"javascriptCode":"final('Answer the streaming check',{})"}`,
				`{"javascriptCode":"final('No tools needed',{})"}`,
				`{"answer":"STREAM-OK"}`,
			}
		} else if req.Model == "harness-budget-short-fixture" {
			responses = []string{
				`{"javascriptCode":"final('Record one short finding',{})"}`,
				`{"javascriptCode":"const out=workflow_output_emit({kind:'finding',title:'Short budget finding',body:'The short check passed.',payload:{ok:true}}); final('Report the finding',{out});"}`,
				`{"answer":"Recorded the short check finding."}`,
			}
		} else if req.Model == "harness-trigger-fixture" || req.Model == "harness-trigger-crash-fixture" {
			responses = []string{
				`{"javascriptCode":"final('Find the seeded reference and record a finding',{})"}`,
				`{"javascriptCode":"const evidence=lookup('Find the seeded reference'); if(!JSON.stringify(evidence).includes('REF-42')) throw Error('lookup evidence missing'); const receipt=workflow_output_emit({kind:'finding',title:'Trigger reference',body:'The seeded reference is REF-42.',payload:{reference:'REF-42'}}); final('Report the finding',{evidence,receipt});"}`,
				`{"javascriptCode":"const evidence=harnessSavedOperation(1); if(!JSON.stringify(evidence).includes('REF-42')) throw Error('saved lookup evidence missing'); const receipt=workflow_output_emit({kind:'finding',title:'Trigger reference',body:'The seeded reference is REF-42.',payload:{reference:'REF-42'}}); final('Report the finding',{receipt});"}`,
				`{"answer":"Recorded the trigger finding for REF-42."}`,
			}
			if resume && req.Model == "harness-trigger-crash-fixture" {
				responses = []string{
					`{"javascriptCode":"final('Report the saved workflow finding',{})"}`,
					`{"javascriptCode":"const receipt=harnessSavedOperation(4),read=harnessSavedOperation(3); if(!JSON.stringify(receipt).includes('outputId')) throw Error('saved output missing'); if(read.result.content.length!==52008||!read.result.content.endsWith('LEAD-42\\n')) throw Error('large saved read missing after restart'); final('Report saved finding',{receipt,readBytes:read.result.content.length});"}`,
					`{"answer":"Recovered the recorded trigger finding for REF-42."}`,
				}
			} else if req.Model == "harness-trigger-crash-fixture" {
				responses[2] = `{"javascriptCode":"const written=file_write({path:'large.csv',content:'lead_id\\n'+'LEAD-42\\n'.repeat(6500)}); const read=file_read({path:'large.csv'}); if(!read.reference||read.result_bytes<52008) throw Error('large file read was not referenced'); const receipt=workflow_output_emit({kind:'finding',title:'Trigger reference',body:'The seeded reference is REF-42.',payload:{reference:'REF-42'}}); final('Report the finding',{receipt,readBytes:read.result_bytes});"}`
			}
		} else if req.Model == "harness-workflow-action-fixture" {
			responses = []string{
				`{"javascriptCode":"final('Propose the installed action and emit a finding',{})"}`,
				`{"javascriptCode":"const request=propose({action:'harness_workflow_effect_fixture',arguments:{value:42},summary:'Update the synthetic reference'}); if(request.status!=='pending_approval') throw Error('approval missing'); const output=workflow_output_emit({kind:'finding',title:'Action proposed',body:'The synthetic action awaits approval.'}); final('Report the pending action and finding',{request,output});"}`,
				`{"answer":"Recorded one finding and one action request pending approval."}`,
			}
		} else if req.Model == "harness-job-child-fixture" || req.Model == "harness-job-child-crash-fixture" || req.Model == "harness-job-child-disabled-fixture" {
			if req.Model == "harness-job-child-disabled-fixture" && n >= 2 {
				http.Error(w, "disabled child fixture has no permitted continuation", http.StatusForbidden)
				return
			}
			responses = []string{
				`{"javascriptCode":"final('Delegate the seeded reference check',{})"}`,
				`{"javascriptCode":"const child=team.researcher({question:'Find the seeded reference'}); if(!JSON.stringify(child).includes('REF-42')) throw Error('child evidence missing'); final('Report the reference',{child});"}`,
				`{"javascriptCode":"final('Find the reference',{})"}`,
				`{"javascriptCode":"const result=lookup('Find the seeded reference'); const evidence=result.reference?harnessSavedOperation(1).result:result; const reply=evidence.response||evidence; final('Report the reference',{answer:reply.answer,trace_id:reply.trace_id,data:reply.data});"}`,
				`{"answer":"Child verified REF-42."}`,
				`{"answer":"Verified REF-42 through the child investigation."}`,
			}
		} else if req.Model == "harness-job-model-only-fixture" {
			responses = []string{
				`{"javascriptCode":"final('Answer without tools',{})"}`,
				`{"javascriptCode":"final('No tools needed',{})"}`,
				`{"answer":"No tools needed."}`,
			}
		} else if req.Model == "harness-memory-save-fixture" {
			responses = []string{`{"javascriptCode":"final('Save the operator rule', {})"}`, `{"javascriptCode":"const receipt=memory_save({text:'Never close a lead without a verified owner',kind:'business_rule',scope:'thread'}); final('Report saved memory',{receipt});"}`, `{"answer":"Saved the operator rule."}`}
		} else if req.Model == "harness-skill-create-fixture" {
			responses = []string{`{"javascriptCode":"final('Create the requested skill', {})"}`, `{"javascriptCode":"const receipt=skill_create({name:'fixture-lead-review',description:'Review a lead CSV',body:'Read the selected CSV and report the owner for each lead.',files:[{path:'scripts/check.py',content:'print(\"skill fixture\")\\n'}]}); final('Report the created skill',{receipt});"}`, `{"answer":"Created the fixture-lead-review skill."}`}
		} else if req.Model == "harness-skill-read-fixture" {
			responses = []string{`{"javascriptCode":"final('Read the installed skill', {})"}`, `{"javascriptCode":"const skill=skill_read({path:'fixture-lead-review/SKILL.md'}); if(!skill.content.includes('Read the selected CSV')) throw Error('installed skill missing'); final('Report installed skill',{skill});"}`, `{"answer":"The installed skill instructs me to read the selected CSV."}`}
		} else if req.Model == "harness-skill-update-fixture" {
			responses = []string{`{"javascriptCode":"final('Update the installed skill', {})"}`, `{"javascriptCode":"const prior=skill_inspect({name:'fixture-lead-review'}); const receipt=skill_update({name:'fixture-lead-review',description:'Review a lead CSV',body:'Verify the lead owner before reporting each row.',files:[{path:'scripts/new_check.py',content:'print(\"updated skill fixture\")'}],expectedVersion:prior.version}); final('Report updated skill',{prior,receipt});"}`, `{"answer":"Updated the fixture-lead-review skill."}`}
		} else if req.Model == "harness-skill-read-updated-fixture" {
			responses = []string{`{"javascriptCode":"final('Read the updated skill', {})"}`, `{"javascriptCode":"const skill=skill_read({path:'fixture-lead-review/SKILL.md'}); if(!skill.content.includes('Verify the lead owner')) throw Error('updated skill missing'); final('Report updated instructions',{skill});"}`, `{"answer":"The updated skill says to verify the lead owner."}`}
		} else if req.Model == "harness-user-admin-fixture" {
			responses = []string{`{"javascriptCode":"final('Request an approved user invitation', {})"}`, `{"javascriptCode":"const request=propose({action:'user_admin',arguments:{action:'invite',email:'harness-invite@example.invalid',role:'member'},summary:'Invite the synthetic team member'}); if(request.status!=='pending_approval') throw Error('admin approval missing'); final('Report the pending invitation',{request});"}`, `{"answer":"The synthetic team invitation is pending administrator approval."}`}
		} else if req.Model == "harness-user-deactivate-fixture" {
			responses = []string{`{"javascriptCode":"final('Request an approved member deactivation', {})"}`, `{"javascriptCode":"const users=JSON.parse(mcp_neko_user_manager_list_users({}).content[0]).users; const user=users.find(u=>u.email==='harness-invite@example.invalid'); if(!user||user.disabledAt)throw Error('active synthetic member missing'); const request=propose({action:'user_admin',arguments:{action:'deactivate',userId:user.id},summary:'Deactivate the synthetic member'}); if(request.status!=='pending_approval')throw Error('deactivation approval missing'); final('Report pending member deactivation',{request,userId:user.id});"}`, `{"answer":"Deactivating the synthetic member is pending administrator approval."}`}
		} else if req.Model == "harness-user-reactivate-fixture" {
			responses = []string{`{"javascriptCode":"final('Request an approved member reactivation', {})"}`, `{"javascriptCode":"const users=JSON.parse(mcp_neko_user_manager_list_users({}).content[0]).users; const user=users.find(u=>u.email==='harness-invite@example.invalid'); if(!user||!user.disabledAt)throw Error('disabled synthetic member missing'); const request=propose({action:'user_admin',arguments:{action:'reactivate',userId:user.id},summary:'Reactivate the synthetic member'}); if(request.status!=='pending_approval')throw Error('reactivation approval missing'); final('Report pending member reactivation',{request,userId:user.id});"}`, `{"answer":"Reactivating the synthetic member is pending administrator approval."}`}
		} else if req.Model == "harness-user-promote-fixture" {
			responses = []string{`{"javascriptCode":"final('Request an approved member promotion', {})"}`, `{"javascriptCode":"const users=JSON.parse(mcp_neko_user_manager_list_users({}).content[0]).users; const user=users.find(u=>u.email==='harness-invite@example.invalid'); if(!user||user.disabledAt||user.role==='admin')throw Error('active synthetic member missing'); const request=propose({action:'user_admin',arguments:{action:'set_role',userId:user.id,role:'admin'},summary:'Promote the synthetic member'}); if(request.status!=='pending_approval')throw Error('promotion approval missing'); final('Report pending member promotion',{request,userId:user.id});"}`, `{"answer":"Promoting the synthetic member is pending administrator approval."}`}
		} else if req.Model == "harness-data-source-admin-fixture" {
			responses = []string{`{"javascriptCode":"final('Request an approved data-source registration', {})"}`, `{"javascriptCode":"const request=propose({action:'data_source_admin',arguments:{action:'register',name:'harness-source',label:'Synthetic API source',sourceKind:'api'},summary:'Register the synthetic API source'}); if(request.status!=='pending_approval') throw Error('source approval missing'); final('Report the pending source',{request});"}`, `{"answer":"The synthetic API source is pending administrator approval."}`}
		} else if req.Model == "harness-group-admin-fixture" {
			responses = []string{`{"javascriptCode":"final('Request an approved group creation', {})"}`, `{"javascriptCode":"const request=propose({action:'group_admin',arguments:{action:'create_group',name:'Harness Reviewers',description:'Synthetic approval group'},summary:'Create the Harness Reviewers group'}); if(request.status!=='pending_approval') throw Error('group approval missing'); final('Report the pending group',{request});"}`, `{"answer":"The Harness Reviewers group is pending administrator approval."}`}
		} else if req.Model == "harness-group-member-fixture" {
			responses = []string{`{"javascriptCode":"final('Request an approved group membership', {})"}`, `{"javascriptCode":"const unpack=v=>JSON.parse(v.content[0]); const groups=unpack(mcp_neko_user_manager_list_groups({})).groups; const users=unpack(mcp_neko_user_manager_list_users({})).users; const group=groups.find(g=>g.name==='Harness Reviewers'); const user=users.find(u=>u.role==='admin'&&!u.disabledAt); if(!group||!user)throw Error('group or active admin missing'); const request=propose({action:'group_admin',arguments:{action:'add_member',groupId:group.id,userId:user.id},summary:'Add the administrator to Harness Reviewers'}); if(request.status!=='pending_approval')throw Error('membership approval missing'); final('Report pending group membership',{request,groupId:group.id,userId:user.id});"}`, `{"answer":"Adding the administrator to Harness Reviewers is pending approval."}`}
		} else if req.Model == "harness-workflow-list-fixture" {
			responses = []string{`{"javascriptCode":"final('List the saved workflows', {})"}`, `{"javascriptCode":"const workflows=mcp_neko_workflow_builder_list_workflows({limit:5}); final('Report workflows',{workflows});"}`, `{"answer":"The saved workflow is Fixture workflow."}`}
		} else if req.Model == "harness-library-fixture" {
			responses = []string{`{"javascriptCode":"final('Search the library', {})"}`, `{"javascriptCode":"const library=mcp_library_search({query:'find contract'}); final('Report library',{library});"}`, `{"answer":"Fixture contract contains TERMS-42."}`}
		} else if req.Model == "harness-records-fixture" {
			responses = []string{`{"javascriptCode":"final('Browse records apps and shipped blueprints', {})"}`, `{"javascriptCode":"const catalog=mcp_neko_records_browse_catalog({}); const blueprints=mcp_neko_records_browse_blueprints({}); final('Report records catalog',{catalog,blueprints});"}`, `{"answer":"The records catalog contains no generated apps for this test organization; the crm blueprint is available."}`}
		} else if req.Model == "harness-records-data-fixture" {
			responses = []string{`{"javascriptCode":"final('Read the equipment loan and recycle bin', {})"}`, `{"javascriptCode":"const catalog=mcp_neko_records_browse_catalog({app:'equipment'}); const found=mcp_neko_records_find_records({app:'equipment',object:'loan',first:5}); const detail=mcp_neko_records_get_record({app:'equipment',object:'loan',id:'loan-42'}); const recycled=mcp_neko_records_find_recycled_records({app:'equipment',object:'loan'}); const deleted=mcp_neko_records_get_recycled_record({app:'equipment',object:'loan',id:'loan-deleted-42'}); final('Report equipment loan',{catalog,found,detail,recycled,deleted});"}`, `{"answer":"The equipment loan is loan-42, and loan-deleted-42 is in the recycle bin."}`}
		} else if req.Model == "harness-records-action-fixture" {
			responses = []string{`{"javascriptCode":"final('Propose the loan update', {})"}`, `{"javascriptCode":"const receipt=propose({action:'record_update',arguments:{app:'equipment',object:'loan',id:'loan-42',fields:{name:'Updated fixture loan'},expected:{name:'Fixture loan'}},summary:'Rename the synthetic equipment loan'}); if(receipt.status!=='pending_approval') throw Error('approval missing'); final('Report the pending record update',{receipt});"}`, `{"answer":"The loan update is pending human approval."}`}
		} else if req.Model == "harness-records-queue-fixture" {
			responses = []string{`{"javascriptCode":"final('Propose the queued loan update', {})"}`, `{"javascriptCode":"const receipt=propose({action:'record_update',arguments:{app:'equipment',object:'loan',id:'loan-43',fields:{name:'Queued fixture loan updated'},expected:{name:'Queued fixture loan'}},summary:'Rename the queued equipment loan'}); if(receipt.status!=='pending_approval') throw Error('approval missing'); final('Report the queued record update',{receipt});"}`, `{"answer":"The queued loan update is pending human approval."}`}
		} else if req.Model == "harness-records-create-fixture" {
			responses = []string{`{"javascriptCode":"final('Propose the loan creation', {})"}`, `{"javascriptCode":"const receipt=propose({action:'record_create',arguments:{app:'equipment',object:'loan',fields:{name:'Created fixture loan'}},summary:'Create a synthetic equipment loan'}); if(receipt.status!=='pending_approval') throw Error('approval missing'); final('Report pending creation',{receipt});"}`, `{"answer":"The loan creation is pending human approval."}`}
		} else if req.Model == "harness-records-delete-fixture" {
			responses = []string{`{"javascriptCode":"final('Propose the loan deletion', {})"}`, `{"javascriptCode":"const receipt=propose({action:'record_delete',arguments:{app:'equipment',object:'loan',id:'loan-43'},summary:'Recycle the synthetic equipment loan'}); if(receipt.status!=='pending_approval') throw Error('approval missing'); final('Report pending deletion',{receipt});"}`, `{"answer":"The loan deletion is pending human approval."}`}
		} else if req.Model == "harness-records-restore-fixture" {
			responses = []string{`{"javascriptCode":"final('Propose the loan restoration', {})"}`, `{"javascriptCode":"const receipt=propose({action:'record_restore',arguments:{app:'equipment',object:'loan',id:'loan-43'},summary:'Restore the synthetic equipment loan'}); if(receipt.status!=='pending_approval') throw Error('approval missing'); final('Report pending restoration',{receipt});"}`, `{"answer":"The loan restoration is pending human approval."}`}
		} else if req.Model == "harness-installed-plugin-fixture" {
			responses = []string{`{"javascriptCode":"final('Propose the installed plugin effect', {})"}`, `{"javascriptCode":"const receipt=propose({action:'fixture_plugin_effect',arguments:{value:42},summary:'Apply the installed plugin effect'}); if(receipt.status!=='pending_approval') throw Error('approval missing'); final('Report the pending plugin effect',{receipt});"}`, `{"answer":"The installed plugin effect is pending human approval."}`}
		} else {
			responses = []string{`{"javascriptCode":"final('Find the seeded reference', {})"}`, `{"javascriptCode":"const result=lookup('Find the seeded reference'); const evidence=result.reference?harnessSavedOperation(1).result:result; const reply=evidence.response||evidence; final('Report the reference',{answer:reply.answer,trace_id:reply.trace_id,data:reply.data});"}`, `{"answer":"The reference is REF-42."}`}
			if answerQuestion {
				responses = []string{`{"javascriptCode":"final('Use the answered day', {})"}`, `{"javascriptCode":"final('The selected day is 2026-09-15', {})"}`, `{"answer":"The selected day is 2026-09-15."}`}
			}
			if askClarification {
				responses = []string{`{"javascriptCode":"final('Ask for the missing day', {})"}`, `{"javascriptCode":"mcp_neko_interaction_ask_user_question({questions:[{question:'Which day?'}]}); final('Wait for the answer', {});"}`}
			}
			if renderCard {
				responses = []string{`{"javascriptCode":"final('Render a summary card', {})"}`, `{"javascriptCode":"const card=mcp_neko_ui_render_cards({messages:[{version:'v1.0',createSurface:{surfaceId:'fixture-card',catalogId:'urn:openneko:catalog:work:v2',components:[{id:'root',component:'Text',text:'Harness card persisted'}]}}]}); final('Report the card',{card});"}`, `{"answer":"Rendered the summary card."}`}
			}
			if followSkill {
				responses = []string{`{"javascriptCode":"final('Follow the staged skill', {})"}`, `{"javascriptCode":"const skill=skill_read({path:'fixture-task/SKILL.md'}); if(!skill.content.includes('SKILL-MARKER')) throw Error('skill not staged'); const file=file_write({path:'skill-result.csv',content:'day\\n2026-09-15\\n'}); final('Report the skill artifact',{skill,file});"}`, `{"answer":"Created skill-result.csv for 2026-09-15."}`}
			}
			if readUpload {
				responses = []string{`{"javascriptCode":"final('Read the uploaded lead file', {})"}`, `{"javascriptCode":"const hidden=upload_search({query:'OTHER-SECRET'}); const matches=upload_search({query:'lead.csv'}); const file=upload_read({path:matches.paths[0]}); final('Report the uploaded lead',{hidden,matches,file});"}`, `{"answer":"The uploaded lead is LEAD-42."}`}
			}
			if writeArtifact {
				responses = []string{`{"javascriptCode":"final('Create a CSV artifact', {})"}`, `{"javascriptCode":"const hidden=file_search({query:'OTHER-RUN-SECRET'}); const written=file_write({path:'result.csv',content:'lead_id\\nLEAD-42\\n'}); final('Report the CSV artifact',{hidden,written});"}`, `{"answer":"Created result.csv."}`}
			}
			if runProcess {
				script := "import os,pathlib,socket\nassert not os.environ.get('OPENNEKO_BROKER_TOKEN')\nassert not os.environ.get('MODEL_API_KEY')\nassert not os.environ.get('OPENNEKO_PROCESS_CANARY')\nassert not pathlib.Path('hidden.txt').exists()\ntry:\n socket.create_connection(('1.1.1.1',80),timeout=2)\n raise AssertionError('ungranted network')\nexcept OSError:\n pass\nsource=pathlib.Path('lead.csv').read_text()\nassert 'LEAD-42' in source\npathlib.Path('result.csv').write_text(source)\n"
				call := fmt.Sprintf("const result=process_run({language:'python',script:%q,uploads:['lead.csv'],outputs:['result.csv']}); final('Report isolated process artifact',{result});", script)
				encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
				responses = []string{`{"javascriptCode":"final('Process the uploaded lead without model data transfer', {})"}`, string(encoded), `{"answer":"Processed the uploaded lead into result.csv."}`}
			}
			if failProcess {
				script := "import pathlib,sys\npathlib.Path('result.csv').write_text('partial')\nsys.exit(7)\n"
				call := fmt.Sprintf("const result=process_run({language:'python',script:%q,uploads:[],outputs:['result.csv']}); final('Report failed process',{result});", script)
				encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
				responses = []string{`{"javascriptCode":"final('Run the failing process fixture', {})"}`, string(encoded), `{"answer":"The process failed after writing a partial output."}`}
			}
			if runCancelProcess {
				script := "import pathlib,subprocess,time\npathlib.Path('result.csv').write_text('partial')\nsubprocess.Popen(['python3','-c','import time; time.sleep(120)'])\nprint('partial ready',flush=True)\ntime.sleep(120)\n"
				call := fmt.Sprintf("const result=process_run({language:'python',script:%q,uploads:[],outputs:['result.csv']}); final('Report cancelled process',{result});", script)
				encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
				responses = []string{`{"javascriptCode":"final('Run the cancellable isolated process fixture', {})"}`, string(encoded), `{"answer":"The isolated process was cancelled."}`}
			}
			if runOversizeProcess {
				script := "from pathlib import Path\nPath('oversize.bin').write_bytes(b'X' * (17 << 20))\n"
				call := fmt.Sprintf("const result=process_run({language:'python',script:%q,uploads:[],outputs:['oversize.bin']}); final('Report oversized process result',{result});", script)
				encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
				responses = []string{`{"javascriptCode":"final('Run the oversized output fixture', {})"}`, string(encoded), `{"answer":"The oversized output was rejected."}`}
			}
			if runFloodProcess {
				script := "from pathlib import Path\nimport sys\nPath('result.csv').write_text('lead_id\\nLEAD-42\\n')\nsys.stdout.write('L' * (96 << 10))\n"
				call := fmt.Sprintf("const result=process_run({language:'python',script:%q,uploads:[],outputs:['result.csv']}); final('Report bounded process output',{result});", script)
				encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
				responses = []string{`{"javascriptCode":"final('Run the bounded stdout fixture', {})"}`, string(encoded), `{"answer":"The log was bounded and the CSV was published."}`}
			}
			if runLargeProcess {
				script := "from pathlib import Path\nPath('large.bin').write_bytes(b'A' * (8 << 20))\n"
				call := fmt.Sprintf("const result=process_run({language:'python',script:%q,uploads:[],outputs:['large.bin']}); final('Report the large process artifact',{result});", script)
				encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
				responses = []string{`{"javascriptCode":"final('Create a large isolated artifact', {})"}`, string(encoded), `{"answer":"Created large.bin."}`}
			}
			if runOfficeProcess {
				call := fmt.Sprintf("const skill=skill_read({path:'office-fixture/SKILL.md'}); if(!skill.content.includes('OFFICE-SKILL-MARKER')) throw Error('skill not staged'); const result=process_run({language:'python',script:%q,uploads:['lead.csv'],outputs:['leads.xlsx','summary.docx']}); final('Report the Office artifacts',{skill,result});", officeScript)
				encoded, _ := json.Marshal(map[string]string{"javascriptCode": call})
				responses = []string{`{"javascriptCode":"final('Create two Office artifacts from the selected upload', {})"}`, string(encoded), `{"answer":"Created leads.xlsx and summary.docx."}`}
			}
			if readManagement {
				responses = []string{`{"javascriptCode":"final('Inspect management catalogs', {})"}`, `{"javascriptCode":"const users=mcp_neko_user_manager_list_users({}); const groups=mcp_neko_user_manager_list_groups({}); const sources=mcp_neko_data_source_manager_list_data_sources({}); const rules=mcp_neko_rule_builder_list_rules({}); const plugins=mcp_neko_plugin_manager_list_plugins({}); const channels=mcp_neko_channel_manager_list_channels({}); final('Report management catalogs',{users,groups,sources,rules,plugins,channels});"}`, `{"answer":"The management catalogs include the seeded rule and organization data source."}`}
			}
			if readAudit {
				responses = []string{`{"javascriptCode":"final('Inspect audit trail', {})"}`, `{"javascriptCode":"const trail=mcp_neko_audit_audit_trail({limit:20}); final('Report the bound actor audit result',{trail});"}`, `{"answer":"The audit request was checked for this actor."}`}
			}
			if readUploadedLibrary {
				responses = []string{`{"javascriptCode":"final('Search the uploaded library document', {})"}`, `{"javascriptCode":"const matches=mcp_library_search({query:'UPLOAD-LIBRARY-42'}); final('Report uploaded policy evidence',{matches});"}`, `{"answer":"The uploaded policy contains UPLOAD-LIBRARY-42."}`}
			}
			if readSourceConfig {
				responses = []string{`{"javascriptCode":"final('Inspect source configuration metadata', {})"}`, `{"javascriptCode":"const graph=mcp_neko_source_config_manager_describe_source_graph({}); const names=mcp_neko_source_config_manager_list_source_secret_names({}); const specs=mcp_neko_source_config_manager_list_openapi_specs({limit:20}); final('Report source metadata',{graph,names,specs});"}`, `{"answer":"The source metadata includes the synthetic credential name and Fixture source API."}`}
			}
			if createWorkflow {
				responses = []string{`{"javascriptCode":"final('Create the fixture workflow', {})"}`, `{"javascriptCode":"const receipt=workflow_save({name:'Harness review workflow',description:'Review synthetic leads',goal:'Review the synthetic lead',steps:[{id:'review',description:'Check the lead owner'}],triggers:{cron:'0 9 * * *',timezone:'UTC',enabled:false},batch:{columns:[{name:'lead_id',path:'lead.id'}]},expectedVersion:'absent'}); final('Report the saved workflow',{receipt});"}`, `{"answer":"Created the Harness review workflow."}`}
			}
			if editWorkflow {
				responses = []string{`{"javascriptCode":"final('List and revise the fixture workflow', {})"}`, `{"javascriptCode":"const listed=mcp_neko_workflow_builder_list_workflows({limit:20}); function unwrap(v){if(typeof v==='string')return unwrap(JSON.parse(v)); if(v&&v.content&&v.content[0])return unwrap(v.content[0]); if(v&&v.text)return unwrap(v.text); return v;} const item=(unwrap(listed).workflows||[]).find(w=>w.name==='Harness review workflow'); if(!item||!item.versionToken) throw Error('workflow version missing'); const receipt=workflow_save({name:item.name,description:'Reviewed synthetic leads',goal:'Review the synthetic lead',steps:[{id:'review',description:'Check the lead owner and status'}],triggers:{cron:'0 9 * * *',timezone:'UTC',enabled:false},expectedVersion:item.versionToken}); final('Report the updated workflow',{receipt});"}`, `{"answer":"Updated the Harness review workflow."}`}
			}
			if deleteWorkflow || denyWorkflowDelete {
				responses = []string{`{"javascriptCode":"final('List the workflow before deletion', {})"}`, `{"javascriptCode":"const listed=mcp_neko_workflow_builder_list_workflows({limit:20}); function unwrap(v){if(typeof v==='string')return unwrap(JSON.parse(v)); if(v&&v.content&&v.content[0])return unwrap(v.content[0]); if(v&&v.text)return unwrap(v.text); return v;} const item=(unwrap(listed).workflows||[]).find(w=>w.name==='Harness review workflow'); if(!item||!item.versionToken) throw Error('workflow version missing'); const receipt=workflow_delete({workflowId:item.id,name:item.name,expectedVersion:item.versionToken}); final('Report the workflow deletion decision',{receipt});"}`, `{"answer":"The workflow deletion request was checked by the host."}`}
			}
			if createWorkflowWhen {
				responses = []string{`{"javascriptCode":"final('Create the source-change workflow', {})"}`, `{"javascriptCode":"const receipt=workflow_save({name:'Harness source-change workflow',description:'Report a changed reference',steps:[{id:'report',description:'Report the changed reference'}],triggers:{when:{table:'references',primary_key:['id'],select:['label'],enabled:true,idempotency_key_template:'reference:{id}'}},expectedVersion:'absent'}); final('Report the source-change receipt',{receipt});"}`, `{"answer":"Created the source-change workflow."}`}
			}
			if editWorkflowWhen {
				responses = []string{`{"javascriptCode":"final('Edit the source-change workflow', {})"}`, `{"javascriptCode":"const listed=mcp_neko_workflow_builder_list_workflows({limit:20}); function unwrap(v){if(typeof v==='string')return unwrap(JSON.parse(v)); if(v&&v.content&&v.content[0])return unwrap(v.content[0]); if(v&&v.text)return unwrap(v.text); return v;} const item=(unwrap(listed).workflows||[]).find(w=>w.name==='Harness source-change workflow'); if(!item||!item.versionToken) throw Error('workflow version missing'); const receipt=workflow_save({name:item.name,description:'Report the watched reference with a filter',steps:[{id:'report',description:'Report reference 42'}],triggers:{when:{table:'references',primary_key:['id'],select:['label'],where:{id:{eq:42}},enabled:true,idempotency_key_template:'reference:{id}'}},expectedVersion:item.versionToken}); final('Report the updated trigger',{receipt});"}`, `{"answer":"Updated the source-change workflow."}`}
			}
			if createWorkflowWatch {
				responses = []string{`{"javascriptCode":"final('Create the condition watch', {})"}`, `{"javascriptCode":"const receipt=workflow_save({name:'Harness condition watch',description:'Notice when the reference id exceeds 40',steps:[{id:'report',description:'Report the reference condition'}],triggers:{watch:{query:'query { references { id label } }',value_path:'references.0.id',op:'gt',threshold:40,cadence_seconds:60,debounce_seconds:0}},expectedVersion:'absent'}); final('Report the watch receipt',{receipt});"}`, `{"answer":"Created the condition watch."}`}
			}
			if rejectWorkflowTrigger {
				responses = []string{`{"javascriptCode":"final('Try the unsafe trigger', {})"}`, `{"javascriptCode":"const receipt=workflow_save({name:'Harness looped workflow',goal:'Update references when references change',steps:[{id:'update',description:'Update references after a reference changes'}],triggers:{when:{table:'references',primary_key:['id'],enabled:true}},expectedVersion:'absent'}); final('Report the rejected trigger',{receipt});"}`, `{"answer":"The unsafe trigger was rejected without saving a workflow."}`}
			}
			if createRule {
				responses = []string{`{"javascriptCode":"final('Create the fixture rule', {})"}`, `{"javascriptCode":"const receipt=rule_save({name:'Harness governed rule',description:'Require review for synthetic changes',applies_to_kinds:['fixture_rule_action'],applies_to_scopes:['external'],mode:'approval_required',approver_role:'admin',expectedVersion:'absent'}); final('Report the saved rule',{receipt});"}`, `{"answer":"Created the Harness governed rule."}`}
			}
			if editRule {
				responses = []string{`{"javascriptCode":"final('List and revise the fixture rule', {})"}`, `{"javascriptCode":"const listed=mcp_neko_rule_builder_list_rules({limit:20}); function unwrap(v){if(typeof v==='string')return unwrap(JSON.parse(v)); if(v&&v.content&&v.content[0])return unwrap(v.content[0]); if(v&&v.text)return unwrap(v.text); return v;} const item=(unwrap(listed).rules||[]).find(r=>r.name==='Harness governed rule'); if(!item||!item.versionToken) throw Error('rule version missing'); const receipt=rule_save({name:item.name,description:'Auto-approve synthetic low-risk changes',applies_to_kinds:['fixture_rule_action'],applies_to_scopes:['external'],mode:'auto_approve',risk_threshold_auto_approve:'low',limits:{daily_cap:2},approver_role:'admin',expectedVersion:item.versionToken}); final('Report the updated rule',{receipt});"}`, `{"answer":"Updated the Harness governed rule."}`}
			}
			if propose {
				responses = []string{`{"javascriptCode":"final('Request approval for the fixture', {})"}`, `{"javascriptCode":"const receipt=propose({action:'harness_effect_fixture',arguments:{value:42},summary:'Update the synthetic value'}); final('Report the pending approval',{receipt});"}`, `{"answer":"Approval requested for the synthetic change; it has not executed."}`}
				if n == 2 && !strings.Contains(string(req.Messages), "pending_approval") {
					http.Error(w, "missing approval receipt", 422)
					return
				}
			}

		}
		if n == 2 && req.Model != "graphjin-fixture" && effects {
			answer := "The reference is REF-42."
			for _, fence := range []struct {
				name string
				body any
			}{
				{"neko_action_request", map[string]any{"scope": "external", "kind": "fixture_action", "target": "fixture:blocked", "payload": map[string]any{"text": "blocked"}, "risk_level": "low", "summary": "Must not execute"}},
				{"neko_workflow_save", map[string]any{"name": "Blocked fixture workflow", "steps": []any{map[string]any{"id": "fixture", "description": "Must not execute"}}}},
				{"neko_rule_save", map[string]any{"name": "Blocked fixture policy", "applies_to_kinds": []string{"fixture_action"}, "applies_to_scopes": []string{"external"}, "mode": "auto_approve", "risk_threshold_auto_approve": "low"}},
				{"neko_memory", []any{map[string]any{"save": map[string]any{"text": "Blocked fixture memory", "scope": "global"}}}},
			} {
				body, _ := json.Marshal(fence.body)
				answer += "\n```" + fence.name + "\n" + string(body) + "\n```"
			}
			encoded, _ := json.Marshal(map[string]string{"answer": answer})
			responses[n] = string(encoded)
		}
		if n == 2 && refused {
			responses[n] = `{"answer":"The lookup was refused because the data agent is not configured read-only."}`
		}
		if n >= len(responses) {
			http.Error(w, "fixture exhausted", 400)
			return
		}
		if req.Model == "harness-stream-fixture" && n == 2 && req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unavailable", 500)
				return
			}
			for index, part := range []string{"Answer: STREAM-", "OK"} {
				chunk, _ := json.Marshal(map[string]any{"id": "harness-stream", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}, "finish_reason": nil}}})
				fmt.Fprintf(w, "data: %s\n\n", chunk)
				flusher.Flush()
				if index == 0 {
					select {
					case <-time.After(time.Second):
					case <-r.Context().Done():
						return
					}
				}
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			flusher.Flush()
			return
		}
		fmt.Printf("model=%s step=%d\n", req.Model, n)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": responses[n]}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	}
	http.HandleFunc("/v1/chat/completions", modelHandler)
	for _, stage := range []string{"context", "context-spare", "context-503", "context-403", "context-429", "skill", "context-lookup", "executor", "executor-base", "executor-strong", "executor-lookup", "responder", "responder-lookup"} {
		http.HandleFunc("/route/"+stage+"/v1/chat/completions", modelHandler)
	}
	panic(http.ListenAndServe(":8080", nil))
}
