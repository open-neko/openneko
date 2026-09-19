package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Fixture state is synthetic only. Control is exercised inside the fixture container.
var credentialState = struct {
	sync.RWMutex
	key string
}{key: "synthetic-M2-credential"}

func configureKey(key string) error {
	body, _ := json.Marshal(map[string]string{"key": key})
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Post("http://127.0.0.1:8080/control", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		return fmt.Errorf("fixture configuration failed")
	}
	return nil
}
func control(w http.ResponseWriter, r *http.Request) {
	var config struct {
		Key string `json:"key"`
	}
	if r.Method != "POST" || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&config) != nil || config.Key == "" {
		http.Error(w, "invalid fixture configuration", 400)
		return
	}
	credentialState.Lock()
	credentialState.key = config.Key
	credentialState.Unlock()
	w.WriteHeader(204)
}
func queryProbe(origin, key string) error {
	if !strings.HasPrefix(key, "openshell:resolve:") {
		return fmt.Errorf("query workload did not receive a placeholder")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Get(origin + "/v1/query?key=" + url.QueryEscape(key))
	if err != nil {
		return fmt.Errorf("query transport failed")
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode != 200 || string(body) != "verified" {
		return fmt.Errorf("query credential rejected: HTTP %d", res.StatusCode)
	}
	fmt.Println(`{"check":"query_credential","ok":true}`)
	return nil
}

func oauthFixture(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/token" || r.ParseForm() != nil ||
		r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "fixture" || r.Form.Get("client_secret") != "synthetic-refresh-secret" {
		http.Error(w, `{"error":"invalid_client"}`, 401)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"access_token":"synthetic-M2-refreshed","token_type":"Bearer","expires_in":3600}`)
	fmt.Println(`{"check":"oauth_token_minted","ok":true}`)
}
