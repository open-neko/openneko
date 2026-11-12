package webfront

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const (
	packBodyLimit   = 64 << 10
	packUploadLimit = 16 << 20
)

var (
	packReadActions  = []string{"inspect", "plan", "status", "doctor"}
	packWriteActions = []string{"review", "install", "configure", "upgrade", "uninstall"}
	errPackTooLarge  = errors.New("request is too large")
)

func readPackBody(r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, errPackTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errPackTooLarge
	}
	return b, nil
}

// relayPackWorker is requestPackWorker() with the routes' catch.
func (a *API) relayPackWorker(ctx context.Context, w http.ResponseWriter, method, path string, header http.Header, body []byte) {
	status, reply, err := a.workerSend(ctx, method, path, workerAdminTimeout, header, body)
	if err != nil {
		log.Printf("pack worker %s %s: %v", method, path, err)
		sendError(w, http.StatusBadGateway, "worker is unavailable")
		return
	}
	parsed, err := jsParse(reply)
	if err != nil {
		sendError(w, status, fmt.Sprintf("Worker returned HTTP %d", status))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(jsStringify(parsed, false)))
}

func packObjectBody(w http.ResponseWriter, r *http.Request) (*jsObj, bool) {
	raw, err := readPackBody(r, packBodyLimit)
	if errors.Is(err, errPackTooLarge) {
		sendError(w, http.StatusRequestEntityTooLarge, err.Error())
		return nil, false
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, "request body must be valid JSON")
		return nil, false
	}
	if len(raw) == 0 {
		return &jsObj{vals: map[string]any{}}, true
	}
	parsed, err := jsParse(raw)
	if err != nil {
		sendError(w, http.StatusBadRequest, "request body must be valid JSON")
		return nil, false
	}
	obj, ok := parsed.(*jsObj)
	if !ok {
		sendError(w, http.StatusBadRequest, "request body must be an object")
		return nil, false
	}
	return obj, true
}

var jsonHeader = http.Header{"Content-Type": {"application/json"}}

func (a *API) adminPacks(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); ok {
		a.relayPackWorker(context.WithoutCancel(r.Context()), w, http.MethodGet, "/admin/packs", nil, nil)
	}
}

func (a *API) adminPackUpload(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	if mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(mediaType) != "application/zip" {
		sendError(w, http.StatusUnsupportedMediaType, "Choose a ZIP pack archive.")
		return
	}
	bytes, err := readPackBody(r, packUploadLimit)
	if errors.Is(err, errPackTooLarge) {
		sendError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		sendError(w, http.StatusBadRequest, err.Error())
		return
	}
	header := http.Header{"Content-Type": {"application/zip"}, "X-Openneko-Actor": {encodeURIComponent(act.UserID)}}
	a.relayPackWorker(r.Context(), w, http.MethodPost, "/admin/packs/upload", header, bytes)
}

func (a *API) adminPackRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	packID, action := r.PathValue("packId"), r.PathValue("action")
	if !packIDPattern.MatchString(packID) || !slices.Contains(packReadActions, action) {
		sendError(w, http.StatusNotFound, "unknown pack operation")
		return
	}
	path := "/admin/packs/" + encodeURIComponent(packID) + "/" + action
	if version := r.URL.Query().Get("version"); version != "" {
		path += "?version=" + encodeURIComponent(version)
	}
	a.relayPackWorker(context.WithoutCancel(r.Context()), w, http.MethodGet, path, nil, nil)
}

func (a *API) adminPackWrite(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	packID, action := r.PathValue("packId"), r.PathValue("action")
	if !packIDPattern.MatchString(packID) || !slices.Contains(packWriteActions, action) {
		sendError(w, http.StatusNotFound, "unknown pack operation")
		return
	}
	body, ok := packObjectBody(w, r)
	if !ok {
		return
	}
	body.set("actorUserId", nullable(act.UserID))
	key, isString := body.vals["idempotencyKey"].(string)
	if !isString {
		key = uuid.NewString()
	}
	body.set("idempotencyKey", key)
	a.relayPackWorker(context.WithoutCancel(r.Context()), w, http.MethodPost, "/admin/packs/"+encodeURIComponent(packID)+"/"+action,
		jsonHeader, []byte(jsStringify(body, false)))
}

const storeManagementPath = "/admin/packs/magento/store-management"

func (a *API) adminStoreManagement(w http.ResponseWriter, r *http.Request) {
	act, ok := a.settingsAdmin(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if r.Method != http.MethodPost {
		a.relayPackWorker(ctx, w, http.MethodGet, storeManagementPath, nil, nil)
		return
	}
	body, ok := packObjectBody(w, r)
	if !ok {
		return
	}
	body.set("actorUserId", nullable(act.UserID))
	a.relayPackWorker(ctx, w, http.MethodPost, storeManagementPath, jsonHeader, []byte(jsStringify(body, false)))
}
