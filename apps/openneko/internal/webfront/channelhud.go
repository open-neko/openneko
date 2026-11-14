package webfront

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// hudChannelURL is the alias every demo pack gives its bridge on the
// shared Docker network.
var (
	hudChannelURL = "http://demo-channel:443"
	hudNow        = time.Now
	hudRandomID   = randomUUID
)

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func hudWorkspace(env map[string]string) string {
	if w := jsTrim(env["HUD_CHANNEL_WORKSPACE"]); w != "" {
		return w
	}
	return "demo"
}

func signHUDBody(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// jsNumberOf is Number(value) for the JSON values a HUD anchor may carry;
// ok is false for NaN and the infinities.
func jsNumberOf(v any, present bool) (float64, bool) {
	if !present {
		return 0, false
	}
	var f float64
	switch t := v.(type) {
	case nil:
		f = 0
	case bool:
		if t {
			f = 1
		}
	case float64:
		f = t
	case string:
		s := jsTrim(t)
		if s == "" {
			f = 0
			break
		}
		parsed, err := strconv.ParseFloat(s, 64)
		if err != nil || strings.ContainsAny(s, "_xXpP") && !strings.HasPrefix(strings.ToLower(s), "0x") {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

func hudRecord(v any) *jsObj {
	if obj, ok := v.(*jsObj); ok && obj != nil {
		return obj
	}
	return &jsObj{vals: map[string]any{}}
}

func hudAnchor(event, recipient any) *jsObj {
	source := hudRecord(event)
	spatial, has := source.vals["spatial"]
	if !has || spatial == nil {
		spatial = hudRecord(source.vals["enrichment"]).vals["spatial"]
	}
	candidate := hudRecord(spatial)
	lon, lonOK := candidate.vals["longitude"]
	lat, latOK := candidate.vals["latitude"]
	longitude, okLon := jsNumberOf(lon, lonOK)
	latitude, okLat := jsNumberOf(lat, latOK)
	if okLon && okLat {
		kind, _ := candidate.vals["kind"].(string)
		if kind == "" {
			kind = "coordinate"
		}
		anchor := jsObjOf("kind", kind)
		if id, _ := candidate.vals["id"].(string); id != "" {
			anchor.set("id", id)
		}
		anchor.set("longitude", longitude)
		anchor.set("latitude", latitude)
		return anchor
	}
	for _, item := range jsArray(source.vals["evidence"]) {
		ref, _ := hudRecord(item).vals["ref"].(string)
		if sep := strings.Index(ref, ":"); sep > 0 && sep < len(ref)-1 {
			return jsObjOf("kind", ref[:sep], "id", ref[sep+1:])
		}
	}
	if def, _ := hudRecord(recipient).vals["defaultAnchor"].(string); def != "" {
		if sep := strings.Index(def, ":"); sep > 0 {
			return jsObjOf("kind", def[:sep], "id", def[sep+1:])
		}
		return jsObjOf("kind", "workspace", "id", def)
	}
	workspace, _ := hudRecord(recipient).vals["workspace"].(string)
	if workspace == "" {
		workspace = "demo"
	}
	return jsObjOf("kind", "workspace", "id", workspace)
}

func setDefined(o *jsObj, key string, event *jsObj) {
	if v, ok := event.vals[key]; ok {
		o.set(key, v)
	}
}

func orTruthy(v any, fallback any) any {
	if jsTruthy(v) {
		return v
	}
	return fallback
}

// hudProjection is projectEvent() in packages/channels/src/hud/projection.ts.
func hudProjection(event, recipient any) *jsObj {
	e := hudRecord(event)
	id := e.vals["id"]
	if !jsTruthy(id) {
		id = hudRandomID()
	}
	out := jsObjOf("id", id, "kind", e.vals["kind"], "anchor", hudAnchor(event, recipient))
	field := func(key string) any { return e.vals[key] }
	switch e.vals["kind"] {
	case "converse":
		out.set("type", "message")
		out.set("title", "OpenNeko")
		setDefined(out, "body", jsObjOf("body", field("text")))
	case "progress":
		out.set("type", "progress")
		out.set("title", field("label"))
		setDefined(out, "phase", e)
	case "ask":
		out.set("type", "decision")
		out.set("title", "Operator decision")
		out.set("body", field("prompt"))
		out.set("ask", field("ask"))
		out.set("decisionRef", field("decisionRef"))
		out.set("risk", orTruthy(field("risk"), "medium"))
		out.set("options", orTruthy(field("options"), []any{}))
	case "resolve":
		out.set("type", "resolution")
		out.set("title", field("status"))
		out.set("body", field("summary"))
		out.set("decisionRef", field("ref"))
		out.set("status", field("status"))
	case "offer":
		out.set("type", "artifact")
		out.set("title", field("label"))
		out.set("artifactRef", field("artifactRef"))
		setDefined(out, "mime", e)
	case "highlight":
		out.set("type", "figures")
		out.set("title", "Key figures")
		out.set("metrics", field("metrics"))
	case "inform":
		out.set("type", "finding")
		out.set("title", field("title"))
		out.set("body", field("body"))
		out.set("mood", field("mood"))
		out.set("evidence", orTruthy(field("evidence"), []any{}))
		for _, k := range []string{"metric", "series", "freshness"} {
			setDefined(out, k, e)
		}
	}
	return out
}

// deliverHUD is deliverHud() in packages/channels/src/hud/channel.ts.
func deliverHUD(ctx context.Context, recipient any, events []any, env map[string]string) (channelDeliverResult, error) {
	if len(events) == 0 {
		return channelDeliverResult{}, nil
	}
	deliveryID := hudRandomID()
	projections := make([]any, len(events))
	for i, e := range events {
		projections[i] = hudProjection(e, recipient)
	}
	envelope := jsObjOf("schema", "openneko.hud.delivery.v1", "deliveryId", deliveryID,
		"deliveredAt", jsTime(hudNow()), "workspace", hudWorkspace(env), "recipient", recipient, "projections", projections)
	body := jsStringify(envelope, false)
	secret := jsTrim(env["HUD_CHANNEL_SECRET"])
	if secret == "" {
		return channelDeliverResult{}, errors.New("HUD_CHANNEL_SECRET is required")
	}
	raw, status, err := postChannelBody(ctx, hudChannelURL+"/api/deliver", map[string]string{
		"Content-Type":               "application/json",
		"x-openneko-hud-delivery-id": deliveryID,
		"x-openneko-hud-signature":   "sha256=" + signHUDBody(body, secret),
	}, []byte(body), 15*time.Second)
	if err != nil {
		return channelDeliverResult{}, err
	}
	if status < 200 || status > 299 {
		return channelDeliverResult{}, fmt.Errorf("HUD bridge returned %d: %s", status, jsSlice(string(raw), 240))
	}
	return channelDeliverResult{Delivered: true, Ref: deliveryID}, nil
}
