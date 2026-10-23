package session

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/rendicott/marble/internal/model"
)

const (
	// clampFloor is the smallest completion we will still send.
	clampFloor = 256
	// retryMinOut is the completion the retry must still be able to reserve.
	retryMinOut = 1024
	// skewSafety inflates the observed estimator gap so one sample does not sit on the window.
	skewSafety = 1.25
	// defaultObsRatio is provider_prompt_tokens / estimated_prompt_tokens with no samples.
	// The incident undercount was ~8.7%; 1.10 leaves a little room before a 400.
	defaultObsRatio = 1.10
	marginAbsFloor  = 4096
)

// ContextLimitObservation is the last parsed context-length 400 for a session.
// Est is the harness estimate of the prompt that produced Messages.
type ContextLimitObservation struct {
	Limit      int
	Messages   int
	Completion int
	Est        int
}

// promptSkewAccum sums provider and harness prompt-token counts for one model.
type promptSkewAccum struct {
	provider  int64
	estimated int64
}

func skewKey(baseURL, modelName string) string {
	return baseURL + "\x00" + modelName
}

// obsRatio is the recorded provider/estimate ratio for em, or defaultObsRatio.
func (r *Runner) obsRatio(em EffectiveModel) float64 {
	if r == nil {
		return defaultObsRatio
	}
	r.skewMu.Lock()
	defer r.skewMu.Unlock()
	if r.promptSkew == nil {
		return defaultObsRatio
	}
	s, ok := r.promptSkew[skewKey(em.BaseURL, em.Model)]
	if !ok || s.estimated <= 0 || s.provider <= 0 {
		return defaultObsRatio
	}
	return float64(s.provider) / float64(s.estimated)
}

// observePromptSkew records one provider_prompt_tokens / estimated_prompt_tokens sample.
func (r *Runner) observePromptSkew(em EffectiveModel, estimated, provider int) {
	if r == nil || estimated <= 0 || provider <= 0 {
		return
	}
	r.skewMu.Lock()
	defer r.skewMu.Unlock()
	if r.promptSkew == nil {
		r.promptSkew = map[string]promptSkewAccum{}
	}
	key := skewKey(em.BaseURL, em.Model)
	s := r.promptSkew[key]
	s.estimated += int64(estimated)
	s.provider += int64(provider)
	r.promptSkew[key] = s
}

// AdaptiveMargin is max(4096, limit/64, ceil(estimate*(obsRatio-1)*safety)),
// then clamped to at most ~10% of limit. obsRatio <= 0 uses defaultObsRatio.
func AdaptiveMargin(limit, estimate int, obsRatio float64) int {
	if obsRatio <= 0 || math.IsNaN(obsRatio) || math.IsInf(obsRatio, 0) {
		obsRatio = defaultObsRatio
	}
	m := marginAbsFloor
	if limit > 0 {
		if v := limit / 64; v > m {
			m = v
		}
	}
	if estimate > 0 && obsRatio > 1 {
		skew := int(math.Ceil(float64(estimate)*(obsRatio-1)*skewSafety - 1e-9))
		if skew > m {
			m = skew
		}
	}
	if limit > 0 {
		cap := (limit + 9) / 10 // ceil(10% of limit)
		if cap < 1 {
			cap = 1
		}
		if m > cap {
			m = cap
		}
	}
	if m < 1 {
		m = 1
	}
	return m
}

// ClampResult is the preflight max_tokens decision.
type ClampResult struct {
	MaxTokens int
	Margin    int
	Est       int
	Room      int
	OK        bool
}

// ClampMaxTokens sets max_tokens to what still fits after est and the adaptive margin,
// capped by maxOutput. OK is false when the remaining completion is below clampFloor:
// the caller must not POST.
func ClampMaxTokens(limit, maxOutput, est int, obsRatio float64) ClampResult {
	margin := AdaptiveMargin(limit, est, obsRatio)
	room := limit - est - margin
	res := ClampResult{Margin: margin, Est: est, Room: room}
	if room < clampFloor {
		return res
	}
	maxTokens := room
	if maxOutput > 0 && maxTokens > maxOutput {
		maxTokens = maxOutput
	}
	res.MaxTokens = maxTokens
	res.OK = true
	return res
}

// RetryMaxTokens is the one structured retry: size the completion from the provider's
// own message count. ok is false when that completion cannot fit, is below clampFloor,
// or would not be smaller than the max_tokens already sent.
func RetryMaxTokens(providerLimit, messages, maxOutput, margin, sent int) (int, bool) {
	if providerLimit <= 0 || messages <= 0 || margin < 0 {
		return 0, false
	}
	if messages+retryMinOut+margin > providerLimit {
		return 0, false
	}
	retry := providerLimit - messages - margin
	if maxOutput > 0 && retry > maxOutput {
		retry = maxOutput
	}
	if retry < clampFloor || retry >= sent {
		return retry, false
	}
	return retry, true
}

func (s *Session) setLastContextLimit(o ContextLimitObservation) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastContextLimit = &o
	s.mu.Unlock()
}

// LastContextLimit returns the last parsed context-length 400 seen by this process.
func (s *Session) LastContextLimit() (ContextLimitObservation, bool) {
	if s == nil {
		return ContextLimitObservation{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastContextLimit == nil {
		return ContextLimitObservation{}, false
	}
	return *s.lastContextLimit, true
}

// contextRetry decides the single overage retry. A typed error is recorded even when
// the retry is declined. The provider message count replaces the harness estimate;
// the margin is never loosened below the one just sent.
func (r *Runner) contextRetry(s *Session, em EffectiveModel, est int, cl ClampResult, err error) (int, bool) {
	if r == nil || err == nil {
		return 0, false
	}
	var cle *model.ContextLimitError
	if !errors.As(err, &cle) || cle == nil {
		return 0, false
	}
	r.noteProviderLimit(s, em, cle)
	if s != nil {
		s.setLastContextLimit(ContextLimitObservation{
			Limit:      cle.Limit,
			Messages:   cle.Messages,
			Completion: cle.Completion,
			Est:        est,
		})
	}
	limit := cle.Limit
	if limit <= 0 {
		limit = em.ContextLimit
	}
	margin := cl.Margin
	if cle.Limit > 0 {
		if m := AdaptiveMargin(cle.Limit, est, r.obsRatio(em)); m > margin {
			margin = m
		}
	}
	retry, ok := RetryMaxTokens(limit, cle.Messages, em.MaxOutput, margin, cl.MaxTokens)
	if cle.Messages > 0 && est > 0 {
		r.observePromptSkew(em, est, cle.Messages)
	}
	return retry, ok
}

// noteProviderLimit records a provider window that is larger than the catalog or
// process limit. The catalog row is not rewritten.
func (r *Runner) noteProviderLimit(s *Session, em EffectiveModel, cle *model.ContextLimitError) {
	if r == nil || cle == nil || cle.Limit <= 0 || cle.Limit <= em.ContextLimit {
		return
	}
	note := fmt.Sprintf("[harness] provider context limit %d is larger than catalog/process limit %d for %s; not rewriting the catalog", cle.Limit, em.ContextLimit, em.Model)
	if s != nil {
		r.advisory(s, note)
	}
	if r.Reg != nil && s != nil {
		r.Reg.logEvent(s, "context_limit_observed", "system", note, "", "", "", nil, nil, nil, nil, nil, "", "")
	}
}

func contextOverageText(err error) bool {
	if err == nil {
		return false
	}
	var cle *model.ContextLimitError
	if errors.As(err, &cle) {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "context length") ||
		strings.Contains(low, "maximum context") ||
		strings.Contains(low, "too many tokens")
}

func tokenHTTP400(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "model http 400") && strings.Contains(low, "token")
}
