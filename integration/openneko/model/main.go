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
	clarification := false
	card := false
	skill := false
	answerClarification := false
	continuation := false
	pauseResponder := false
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
			Delay               int  `json:"delay"`
			EffectFences        bool `json:"effect_fences"`
			Proposal            bool `json:"proposal"`
			Upload              bool `json:"upload"`
			Artifact            bool `json:"artifact"`
			Clarification       bool `json:"clarification"`
			Card                bool `json:"card"`
			Skill               bool `json:"skill"`
			AnswerClarification bool `json:"answer_clarification"`
			Continue            bool `json:"continue"`
			PauseResponder      bool `json:"pause_responder"`
		}
		if json.NewDecoder(r.Body).Decode(&c) != nil || c.Delay < 0 || c.Delay > 30 {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		if !c.Continue {
			counts = map[string]int{}
		}
		continuation = c.Continue
		pauseResponder = c.PauseResponder
		delay = c.Delay
		effectFences = c.EffectFences
		proposal = c.Proposal
		upload = c.Upload
		artifact = c.Artifact
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
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
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
		askClarification := clarification
		renderCard := card
		followSkill := skill
		answerQuestion := answerClarification
		resume := continuation
		n := counts[req.Model]
		if pauseResponder && req.Model == "harness-fixture" && n == 2 {
			wait = 30
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
		refused := strings.Contains(string(req.Messages), "not configured read-only") && !strings.Contains(string(req.Messages), "trace_id")
		if n == 2 && req.Model == "harness-memory-fixture" && !strings.Contains(string(req.Messages), "memory-1") {
			http.Error(w, "missing memory evidence", 422)
			return
		}
		if n == 2 && req.Model == "harness-memory-save-fixture" && !strings.Contains(string(req.Messages), "memoryId") {
			http.Error(w, "missing saved memory receipt", 422)
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
		if n == 2 && req.Model == "harness-fixture" && readUpload && !strings.Contains(string(req.Messages), "LEAD-42") {
			http.Error(w, "missing uploaded file evidence", 422)
			return
		}
		artifactHash := sha256.Sum256([]byte("lead_id\nLEAD-42\n"))
		if n == 2 && req.Model == "harness-fixture" && writeArtifact && !strings.Contains(string(req.Messages), hex.EncodeToString(artifactHash[:])) {
			http.Error(w, "missing created artifact receipt", 422)
			return
		}
		if n == 2 && req.Model != "harness-memory-fixture" && req.Model != "harness-memory-save-fixture" && req.Model != "harness-library-fixture" && req.Model != "harness-records-fixture" && req.Model != "harness-records-data-fixture" && !readUpload && !writeArtifact && !propose && !answerQuestion && !renderCard && !followSkill && !refused && (!strings.Contains(string(req.Messages), "REF-42") || (req.Model != "graphjin-fixture" && !strings.Contains(string(req.Messages), "trace_id"))) {
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
		} else if req.Model == "harness-memory-save-fixture" {
			responses = []string{`{"javascriptCode":"final('Save the operator rule', {})"}`, `{"javascriptCode":"const receipt=memory_save({text:'Never close a lead without a verified owner',kind:'business_rule',scope:'thread'}); final('Report saved memory',{receipt});"}`, `{"answer":"Saved the operator rule."}`}
		} else if req.Model == "harness-library-fixture" {
			responses = []string{`{"javascriptCode":"final('Search the library', {})"}`, `{"javascriptCode":"const library=mcp_library_search({query:'find contract'}); final('Report library',{library});"}`, `{"answer":"Fixture contract contains TERMS-42."}`}
		} else if req.Model == "harness-records-fixture" {
			responses = []string{`{"javascriptCode":"final('Browse records apps and shipped blueprints', {})"}`, `{"javascriptCode":"const catalog=mcp_neko_records_browse_catalog({}); const blueprints=mcp_neko_records_browse_blueprints({}); final('Report records catalog',{catalog,blueprints});"}`, `{"answer":"The records catalog contains no generated apps for this test organization; the crm blueprint is available."}`}
		} else if req.Model == "harness-records-data-fixture" {
			responses = []string{`{"javascriptCode":"final('Read the equipment loan and recycle bin', {})"}`, `{"javascriptCode":"const catalog=mcp_neko_records_browse_catalog({app:'equipment'}); const found=mcp_neko_records_find_records({app:'equipment',object:'loan',first:5}); const detail=mcp_neko_records_get_record({app:'equipment',object:'loan',id:'loan-42'}); const recycled=mcp_neko_records_find_recycled_records({app:'equipment',object:'loan'}); const deleted=mcp_neko_records_get_recycled_record({app:'equipment',object:'loan',id:'loan-deleted-42'}); final('Report equipment loan',{catalog,found,detail,recycled,deleted});"}`, `{"answer":"The equipment loan is loan-42, and loan-deleted-42 is in the recycle bin."}`}
		} else {
			responses = []string{`{"javascriptCode":"final('Find the seeded reference', {})"}`, `{"javascriptCode":"const evidence=lookup('Find the seeded reference'); final('Report the reference', {evidence});"}`, `{"answer":"The reference is REF-42."}`}
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
		fmt.Printf("model=%s step=%d\n", req.Model, n)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": responses[n]}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
	})
	panic(http.ListenAndServe(":8080", nil))
}
