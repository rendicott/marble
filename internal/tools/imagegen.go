package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rendicott/marble/internal/db"
	"github.com/rendicott/marble/internal/model"
)

const (
	imageGenTurnCallMax = 6
	imageGenMaxBytes    = 25 << 20 // 25 MiB per image
	imageGenDefaultTO   = 300 * time.Second

	// OpenAI gpt-image token rates (USD per token).
	imageUSDTextIn   = 5.0 / 1e6
	imageUSDImageIn  = 8.0 / 1e6
	imageUSDImageOut = 30.0 / 1e6

	noImageModelMsg = "no image model configured — add one with model_add kind=image (e.g. gpt-image-2.5-sunburst, base_url https://api.openai.com/v1, api_key_env OPENAI_API_KEY)"
	vtracerPy       = `import sys, vtracer
vtracer.convert_image_to_svg_py(sys.argv[1], sys.argv[2], colormode="color", mode="spline", filter_speckle=4)
`
	vtracerPipHint = "vectorize skipped: python3/vtracer unavailable — pip install vtracer"
)

type generateImageArgs struct {
	Prompt       string `json:"prompt"`
	Path         string `json:"path"`
	ModelID      string `json:"model_id"`
	Size         string `json:"size"`
	Quality      string `json:"quality"`
	Background   string `json:"background"`
	OutputFormat string `json:"output_format"`
	N            int    `json:"n"`
	Vectorize    bool   `json:"vectorize"`
	Attach       *bool  `json:"attach"`
}

type imageGenFile struct {
	Path           string `json:"path"`
	Bytes          int    `json:"bytes"`
	MIME           string `json:"mime"`
	VectorizedPath string `json:"vectorized_path,omitempty"`
}

type imageUsageJSON struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
	InputTextTokens   int `json:"input_text_tokens"`
	InputImageTokens  int `json:"input_image_tokens"`
	OutputImageTokens int `json:"output_image_tokens"`
}

func (r *Registry) generateImage(argsJSON string, tc *TurnContext) (string, error) {
	if r == nil || r.ImageClientFor == nil {
		return "", fmt.Errorf("image models not configured")
	}
	if tc == nil {
		tc = &TurnContext{}
	}
	var a generateImageArgs
	if err := parseArgs(argsJSON, &a); err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if tc.ImageGenCalls >= imageGenTurnCallMax {
		return "", fmt.Errorf("too many generate_image calls this turn (max %d)", imageGenTurnCallMax)
	}
	tc.ImageGenCalls++

	n := a.N
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		return "", fmt.Errorf("n must be 1..4")
	}
	size := strings.TrimSpace(a.Size)
	if size == "" {
		size = "1024x1024"
	}
	quality := strings.TrimSpace(a.Quality)
	if quality == "" {
		quality = "medium"
	}
	background := strings.TrimSpace(a.Background)
	if background == "" {
		background = "transparent"
	}
	format := strings.TrimSpace(a.OutputFormat)
	if format == "" {
		format = "png"
	}
	attach := true
	if a.Attach != nil {
		attach = *a.Attach
	}
	rel := strings.TrimSpace(a.Path)
	if rel == "" {
		rel = fmt.Sprintf("generated/image-%d%s", time.Now().Unix(), extForImageFormat(format))
	}

	catalogID, err := r.resolveImageCatalogID(strings.TrimSpace(a.ModelID))
	if err != nil {
		return "", err
	}
	client, providerModel, err := r.ImageClientFor(catalogID)
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", fmt.Errorf("image models not configured")
	}
	if strings.TrimSpace(providerModel) == "" {
		providerModel = client.Model
	}

	timeout := r.ImageTimeout
	if timeout <= 0 {
		timeout = imageGenDefaultTO
	}
	parent := context.Background()
	if tc.Ctx != nil {
		parent = tc.Ctx
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	res, err := client.GenerateImage(ctx, model.ImageRequest{
		Model:        providerModel,
		Prompt:       prompt,
		Size:         size,
		Quality:      quality,
		Background:   background,
		OutputFormat: format,
		N:            n,
	})
	if err != nil {
		return "", err
	}
	for _, img := range res.Images {
		if len(img) > imageGenMaxBytes {
			return "", fmt.Errorf("image exceeds 25 MiB")
		}
	}

	notes := []string{}
	files := make([]imageGenFile, 0, len(res.Images))
	ids := []string{}
	anySVG := false
	for i, img := range res.Images {
		outRel := imageIndexedPath(rel, i)
		abs, err := r.resolve(outRel)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, img, 0o644); err != nil {
			return "", err
		}
		name := filepath.Base(outRel)
		mime := mimeForImageFormat(format)
		if m, k, sErr := db.SniffAttachment(name, img); sErr == nil && k == "image" && m != "" {
			mime = m
		}
		f := imageGenFile{Path: outRel, Bytes: len(img), MIME: mime}
		if a.Vectorize {
			svgRel := swapExt(outRel, ".svg")
			svgAbs, rErr := r.resolve(svgRel)
			if rErr != nil {
				notes = append(notes, "vectorize skipped: "+rErr.Error())
			} else if vErr := vectorizeWithVtracer(ctx, abs, svgAbs); vErr != nil {
				notes = append(notes, vectorizeNote(vErr))
			} else {
				f.VectorizedPath = svgRel
				anySVG = true
			}
		}
		if attach && strings.TrimSpace(tc.SessionID) != "" {
			if r.StageChatAttachment == nil {
				notes = append(notes, "attachment store not configured")
			} else {
				meta := imageAttachMeta(prompt, providerModel, catalogID, outRel)
				id, gotMIME, gotKind, sErr := r.StageChatAttachment(tc.SessionID, name, img, meta)
				if sErr != nil {
					notes = append(notes, "attach skipped: "+sErr.Error())
				} else if id != "" {
					ids = append(ids, id)
					if gotMIME != "" {
						f.MIME = gotMIME
					}
					if tc.OnChatAttachment != nil {
						tc.OnChatAttachment(Attachment{
							Path:   id,
							Name:   name,
							Inline: gotKind == "image" || gotKind == "",
							Mime:   f.MIME,
							Size:   int64(len(img)),
						})
					}
				}
			}
		}
		files = append(files, f)
	}

	usage := imageUsageJSON{}
	if res.Usage != nil {
		usage = imageUsageJSON{
			InputTokens:       res.Usage.InputTokens,
			OutputTokens:      res.Usage.OutputTokens,
			TotalTokens:       res.Usage.TotalTokens,
			InputTextTokens:   res.Usage.InputTextTokens,
			InputImageTokens:  res.Usage.InputImageTokens,
			OutputImageTokens: res.Usage.OutputImageTokens,
		}
	}
	return mustJSON(map[string]interface{}{
		"ok":             true,
		"files":          files,
		"attachment_ids": ids,
		"model":          providerModel,
		"model_id":       catalogID,
		"size":           size,
		"quality":        quality,
		"background":     background,
		"vectorized":     anySVG,
		"usage":          usage,
		"est_cost_usd":   estImageCostUSD(res.Usage),
		"latency_ms":     res.LatencyMs,
		"notes":          notes,
	}), nil
}

func (r *Registry) resolveImageCatalogID(modelID string) (string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		if r.ImageModelDefault == nil {
			return "", fmt.Errorf("image models not configured")
		}
		row, err := r.ImageModelDefault()
		if err != nil {
			return "", err
		}
		if row == nil {
			return "", fmt.Errorf("%s", noImageModelMsg)
		}
		if err := imageRowKindOK(row, ""); err != nil {
			return "", err
		}
		id, _ := row["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return "", fmt.Errorf("%s", noImageModelMsg)
		}
		return id, nil
	}
	if r.ImageModelGet == nil {
		return "", fmt.Errorf("image models not configured")
	}
	row, err := r.ImageModelGet(modelID)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", fmt.Errorf("model_id %q not found", modelID)
	}
	if err := imageRowKindOK(row, modelID); err != nil {
		return "", err
	}
	id, _ := row["id"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		id = modelID
	}
	return id, nil
}

func imageRowKindOK(row map[string]interface{}, requestedID string) error {
	if row == nil {
		return fmt.Errorf("model_id %q not found", requestedID)
	}
	if en, ok := row["enabled"].(bool); ok && !en {
		id := requestedID
		if id == "" {
			id, _ = row["id"].(string)
		}
		return fmt.Errorf("model_id %q is disabled", id)
	}
	kind, _ := row["kind"].(string)
	if db.NormalizeModelKind(kind) != "image" {
		id := requestedID
		if id == "" {
			id, _ = row["id"].(string)
		}
		shown := kind
		if strings.TrimSpace(shown) == "" {
			shown = "chat"
		}
		return fmt.Errorf("model_id %q is kind %q; kind must be image", id, shown)
	}
	return nil
}

func imageIndexedPath(base string, i int) string {
	if i <= 0 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s-%d%s", stem, i+1, ext)
}

func swapExt(path, ext string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

func extForImageFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return ".jpg"
	case "webp":
		return ".webp"
	case "gif":
		return ".gif"
	default:
		return ".png"
	}
}

func mimeForImageFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

func estImageCostUSD(u *model.ImageUsage) float64 {
	if u == nil {
		return 0
	}
	v := float64(u.InputTextTokens)*imageUSDTextIn +
		float64(u.InputImageTokens)*imageUSDImageIn +
		float64(u.OutputImageTokens)*imageUSDImageOut
	return math.Round(v*10000) / 10000
}

func imageAttachMeta(prompt, providerModel, catalogID, path string) string {
	b, err := json.Marshal(map[string]string{
		"prompt":     prompt,
		"model":      providerModel,
		"model_id":   catalogID,
		"path":       path,
		"provenance": "generate_image",
	})
	if err != nil {
		return ""
	}
	return string(b)
}

func vectorizeWithVtracer(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "python3", "-c", vtracerPy, src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("%s", msg)
	}
	if _, err := os.Stat(dst); err != nil {
		return fmt.Errorf("vtracer produced no svg")
	}
	return nil
}

func vectorizeNote(err error) string {
	if err == nil {
		return vtracerPipHint
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "no module named") ||
		strings.Contains(low, "modulenotfounderror") ||
		strings.Contains(low, "executable file not found") ||
		strings.Contains(low, "not found") {
		return vtracerPipHint
	}
	return vtracerPipHint + " (" + err.Error() + ")"
}
