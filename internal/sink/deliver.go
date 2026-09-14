package sink

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultOrbAPI  = "https://api.dev.orbnet.app"
	defaultNtfy    = "https://ntfy.sh"
	defaultTimeout = 10 * time.Second
)

// view is the template data bag (TurnEvent plus constructor extras).
type view struct {
	SessionID      string
	SessionTitle   string
	Workspace      string
	ModelID        string
	TurnID         string
	Kind           string
	Message        string
	Preview        string
	DeepLink       string
	Timestamp      time.Time
	IdempotencyKey string
	TitlePrefix    string
	Channel        string
	Username       string
	IconEmoji      string
	AvatarURL      string
}

func viewOf(ev TurnEvent, spec SinkSpec) view {
	msg := ev.Message
	if len(msg) > maxBodyBytes {
		msg = truncateRunes(msg, maxBodyBytes/2)
	}
	return view{
		SessionID:      ev.SessionID,
		SessionTitle:   ev.SessionTitle,
		Workspace:      ev.Workspace,
		ModelID:        ev.ModelID,
		TurnID:         ev.TurnID,
		Kind:           ev.Kind,
		Message:        msg,
		Preview:        ev.Preview,
		DeepLink:       ev.DeepLink,
		Timestamp:      ev.Timestamp,
		IdempotencyKey: ev.IdempotencyKey,
		TitlePrefix:    spec.TitlePrefix,
		Channel:        spec.Channel,
		Username:       spec.Username,
		IconEmoji:      spec.IconEmoji,
		AvatarURL:      spec.AvatarURL,
	}
}

func deliverSpec(ctx context.Context, client *http.Client, spec SinkSpec, ev TurnEvent, secret string, stdout io.Writer) (int, error) {
	if spec.Type == "stdout" {
		return 0, deliverStdout(stdout, spec, ev)
	}
	plan, err := spec.plan(secret)
	if err != nil {
		return 0, err
	}
	data := viewOf(ev, spec)
	return deliverHTTP(ctx, client, plan, data, bearerSecret(spec, secret))
}

func bearerSecret(spec SinkSpec, secret string) string {
	switch spec.Type {
	case "slack", "discord":
		return "" // secret is the URL
	default:
		return secret
	}
}

func (s SinkSpec) plan(secret string) (webhookPlan, error) {
	switch s.Type {
	case "orb":
		return s.planOrb()
	case "webhook":
		return s.planWebhook()
	case "slack":
		return s.planSlack(secret)
	case "discord":
		return s.planDiscord(secret)
	case "ntfy":
		return s.planNtfy()
	default:
		return webhookPlan{}, fmt.Errorf("unknown type %q", s.Type)
	}
}

func (s SinkSpec) planOrb() (webhookPlan, error) {
	base := s.APIBase
	if base == "" {
		base = defaultOrbAPI
	}
	u := strings.TrimRight(base, "/") + "/v1/topics/" + url.PathEscape(s.TopicID) + "/messages"
	titleTmpl := "{{.TitlePrefix}}{{if .Preview}}{{.Preview}}{{else}}{{.SessionTitle}}{{end}}"
	headers := map[string]string{
		"Content-Type":     "text/markdown",
		"X-Orb-Title":      titleTmpl,
		"X-Orb-Body-Type":  "markdown",
		"X-Orb-Return-Url": "{{.DeepLink}}",
		"X-Orb-Run-Id":     "{{.SessionID}}",
		"Idempotency-Key":  "{{.IdempotencyKey}}",
	}
	return webhookPlan{
		URL:     u,
		Method:  http.MethodPost,
		Headers: headers,
		// Body always carries the session URL so http:// deep links still work
		// when X-Orb-Return-Url is omitted (Orb requires https://).
		Template: "{{.Message}}\n\n[Open session]({{.DeepLink}})\n",
	}, nil
}

func (s SinkSpec) planWebhook() (webhookPlan, error) {
	headers := map[string]string{}
	for k, v := range s.Headers {
		headers[k] = v
	}
	method := s.Method
	if method == "" {
		method = http.MethodPost
	}
	return webhookPlan{
		URL:      s.URL,
		Method:   method,
		Headers:  headers,
		Template: s.Template,
	}, nil
}

const slackTemplate = `{"text":{{json (printf "%s\n%s" .Preview .DeepLink)}}{{if .Channel}},"channel":{{json .Channel}}{{end}}{{if .Username}},"username":{{json .Username}}{{end}}{{if .IconEmoji}},"icon_emoji":{{json .IconEmoji}}{{end}},"blocks":[{"type":"section","text":{"type":"mrkdwn","text":{{json (truncate .Message 2800)}}}},{"type":"section","text":{"type":"mrkdwn","text":{{json (printf "<%s|Open session>" .DeepLink)}}}}]}`

func (s SinkSpec) planSlack(secret string) (webhookPlan, error) {
	u := strings.TrimSpace(secret)
	if u == "" {
		return webhookPlan{}, fmt.Errorf("slack webhook URL empty (secret_env)")
	}
	headers := map[string]string{"Content-Type": "application/json"}
	return webhookPlan{
		URL:      u,
		Method:   http.MethodPost,
		Headers:  headers,
		Template: slackTemplate,
	}, nil
}

const discordTemplate = `{"username":{{json .Username}},"avatar_url":{{json .AvatarURL}},"embeds":[{"title":{{json .Preview}},"description":{{json (truncate .Message 3900)}},"url":{{json .DeepLink}}}]}`

func (s SinkSpec) planDiscord(secret string) (webhookPlan, error) {
	u := strings.TrimSpace(secret)
	if u == "" {
		return webhookPlan{}, fmt.Errorf("discord webhook URL empty (secret_env)")
	}
	headers := map[string]string{"Content-Type": "application/json"}
	return webhookPlan{
		URL:      u,
		Method:   http.MethodPost,
		Headers:  headers,
		Template: discordTemplate,
	}, nil
}

func (s SinkSpec) planNtfy() (webhookPlan, error) {
	server := s.Server
	if server == "" {
		server = defaultNtfy
	}
	topic := strings.Trim(s.Topic, "/")
	u := strings.TrimRight(server, "/") + "/" + url.PathEscape(topic)
	prio := s.Priority
	if prio == 0 {
		prio = 3
	}
	headers := map[string]string{
		"Title":    "{{if .Preview}}{{.Preview}}{{else}}{{.SessionTitle}}{{end}}",
		"Click":    "{{.DeepLink}}",
		"Priority": fmt.Sprintf("%d", prio),
	}
	return webhookPlan{
		URL:      u,
		Method:   http.MethodPost,
		Headers:  headers,
		Template: "{{.Message}}",
	}, nil
}

func deliverStdout(w io.Writer, spec SinkSpec, ev TurnEvent) error {
	format := spec.Format
	if format == "" {
		format = "plain"
	}
	var line string
	if format == "json" {
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		line = string(b)
	} else {
		line = fmt.Sprintf("sink=%s kind=%s session=%s turn=%s title=%q preview=%q link=%s",
			spec.ID, ev.Kind, ev.SessionID, ev.TurnID, ev.SessionTitle, ev.Preview, ev.DeepLink)
	}
	if w != nil {
		_, err := io.WriteString(w, line+"\n")
		return err
	}
	log.Printf("sink/stdout %s", line)
	return nil
}

// resolveSecret is assigned in manager.go to prefer $MEMORY/env overlay.
var resolveSecret = func(name string) (string, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v != "" {
		return v, true
	}
	return "", false
}
