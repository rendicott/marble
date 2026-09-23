package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ImageRequest is a POST /images/generations call (OpenAI gpt-image-*).
type ImageRequest struct {
	Model        string
	Prompt       string
	Size         string // "" = provider default
	Quality      string
	Background   string
	OutputFormat string
	N            int
}

// ImageUsage is token accounting from the Images API.
type ImageUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
	InputTextTokens   int `json:"-"`
	InputImageTokens  int `json:"-"`
	OutputImageTokens int `json:"-"`
}

// ImageResult is decoded image bytes plus usage.
type ImageResult struct {
	Images         [][]byte
	B64            []string
	RevisedPrompts []string
	Usage          *ImageUsage
	LatencyMs      int
	Model          string
}

type imageGenRequest struct {
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	Size         string `json:"size,omitempty"`
	Quality      string `json:"quality,omitempty"`
	Background   string `json:"background,omitempty"`
	OutputFormat string `json:"output_format,omitempty"`
	N            int    `json:"n,omitempty"`
}

type imageGenResponse struct {
	Data []struct {
		B64JSON       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt"`
	} `json:"data"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		TotalTokens        int `json:"total_tokens"`
		InputTokensDetails struct {
			ImageTokens int `json:"image_tokens"`
			TextTokens  int `json:"text_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ImageTokens int `json:"image_tokens"`
			TextTokens  int `json:"text_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

// GenerateImage calls {base}/images/generations and decodes b64_json payloads.
func (c *Client) GenerateImage(ctx context.Context, req ImageRequest) (ImageResult, error) {
	if c == nil {
		return ImageResult{}, fmt.Errorf("nil client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = c.Model
	}
	body := imageGenRequest{
		Model:        modelName,
		Prompt:       req.Prompt,
		Size:         strings.TrimSpace(req.Size),
		Quality:      strings.TrimSpace(req.Quality),
		Background:   strings.TrimSpace(req.Background),
		OutputFormat: strings.TrimSpace(req.OutputFormat),
		N:            n,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ImageResult{}, err
	}
	url := c.BaseURL + "/images/generations"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(raw)))
	if err != nil {
		return ImageResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuth(httpReq)

	start := time.Now()
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return ImageResult{}, fmt.Errorf("model request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ImageResult{}, err
	}
	latency := int(time.Since(start).Milliseconds())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ImageResult{LatencyMs: latency}, fmt.Errorf("model HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 800))
	}

	var cr imageGenResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return ImageResult{LatencyMs: latency}, fmt.Errorf("decode response: %w", err)
	}
	out := ImageResult{LatencyMs: latency, Model: modelName}
	for _, d := range cr.Data {
		b64 := strings.TrimSpace(d.B64JSON)
		if b64 == "" {
			continue
		}
		img, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			img, err = base64.RawStdEncoding.DecodeString(b64)
		}
		if err != nil {
			return ImageResult{LatencyMs: latency, Model: modelName}, fmt.Errorf("decode b64_json: %w", err)
		}
		out.B64 = append(out.B64, b64)
		out.Images = append(out.Images, img)
		if strings.TrimSpace(d.RevisedPrompt) != "" {
			out.RevisedPrompts = append(out.RevisedPrompts, strings.TrimSpace(d.RevisedPrompt))
		}
	}
	if cr.Usage != nil {
		out.Usage = &ImageUsage{
			InputTokens:       cr.Usage.InputTokens,
			OutputTokens:      cr.Usage.OutputTokens,
			TotalTokens:       cr.Usage.TotalTokens,
			InputTextTokens:   cr.Usage.InputTokensDetails.TextTokens,
			InputImageTokens:  cr.Usage.InputTokensDetails.ImageTokens,
			OutputImageTokens: cr.Usage.OutputTokensDetails.ImageTokens,
		}
	}
	if len(out.Images) == 0 {
		return ImageResult{LatencyMs: latency, Model: modelName}, fmt.Errorf("image model returned no image")
	}
	return out, nil
}
