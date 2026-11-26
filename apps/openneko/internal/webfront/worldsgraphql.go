package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
)

// worldsGraphQLLimit is the largest read request that the world builder sends.
const worldsGraphQLLimit = 1 << 20

// worldsTokenTTL is how long a GraphJin token of a world read lives. A
// subscription shows it once, when its socket opens.
const worldsTokenTTL = 10 * time.Minute

// worldsTarget is the GraphJin of the organization's data, and the token that
// makes a read as the person who asks.
type worldsTarget struct {
	endpoint string
	token    string
}

// worldsGraphQL passes a read of the world builder to the organization's
// GraphJin, as the signed-in person, so the read follows that person's grants
// and GraphJin stays inside the stack. A POST is one query. A WebSocket on the
// same path is a graphql-transport-ws socket for subscriptions. Mutations are
// refused on both.
func (a *API) worldsGraphQL(w http.ResponseWriter, r *http.Request) {
	act, err := a.currentActor(r.Context(), r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	target, err := a.worldsGraphjinTarget(r.Context(), act)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if target.endpoint == "" {
		sendError(w, http.StatusNotFound, "No GraphJin data source is enabled.")
		return
	}
	if r.Method == http.MethodGet {
		a.worldsGraphQLSocket(w, r, target)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, worldsGraphQLLimit+1))
	if err != nil || len(body) > worldsGraphQLLimit {
		sendError(w, http.StatusRequestEntityTooLarge, "The read is too large.")
		return
	}
	var request struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(body, &request) != nil || strings.TrimSpace(request.Query) == "" {
		sendError(w, http.StatusBadRequest, "Send a query.")
		return
	}
	if err := worldsReadOnly(request.Query, false); err != nil {
		sendError(w, http.StatusForbidden, err.Error())
		return
	}
	status, reply, err := forwardGraphQL(r.Context(), target, body)
	if err != nil {
		sendError(w, http.StatusBadGateway, "GraphJin did not answer: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(reply)
}

// worldsGraphjinTarget finds the organization's GraphJin and mints a token for
// the person: an admin reads as admin, and a member reads with the grants of
// the member's groups.
func (a *API) worldsGraphjinTarget(ctx context.Context, act actor) (worldsTarget, error) {
	var graphqlURL, kind, authMode string
	err := a.DB.QueryRow(ctx, `select coalesce(graphql_url, ''), kind, coalesce(auth_mode, '') from data_source
		where org_id = $1 and enabled = true order by is_default desc, created_at limit 1`, act.OrgID).
		Scan(&graphqlURL, &kind, &authMode)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && strings.TrimSpace(graphqlURL) == "") {
		return worldsTarget{}, nil
	}
	if err != nil {
		return worldsTarget{}, err
	}
	if kind != "graphjin" {
		return worldsTarget{}, errors.New("the default data source is not a GraphJin server")
	}
	// The internal GraphJin holds OpenNeko's own tables. A world never reads it.
	if isInternalGraphjinURL(graphqlURL) {
		return worldsTarget{}, errors.New("refusing to expose the OpenNeko internal GraphJin")
	}
	target := worldsTarget{endpoint: graphqlEndpoint(graphqlURL)}
	if authMode == "jwt" {
		role := "member"
		if act.Admin {
			role = "admin"
		}
		token, err := a.graphjinPrincipalToken(ctx, act.OrgID, graphjinPrincipal{userID: act.UserID, role: role}, worldsTokenTTL)
		if err != nil {
			return worldsTarget{}, err
		}
		target.token = token
	}
	return target, nil
}

// forwardGraphQL posts one read to GraphJin and gives back its status and body.
func forwardGraphQL(ctx context.Context, target worldsTarget, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if target.token != "" {
		req.Header.Set("Authorization", "Bearer "+target.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	return res.StatusCode, reply, err
}

// worldsGraphQLSocket joins the builder's socket to a socket on GraphJin. The
// token goes in the first message to GraphJin, and the builder's own first
// message is replaced, so the builder cannot read as anyone else. Each
// subscribe message is checked: only a query or a subscription passes.
func (a *API) worldsGraphQLSocket(w http.ResponseWriter, r *http.Request, target worldsTarget) {
	client, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
	if err != nil {
		return
	}
	defer client.CloseNow() //nolint:errcheck
	client.SetReadLimit(worldsGraphQLLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstreamURL := target.endpoint
	if strings.HasPrefix(upstreamURL, "http") {
		upstreamURL = "ws" + strings.TrimPrefix(upstreamURL, "http")
	}
	upstream, _, err := websocket.Dial(ctx, upstreamURL, &websocket.DialOptions{Subprotocols: []string{"graphql-transport-ws"}})
	if err != nil {
		client.Close(websocket.StatusTryAgainLater, "GraphJin did not answer") //nolint:errcheck
		return
	}
	defer upstream.CloseNow() //nolint:errcheck
	upstream.SetReadLimit(32 << 20)

	go func() {
		defer cancel()
		for {
			kind, data, err := upstream.Read(ctx)
			if err != nil {
				return
			}
			if client.Write(ctx, kind, data) != nil {
				return
			}
		}
	}()
	for {
		_, data, err := client.Read(ctx)
		if err != nil {
			return
		}
		out, refusal := worldsSocketMessage(data, target.token)
		if refusal != nil {
			if client.Write(ctx, websocket.MessageText, refusal) != nil {
				return
			}
			continue
		}
		if upstream.Write(ctx, websocket.MessageText, out) != nil {
			return
		}
	}
}

// worldsSocketMessage checks one message of the builder on its way to
// GraphJin. It gives the message to send on, or a refusal to send back.
func worldsSocketMessage(data []byte, token string) (out, refusal []byte) {
	var msg struct {
		Type    string          `json:"type"`
		ID      string          `json:"id,omitempty"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	if json.Unmarshal(data, &msg) != nil {
		reply, _ := json.Marshal(map[string]any{"type": "error", "payload": []map[string]string{{"message": "The message is not JSON."}}})
		return nil, reply
	}
	switch msg.Type {
	case "connection_init":
		payload := map[string]any{}
		if token != "" {
			payload["Authorization"] = "Bearer " + token
		}
		out, _ = json.Marshal(map[string]any{"type": "connection_init", "payload": payload})
		return out, nil
	case "subscribe":
		var p struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(msg.Payload, &p)
		if err := worldsReadOnly(p.Query, true); err != nil {
			reply, _ := json.Marshal(map[string]any{"type": "error", "id": msg.ID, "payload": []map[string]string{{"message": err.Error()}}})
			return nil, reply
		}
	}
	return data, nil
}

// worldsReadOnly refuses a GraphQL document that changes data. It allows
// queries, and subscriptions when the read comes on a socket.
func worldsReadOnly(document string, socket bool) error {
	kinds, err := graphqlOperationKinds(document)
	if err != nil {
		return err
	}
	if len(kinds) == 0 {
		return errors.New("Send a query.")
	}
	for _, kind := range kinds {
		switch {
		case kind == "query":
		case kind == "subscription" && socket:
		case kind == "subscription":
			return errors.New("A subscription goes on the WebSocket of this path.")
		default:
			return fmt.Errorf("A world reads only. The %s is refused.", kind)
		}
	}
	return nil
}

// graphqlOperationKinds gives the kind of each operation of a GraphQL
// document: query, mutation or subscription. A selection set at the top is a
// query. Strings, block strings and comments are skipped, so a keyword in a
// value does not count.
func graphqlOperationKinds(doc string) ([]string, error) {
	var kinds []string
	depth := 0
	expectOperation := true
	for i := 0; i < len(doc); {
		c := doc[i]
		switch {
		case c == '#':
			for i < len(doc) && doc[i] != '\n' {
				i++
			}
		case strings.HasPrefix(doc[i:], `"""`):
			end := strings.Index(doc[i+3:], `"""`)
			if end < 0 {
				return nil, errors.New("The query has an open string.")
			}
			i += end + 6
		case c == '"':
			i++
			for i < len(doc) && doc[i] != '"' {
				if doc[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(doc) {
				return nil, errors.New("The query has an open string.")
			}
			i++
		case c == '{':
			if depth == 0 && expectOperation {
				kinds = append(kinds, "query")
			}
			depth++
			i++
		case c == '}':
			depth--
			if depth < 0 {
				return nil, errors.New("The query has a closing brace with no opening brace.")
			}
			if depth == 0 {
				expectOperation = true
			}
			i++
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			start := i
			for i < len(doc) && (doc[i] == '_' || (doc[i] >= 'a' && doc[i] <= 'z') || (doc[i] >= 'A' && doc[i] <= 'Z') || (doc[i] >= '0' && doc[i] <= '9')) {
				i++
			}
			if depth == 0 && expectOperation {
				switch word := doc[start:i]; word {
				case "query", "mutation", "subscription":
					kinds = append(kinds, word)
					expectOperation = false
				case "fragment":
					expectOperation = false
				default:
					return nil, fmt.Errorf("The query starts with %q, which is not an operation.", word)
				}
			}
		default:
			i++
		}
	}
	if depth != 0 {
		return nil, errors.New("The query has an open brace.")
	}
	return kinds, nil
}
