// Package embedding is the one gateway to an OpenAI-compatible embedding
// service at a URL.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/open-neko/openneko/apps/ax-harness/inference"
)

const (
	MaxInputs     = 64
	MaxInputChars = 32_000
	MaxDimensions = 16_000
	maxReplyBytes = 64 << 20
)

type Request struct {
	URL        string   `json:"url"`
	Model      string   `json:"model"`
	Dimensions int      `json:"dimensions,omitempty"`
	Input      []string `json:"input"`
}

type Result struct {
	Vectors     [][]float64 `json:"vectors"`
	Model       string      `json:"model"`
	InputTokens int         `json:"input_tokens,omitempty"`
}

func (r Request) Validate() error {
	if r.Model == "" || len(r.Model) > 128 {
		return errors.New("invalid embedding model")
	}
	if r.Dimensions < 0 || r.Dimensions > MaxDimensions {
		return errors.New("invalid embedding dimensions")
	}
	if inference.ValidModelURL(r.URL) != nil {
		return errors.New("invalid embedding URL")
	}
	if len(r.Input) == 0 || len(r.Input) > MaxInputs {
		return fmt.Errorf("embedding takes 1 to %d texts", MaxInputs)
	}
	for _, text := range r.Input {
		if strings.TrimSpace(text) == "" {
			return errors.New("embedding text is empty")
		}
		if utf8.RuneCountInString(text) > MaxInputChars {
			return fmt.Errorf("embedding text exceeds %d characters", MaxInputChars)
		}
	}
	return nil
}

var client = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("embedding service redirected") },
	Timeout:       10 * time.Minute,
}

// Embed returns one vector per input, in input order.
func Embed(ctx context.Context, request Request, key string) (Result, error) {
	result, err := embed(ctx, request, key)
	if err != nil && key != "" && strings.Contains(err.Error(), key) {
		err = errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
	}
	return result, err
}

func embed(ctx context.Context, request Request, key string) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	body := map[string]any{"model": request.Model, "input": request.Input, "encoding_format": "float"}
	if request.Dimensions > 0 {
		body["dimensions"] = request.Dimensions
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(request.URL, "/")+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return Result{}, errors.New("invalid embedding URL")
	}
	httpRequest.Header.Set("content-type", "application/json")
	if key != "" {
		httpRequest.Header.Set("authorization", "Bearer "+key)
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("embedding service: %w", ctx.Err())
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return Result{}, fmt.Errorf("embedding service unavailable: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return Result{}, fmt.Errorf("embedding service returned HTTP %d", response.StatusCode)
	}
	var reply struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxReplyBytes)).Decode(&reply); err != nil {
		return Result{}, errors.New("embedding service returned a reply that is not JSON")
	}
	if len(reply.Data) != len(request.Input) {
		return Result{}, errors.New("embedding service returned an incompatible vector")
	}
	vectors := make([][]float64, len(request.Input))
	width := 0
	for position, item := range reply.Data {
		index := position
		if item.Index != nil {
			index = *item.Index
		}
		if index < 0 || index >= len(vectors) || vectors[index] != nil || !validVector(item.Embedding, request.Dimensions) ||
			(width != 0 && len(item.Embedding) != width) {
			return Result{}, errors.New("embedding service returned an incompatible vector")
		}
		width = len(item.Embedding)
		vectors[index] = item.Embedding
	}
	return Result{Vectors: vectors, Model: request.Model, InputTokens: reply.Usage.PromptTokens}, nil
}

func validVector(vector []float64, dimensions int) bool {
	if len(vector) == 0 || len(vector) > MaxDimensions || (dimensions > 0 && len(vector) != dimensions) {
		return false
	}
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}
