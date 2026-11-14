package webfront

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

// WorkRun is the work_run job: the chat turn of a channel message, run as a
// web question runs, then the skill learning passes and the reply to the
// channel. After the turn starts the job returns no error, so River never
// repeats a paid turn.
func (a *API) WorkRun(ctx context.Context, job jobs.WorkRunArgs) error {
	var status string
	err := a.DB.QueryRow(ctx, `select status from work_run where org_id = $1 and id::text = $2`, job.OrgID, job.RunID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Printf("[work-run] run %s not found for thread %s; skipping stale job", job.RunID, job.ThreadID)
		return nil
	}
	if err != nil {
		return err
	}
	if status != "queued" {
		log.Printf("[work-run] run %s is %s; skipping repeated job", job.RunID, status)
		if job.ProcessingJobID != "" && a.runs.get(job.RunID) == nil {
			if _, err := a.DB.Exec(ctx, `update processing_job set status = 'failed', finished_at = now(), error = $2, updated_at = now()
				where id::text = $1 and status in ('queued', 'running')`, job.ProcessingJobID, "The run was already "+status+" when the job ran again."); err != nil {
				return err
			}
		}
		return nil
	}
	a.markProcessingJob(ctx, job.ProcessingJobID, "running", "")
	res, runErr, err := a.runQueuedChatTurn(ctx, job)
	if err != nil {
		a.markProcessingJob(context.WithoutCancel(ctx), job.ProcessingJobID, "failed", err.Error())
		log.Printf("[work-run] run %s not started; River may retry: %v", job.RunID, err)
		return err
	}
	bg := context.WithoutCancel(ctx)
	if runErr != nil {
		a.markProcessingJob(bg, job.ProcessingJobID, "failed", runErr.Error())
	} else {
		a.markProcessingJob(bg, job.ProcessingJobID, "succeeded", "")
	}
	if err := a.queueRunSkillLearning(bg, job.OrgID, job.RunID); err != nil {
		log.Printf("[work-run] skill learn enqueue failed for %s: %v", job.RunID, err)
	}
	if runErr != nil || job.ChannelPlugin == "" || !jsonObject(job.Recipient) || (res.Status != "completed" && res.Status != "needs_input") {
		return nil
	}
	if err := a.queueChatReply(bg, job, res.FinalText); err != nil {
		log.Printf("[work-run] reply for run %s not queued: %v", job.RunID, err)
	}
	return nil
}

func (a *API) runQueuedChatTurn(ctx context.Context, job jobs.WorkRunArgs) (chatTurnResult, error, error) {
	if err := a.ensureHostConfigProvisioned(ctx, job.OrgID); err != nil {
		return chatTurnResult{}, nil, err
	}
	packActions, err := a.packActionDescriptors(ctx, job.OrgID)
	if err != nil {
		return chatTurnResult{}, nil, err
	}
	channel := job.Channel
	if channel == "" {
		channel = "web"
	}
	var res chatTurnResult
	var runErr error
	o := chatTurnOptions{OrgID: job.OrgID, ThreadID: job.ThreadID, RunID: job.RunID, Message: job.Message, Channel: channel,
		PackActions: packActions, resolve: a.turnResolve, runCore: a.turnCore,
		finished: func(r chatTurnResult, err error) { res, runErr = r, err }}
	done, err := a.startChatRun(ctx, o)
	if err != nil {
		return chatTurnResult{}, nil, err
	}
	select {
	case <-done:
	case <-ctx.Done():
		a.abortRun(job.RunID)
		<-done
	}
	return res, runErr, nil
}

func (a *API) markProcessingJob(ctx context.Context, id, status, message string) {
	if id == "" {
		return
	}
	var err error
	switch status {
	case "running":
		_, err = a.DB.Exec(ctx, `update processing_job set status = 'running', started_at = now(), updated_at = now() where id::text = $1`, id)
	case "succeeded":
		_, err = a.DB.Exec(ctx, `update processing_job set status = 'succeeded', finished_at = now(), error = null, updated_at = now() where id::text = $1`, id)
	default:
		_, err = a.DB.Exec(ctx, `update processing_job set status = 'failed', finished_at = now(), error = $2, updated_at = now() where id::text = $1`, id, message)
	}
	if err != nil {
		log.Printf("[jobs] could not mark job %s %s: %v", id, status, err)
	}
}

func (a *API) queueRunSkillLearning(ctx context.Context, orgID, runID string) error {
	if a.Jobs == nil {
		return errors.New("the job queue is not ready")
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `select skill_name from skill_usage where run_id::text = $1`, runID)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := a.Jobs.SkillLearn(ctx, tx, orgID, name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func jsonObject(raw json.RawMessage) bool {
	var v map[string]any
	return json.Unmarshal(raw, &v) == nil && v != nil
}

// queueChatReply is deliverChatReply() of the worker's channel delivery: the
// answer goes back as the assistant's message, with the run's surfaces, or
// cards built from its vitals, for a channel that can show them.
func (a *API) queueChatReply(ctx context.Context, job jobs.WorkRunArgs, body string) error {
	events, err := a.storedRunEvents(ctx, job.OrgID, job.RunID)
	if err != nil {
		return err
	}
	event := chatReplyEvent(job.RunID, body, events)
	if event == nil {
		return nil
	}
	return a.queueChannelDelivery(ctx, job.OrgID, job.ChannelPlugin, job.Recipient, []any{event}, "reply-"+job.RunID)
}

func chatReplyEvent(runID, body string, events []any) *jsObj {
	var surfaces []any
	var vitals []any
	for _, e := range events {
		switch jsGet(e, "type") {
		case "surface":
			surfaces = append(surfaces, jsArray(jsGet(e, "messages"))...)
		case "vitals":
			if items, ok := jsGet(e, "items").([]any); ok {
				vitals = items
			}
		}
	}
	if len(surfaces) == 0 && len(vitals) > 0 {
		surfaces = replySurface(body, vitals)
	}
	if jsTrim(body) == "" && len(surfaces) == 0 {
		return nil
	}
	event := jsObjOf("kind", "converse", "id", runID, "role", "assistant", "text", body)
	if len(surfaces) > 0 {
		event.set("enrichment", jsObjOf("surfaces", surfaces))
	}
	return event
}

// replySurface is buildReplySurface(): the answer as Markdown and one
// MetricCard per vital.
func replySurface(body string, vitals []any) []any {
	var children []any
	hasBody := jsTrim(body) != ""
	if hasBody {
		children = append(children, "answer-body")
	}
	var cards []any
	for i, v := range vitals {
		id := "vital-" + strconv.Itoa(i)
		card := jsObjOf("id", id, "component", "MetricCard", "metric", jsGet(v, "value"), "label", jsGet(v, "label"))
		if sub, ok := jsGet(v, "sub").(string); ok && sub != "" {
			card.set("detail", sub)
		}
		children = append(children, id)
		cards = append(cards, card)
	}
	components := []any{jsObjOf("id", "root", "component", "Answer", "children", children)}
	if hasBody {
		components = append(components, jsObjOf("id", "answer-body", "component", "Markdown", "text", body))
	}
	components = append(components, cards...)
	return []any{
		jsObjOf("version", "v1.0", "createSurface", jsObjOf("surfaceId", "reply", "catalogId", "urn:app:catalog:answer:v1")),
		jsObjOf("version", "v1.0", "updateComponents", jsObjOf("surfaceId", "reply", "components", components)),
	}
}

func (a *API) queueChannelDelivery(ctx context.Context, orgID, channel string, recipient json.RawMessage, events []any, key string) error {
	if a.Jobs == nil {
		return errors.New("the job queue is not ready")
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := a.Jobs.ChannelDeliver(ctx, tx, jobs.ChannelDeliverArgs{OrgID: orgID, ChannelPlugin: channel, Recipient: recipient,
		Events: json.RawMessage(jsStringify(events, false)), Key: key}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ChannelDeliver is the channel_deliver job. The worker sends on the
// channel; any answer but a delivery fails the attempt, and River tries
// again.
func (a *API) ChannelDeliver(ctx context.Context, job jobs.ChannelDeliverArgs) error {
	body, err := json.Marshal(map[string]json.RawMessage{"recipient": job.Recipient, "events": job.Events})
	if err != nil {
		return err
	}
	status, reply, err := a.workerDo(ctx, http.MethodPost, "/admin/channels/"+url.PathEscape(job.ChannelPlugin)+"/deliver", jobs.ChannelDeliverTimeout, body)
	if err != nil {
		return fmt.Errorf("channel-delivery: %s: the worker did not answer: %w", job.ChannelPlugin, err)
	}
	var out struct {
		Delivered bool   `json:"delivered"`
		Ref       string `json:"ref"`
		Error     string `json:"error"`
	}
	_ = json.Unmarshal(reply, &out)
	if status != http.StatusOK {
		if out.Error == "" {
			out.Error = http.StatusText(status)
		}
		return fmt.Errorf("channel-delivery: %s failed (%d): %s", job.ChannelPlugin, status, out.Error)
	}
	if !out.Delivered {
		return fmt.Errorf("channel-delivery: %s reported delivered=false", job.ChannelPlugin)
	}
	ref := ""
	if out.Ref != "" {
		ref = " ref=" + out.Ref
	}
	log.Printf("[channel-delivery] %s delivered=true%s", job.ChannelPlugin, ref)
	return nil
}
