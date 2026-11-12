package webfront

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

const (
	WakePath       = "/internal/jobs/wake"
	wakeDefault    = 50 * time.Second
	wakeMaxSeconds = 300
)

// Wake answers WakePath for an outside scheduler, so the jobs of a web that
// gets no processor time between requests still run. It sits outside the
// request gate and the /api/v1 contract.
type Wake struct {
	// Token is the bearer token a call must carry; "" refuses every call.
	Token string
	Queue *jobs.Queue
	// JobsOn is true when this web runs the jobs; a call then only inserts
	// the due cron jobs.
	JobsOn bool
}

func (h *Wake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if h.Token == "" || !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(h.Token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="jobs-wake"`)
		writeJSON(w, http.StatusUnauthorized, "a valid wake token is required")
		return
	}
	limit := wakeDefault
	if raw := r.URL.Query().Get("max_seconds"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > wakeMaxSeconds {
			writeJSON(w, http.StatusBadRequest, "max_seconds must be a whole number from 1 to 300")
			return
		}
		limit = time.Duration(n) * time.Second
	}
	res, err := h.Queue.Wake(r.Context(), limit, !h.JobsOn)
	if err != nil {
		log.Printf("[jobs-wake] %v", err)
		writeJSON(w, http.StatusInternalServerError, "the wake call failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
