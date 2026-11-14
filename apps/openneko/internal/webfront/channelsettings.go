package webfront

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/open-neko/neko/apps/openneko/internal/secrets"
)

const maxChannelSetting = 8192

var channelIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type channelSettingsError string

func (e channelSettingsError) Error() string { return string(e) }

func channelDirections() []any { return []any{"outbound", "inbound"} }

// channelSettingsView is channelSettings() of the worker's channel runtime:
// secret values report only whether they are set.
func channelSettingsView(store secrets.Store) []any {
	out := make([]any, 0, len(builtInChannels))
	for i := range builtInChannels {
		ch := &builtInChannels[i]
		env := store["channel."+ch.ID]
		fields := []any{}
		missing := []any{}
		for _, s := range ch.Settings {
			if s.Autogenerate {
				continue
			}
			value := env[s.Key]
			f := jsObjOf("key", s.Key, "required", s.Required, "secret", s.Secret, "description", s.Description, "set", value != "")
			if value != "" && !s.Secret {
				f.set("value", value)
			}
			fields = append(fields, f)
			if s.Required && value == "" {
				missing = append(missing, s.Key)
			}
		}
		out = append(out, jsObjOf("id", ch.ID, "providerLabel", ch.Label, "directions", channelDirections(),
			"ingress", channelInbound[ch.ID].ingress, "enabled", channelEnabled(ch, env), "fields", fields, "missing", missing))
	}
	return out
}

// adminChannelSettings is GET and POST /api/v1/admin/channels/settings.
func (a *API) adminChannelSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.settingsAdmin(w, r); !ok {
		return
	}
	if r.Method == http.MethodGet {
		store, err := secrets.Read(a.SecretsDir)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sendJSValue(w, http.StatusOK, jsObjOf("channels", channelSettingsView(store)))
		return
	}
	body, _ := readBody(r)
	channel, _ := fieldString(body, "channel")
	values, hasValues := field(body, "values")
	state, hasState := field(body, "state")
	if !channelIDPattern.MatchString(channel) {
		sendError(w, http.StatusBadRequest, "channel must be a channel id")
		return
	}
	var err error
	if hasState {
		switch {
		case hasValues:
			sendError(w, http.StatusBadRequest, "send values or state, not both")
			return
		case state != "on" && state != "off":
			sendError(w, http.StatusBadRequest, "state must be on or off")
			return
		}
		err = a.setChannelState(channel, state.(string))
	} else {
		m, ok := values.(map[string]any)
		if !ok || len(m) == 0 {
			sendError(w, http.StatusBadRequest, "values must map at least one setting to a string or null")
			return
		}
		for _, v := range m {
			if s, isText := v.(string); v != nil && (!isText || s == "" || jsLength(s) > maxChannelSetting) {
				sendError(w, http.StatusBadRequest, fmt.Sprintf("each value must be null or a string of 1 to %d characters", maxChannelSetting))
				return
			}
		}
		err = a.setChannelSettings(channel, m)
	}
	var refused channelSettingsError
	if errors.As(err, &refused) {
		sendError(w, http.StatusBadRequest, refused.Error())
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.channelsChanged()
	sendJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func requireChannel(id string) (*builtInChannel, error) {
	if ch := findBuiltInChannel(id); ch != nil {
		return ch, nil
	}
	return nil, channelSettingsError("unknown channel " + id)
}

// setChannelSettings writes or clears declared settings of one channel.
func (a *API) setChannelSettings(id string, values map[string]any) error {
	ch, err := requireChannel(id)
	if err != nil {
		return err
	}
	keys := slices.Sorted(maps.Keys(values))
	for _, k := range keys {
		if !slices.ContainsFunc(ch.Settings, func(s channelSetting) bool { return s.Key == k && !s.Autogenerate }) {
			return channelSettingsError(ch.ID + " does not declare " + k)
		}
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return err
	}
	section := maps.Clone(store["channel."+ch.ID])
	if section == nil {
		section = map[string]string{}
	}
	for _, k := range keys {
		if s, ok := values[k].(string); ok {
			section[k] = s
		} else {
			delete(section, k)
		}
	}
	store["channel."+ch.ID] = section
	return secrets.Write(store, a.SecretsDir)
}

// setChannelState turns a channel on by minting the settings OpenNeko
// generates, or off by removing every stored setting.
func (a *API) setChannelState(id, state string) error {
	ch, err := requireChannel(id)
	if err != nil {
		return err
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return err
	}
	section := "channel." + ch.ID
	if state == "off" {
		if _, ok := store[section]; !ok {
			return nil
		}
		delete(store, section)
		return secrets.Write(store, a.SecretsDir)
	}
	env := maps.Clone(store[section])
	if env == nil {
		env = map[string]string{}
	}
	var missing []string
	for _, s := range ch.Settings {
		if s.Required && !s.Autogenerate && jsTrim(env[s.Key]) == "" {
			missing = append(missing, s.Key)
		}
	}
	if len(missing) > 0 {
		return channelSettingsError(fmt.Sprintf("%s needs %s before it can turn on", ch.ID, strings.Join(missing, ", ")))
	}
	changed := false
	for _, s := range ch.Settings {
		if s.Autogenerate && env[s.Key] == "" {
			b := make([]byte, 48)
			if _, err := io.ReadFull(rand.Reader, b); err != nil {
				return err
			}
			env[s.Key] = base64.RawURLEncoding.EncodeToString(b)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	store[section] = env
	return secrets.Write(store, a.SecretsDir)
}

// MigrateLegacyChannelSecrets moves channel settings once from the sections
// the channel plugins used to `channel.<id>`. Values already in the new
// section win.
func (a *API) MigrateLegacyChannelSecrets() ([]string, error) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	store, err := secrets.Read(a.SecretsDir)
	if err != nil {
		return nil, err
	}
	var moved []string
	for legacy, id := range legacyChannelNames {
		old, ok := store[legacy]
		if !ok {
			continue
		}
		ch := findBuiltInChannel(id)
		next := map[string]string{}
		for _, s := range ch.Settings {
			if v, ok := old[s.Key]; ok {
				next[s.Key] = v
			}
		}
		maps.Copy(next, store["channel."+id])
		store["channel."+id] = next
		delete(store, legacy)
		moved = append(moved, legacy)
	}
	if len(moved) == 0 {
		return nil, nil
	}
	sort.Strings(moved)
	if err := secrets.Write(store, a.SecretsDir); err != nil {
		return nil, err
	}
	log.Printf("[channels] moved channel settings from %s", strings.Join(moved, ", "))
	return moved, nil
}
