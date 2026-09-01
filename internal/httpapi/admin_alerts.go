package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chnzzh/hostpin/internal/model"
	"github.com/chnzzh/hostpin/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (a *API) handleAdminAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := a.store.ListAlertRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list alert rules")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.AlertRule]{Data: rules})
}

func (a *API) handleAdminSaveAlertRule(w http.ResponseWriter, r *http.Request) {
	var rule model.AlertRule
	if !decodeJSON(w, r, &rule, 64<<10) {
		return
	}
	if rawID := chi.URLParam(r, "id"); rawID != "" {
		id, err := parsePositiveID(rawID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_id", "invalid alert rule id")
			return
		}
		rule.ID = id
	}
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Scope.Groups = normalizeAgentList(rule.Scope.Groups)
	rule.Scope.NodeIDs = normalizeAgentList(rule.Scope.NodeIDs)
	rule.Scope.Excluded = normalizeAgentList(rule.Scope.Excluded)
	if err := validateAlertRule(rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_alert_rule", err.Error())
		return
	}
	for _, nodeID := range append(append([]string{}, rule.Scope.NodeIDs...), rule.Scope.Excluded...) {
		if _, err := a.store.GetNode(r.Context(), nodeID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_alert_node", "alert scope contains an unknown node")
			return
		}
	}
	saved, err := a.store.SaveAlertRule(r.Context(), rule)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "alert_rule_not_found", "alert rule was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not save alert rule")
		return
	}
	a.alerts.Reload()
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "alert_rule.save", strconv.FormatInt(saved.ID, 10), saved.Name, time.Now().UTC())
	status := http.StatusOK
	if rule.ID == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, model.Envelope[model.AlertRule]{Data: saved})
}

func validateAlertRule(rule model.AlertRule) error {
	if strings.TrimSpace(rule.Name) == "" || len(rule.Name) > 128 {
		return errors.New("rule name is required")
	}
	metrics := map[string]bool{
		"online": true, "cpu": true, "memory": true, "swap": true,
		"load1": true, "load5": true, "load15": true, "disk": true,
		"temperature": true, "gpu": true, "traffic_sum": true,
		"traffic_up": true, "traffic_down": true, "probe_success": true,
		"probe_latency": true, "probe_loss": true,
	}
	if !metrics[rule.Metric] {
		return errors.New("unsupported alert metric")
	}
	if !map[string]bool{">": true, ">=": true, "<": true, "<=": true, "==": true}[rule.Operator] {
		return errors.New("operator must be >, >=, <, <=, or ==")
	}
	if rule.DurationSeconds < 0 || rule.DurationSeconds > 30*24*3600 || rule.CooldownSeconds < 0 || rule.CooldownSeconds > 30*24*3600 {
		return errors.New("duration and cooldown are out of range")
	}
	if !map[string]bool{"info": true, "warning": true, "critical": true}[rule.Severity] {
		return errors.New("severity must be info, warning, or critical")
	}
	if len(rule.Scope.NodeIDs) > 1000 || len(rule.Scope.Groups) > 100 || len(rule.Scope.Excluded) > 1000 {
		return errors.New("alert scope is too large")
	}
	for _, group := range rule.Scope.Groups {
		if len(group) > 128 {
			return errors.New("alert scope group names must not exceed 128 characters")
		}
	}
	return nil
}

func (a *API) handleAdminDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := parsePositiveID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid alert rule id")
		return
	}
	if err := a.store.DeleteAlertRule(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "alert_rule_not_found", "alert rule was not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not delete alert rule")
		return
	}
	a.alerts.Reload()
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleAdminAlertEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := a.store.ListAlertEvents(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list alert events")
		return
	}
	for index := range events {
		if events[index].Link == "" {
			events[index].Link = publicBase(a.cfg) + "/nodes/" + events[index].Node.ID
		}
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.AlertEvent]{Data: events})
}

func (a *API) handleAdminNotificationChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := a.store.ListNotificationChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list notification channels")
		return
	}
	for index := range channels {
		channels[index].Config = a.redactedChannelConfig(channels[index])
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.NotificationChannel]{Data: channels})
}

func (a *API) handleAdminSaveNotificationChannel(w http.ResponseWriter, r *http.Request) {
	var channel model.NotificationChannel
	if !decodeJSON(w, r, &channel, 128<<10) {
		return
	}
	if rawID := chi.URLParam(r, "id"); rawID != "" {
		id, err := parsePositiveID(rawID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_id", "invalid notification channel id")
			return
		}
		channel.ID = id
	}
	channel.Name, channel.Type = strings.TrimSpace(channel.Name), strings.ToLower(strings.TrimSpace(channel.Type))
	if err := validateNotificationChannel(channel); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_notification_channel", err.Error())
		return
	}
	encrypted := ""
	if len(channel.Config) > 0 {
		if channel.ID > 0 {
			channel.Config = a.mergeChannelConfig(r, channel.ID, channel.Config)
		}
		for key := range channel.Config {
			if strings.HasSuffix(key, "_configured") || key == "configured" {
				delete(channel.Config, key)
			}
		}
		if err := validateChannelConfig(channel.Type, channel.Config); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_notification_config", err.Error())
			return
		}
		payload, _ := json.Marshal(channel.Config)
		var err error
		encrypted, err = a.secrets.Seal(string(payload))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption_error", "could not protect notification credentials")
			return
		}
	}
	if channel.ID == 0 && encrypted == "" {
		writeError(w, http.StatusBadRequest, "config_required", "channel configuration is required")
		return
	}
	saved, err := a.store.SaveNotificationChannel(r.Context(), channel, encrypted)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "notification_not_found", "notification channel was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not save notification channel")
		return
	}
	admin := adminFromContext(r.Context())
	_ = a.store.AppendAudit(r.Context(), admin.Username, "notification.save", strconv.FormatInt(saved.ID, 10), saved.Name, time.Now().UTC())
	saved.Config = map[string]any{"configured": true}
	status := http.StatusOK
	if channel.ID == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, model.Envelope[model.NotificationChannel]{Data: saved})
}

func (a *API) mergeChannelConfig(r *http.Request, id int64, incoming map[string]any) map[string]any {
	merged := make(map[string]any)
	channels, err := a.store.ListNotificationChannels(r.Context())
	if err == nil {
		for _, channel := range channels {
			if channel.ID != id {
				continue
			}
			if plaintext, openErr := a.secrets.Open(channel.EncryptedConfig); openErr == nil {
				_ = json.Unmarshal([]byte(plaintext), &merged)
			}
			break
		}
	}
	for key, value := range incoming {
		if text, ok := value.(string); ok && text == "" && map[string]bool{"password": true, "bot_token": true, "device_key": true, "secret": true}[key] {
			continue
		}
		merged[key] = value
	}
	return merged
}

func validateChannelConfig(channelType string, config map[string]any) error {
	text := func(key string) string {
		value, _ := config[key].(string)
		return strings.TrimSpace(value)
	}
	switch channelType {
	case "webhook":
		target, err := url.Parse(text("url"))
		if err != nil || target.Host == "" || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
			return errors.New("webhook URL is required and must use HTTP or HTTPS")
		}
	case "telegram":
		if text("bot_token") == "" || text("chat_id") == "" {
			return errors.New("Telegram bot token and chat ID are required")
		}
	case "bark":
		if text("device_key") == "" {
			return errors.New("Bark device key is required")
		}
	case "smtp":
		if text("host") == "" || strings.ContainsAny(text("host"), "\r\n\t ") || text("from") == "" || config["to"] == nil {
			return errors.New("SMTP host, from, and recipients are required")
		}
		if err := validateMailbox(text("from")); err != nil {
			return errors.New("SMTP from address is malformed")
		}
		if err := validateMailboxList(config["to"]); err != nil {
			return errors.New("one or more SMTP recipients are malformed")
		}
	}
	return nil
}

func validateMailbox(raw string) error {
	if strings.ContainsAny(raw, "\r\n") {
		return errors.New("mailbox contains a line break")
	}
	_, err := mail.ParseAddress(strings.TrimSpace(raw))
	return err
}

func validateMailboxList(raw any) error {
	var values []string
	switch typed := raw.(type) {
	case string:
		values = strings.FieldsFunc(typed, func(character rune) bool { return character == ',' || character == ';' })
	case []string:
		values = typed
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return errors.New("mailbox must be a string")
			}
			values = append(values, value)
		}
	default:
		return errors.New("mailbox list is malformed")
	}
	if len(values) == 0 {
		return errors.New("mailbox list is empty")
	}
	for _, value := range values {
		if err := validateMailbox(value); err != nil {
			return err
		}
	}
	return nil
}

func validateNotificationChannel(channel model.NotificationChannel) error {
	if channel.Name == "" || len(channel.Name) > 128 || strings.ContainsAny(channel.Name, "\r\n") {
		return errors.New("channel name is required")
	}
	if !map[string]bool{"smtp": true, "telegram": true, "bark": true, "webhook": true}[channel.Type] {
		return errors.New("channel type must be smtp, telegram, bark, or webhook")
	}
	if len(channel.Config) > 64 {
		return errors.New("channel configuration is too large")
	}
	return nil
}

func (a *API) handleAdminDeleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id, err := parsePositiveID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid notification channel id")
		return
	}
	if err := a.store.DeleteNotificationChannel(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "notification_not_found", "notification channel was not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not delete notification channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleAdminTestNotificationChannel(w http.ResponseWriter, r *http.Request) {
	id, err := parsePositiveID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "invalid notification channel id")
		return
	}
	channels, err := a.store.ListNotificationChannels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not read notification channel")
		return
	}
	var selected *model.NotificationChannel
	for index := range channels {
		if channels[index].ID == id {
			selected = &channels[index]
			break
		}
	}
	if selected == nil {
		writeError(w, http.StatusNotFound, "notification_not_found", "notification channel was not found")
		return
	}
	eventID, _ := uuid.NewV7()
	event := model.AlertEvent{
		ID: eventID.String(), Type: "test", Status: model.AlertFiring, Severity: "info",
		OccurredAt: time.Now().UTC(), Node: model.PublicNode{ID: "test", Name: "Hostpin test"},
		Metric: "test", Message: "This is a Hostpin notification test.", Link: publicBase(a.cfg),
	}
	if err := a.notifier.Send(r.Context(), *selected, event); err != nil {
		writeError(w, http.StatusBadGateway, "notification_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivered": true})
}

func (a *API) redactedChannelConfig(channel model.NotificationChannel) map[string]any {
	plaintext, err := a.secrets.Open(channel.EncryptedConfig)
	if err != nil {
		return map[string]any{"configured": false}
	}
	var config map[string]any
	if json.Unmarshal([]byte(plaintext), &config) != nil {
		return map[string]any{"configured": false}
	}
	for _, key := range []string{"password", "bot_token", "device_key", "secret"} {
		if value, exists := config[key]; exists {
			config[key+"_configured"] = fmt.Sprint(value) != ""
			delete(config, key)
		}
	}
	if headers, exists := config["headers"]; exists {
		configured := false
		if values, ok := headers.(map[string]any); ok {
			configured = len(values) > 0
		}
		config["headers_configured"] = configured
		delete(config, "headers")
	}
	config["configured"] = true
	return config
}

func (a *API) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := a.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", "could not list audit entries")
		return
	}
	writeJSON(w, http.StatusOK, model.Envelope[[]model.AuditEntry]{Data: entries})
}
