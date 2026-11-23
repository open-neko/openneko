package gjhost

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/dosco/graphjin/serv/v3"
)

// CheckDir is the folder beside the config file where the control zone
// writes candidate configs for CheckHandler.
const CheckDir = ".openneko-check"

// CheckConfig loads a config file with the pinned GraphJin loader and runs
// the compiler's config checks, as GraphJin does when it starts.
func CheckConfig(file string) error {
	conf, err := serv.ReadInConfig(file)
	if err != nil {
		return err
	}
	return conf.Core.Validate()
}

var candidateID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type checkRequest struct {
	Candidate string `json:"candidate"`
}

type checkResult struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
}

// CheckHandler checks the candidate <dir of configFile>/.openneko-check/<id>/<config name>.
// The request names the candidate by id only, and the reply carries errors only.
func CheckHandler(configFile string) http.Handler {
	base := filepath.Join(filepath.Dir(configFile), CheckDir)
	name := filepath.Base(configFile)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/graphjin/config-check" {
			http.NotFound(w, r)
			return
		}
		var req checkRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil || !candidateID.MatchString(req.Candidate) {
			http.Error(w, "candidate must be a candidate id", http.StatusBadRequest)
			return
		}
		dir := filepath.Join(base, req.Candidate)
		file := filepath.Join(dir, name)
		info, err := os.Lstat(dir)
		if err == nil && !info.IsDir() {
			err = errors.New("not a folder")
		}
		if err == nil {
			info, err = os.Lstat(file)
		}
		if err == nil && !info.Mode().IsRegular() {
			err = errors.New("not a regular file")
		}
		if err != nil {
			http.Error(w, "no candidate with that id", http.StatusNotFound)
			return
		}
		res := checkResult{Valid: true, Errors: []string{}}
		if err := CheckConfig(file); err != nil {
			res = checkResult{Valid: false, Errors: []string{err.Error()}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
}
