// Package config loads and validates the bridge configuration from a TOML file.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration loaded from config.toml.
type Config struct {
	Discord DiscordConfig `toml:"discord"`
	Gotify  GotifyConfig  `toml:"gotify"`
	Watch   []WatchEntry  `toml:"watch"`
}

// DiscordConfig holds Discord gateway credentials and behaviour flags.
type DiscordConfig struct {
	// UserToken is your personal Discord account token.
	UserToken string `toml:"user_token"`
	// NotifyOwn, when true, also forwards messages you send yourself.
	NotifyOwn bool `toml:"notify_own"`
	// IgnoreBots, when true, drops messages authored by bots/webhooks.
	IgnoreBots bool `toml:"ignore_bots"`
}

// GotifyConfig holds the Gotify server endpoint and application token.
type GotifyConfig struct {
	URL             string `toml:"url"`
	Token           string `toml:"token"`
	DefaultPriority int    `toml:"default_priority"`
}

// WatchEntry maps a set of channels (optionally on a named server) to forward.
type WatchEntry struct {
	GuildID    string   `toml:"guild_id"`
	ChannelIDs []string `toml:"channel_ids"`
	// Label is used as the Gotify notification title for these channels.
	Label string `toml:"label"`
	// Priority overrides gotify.default_priority for these channels.
	Priority *int `toml:"priority"`
}

// Target is the resolved forwarding rule for a single channel.
type Target struct {
	Label    string
	Priority int
	GuildID  string
}

// Load builds the config from the TOML file at path (if it exists) and then
// applies environment-variable overrides. The file is optional: if it is
// missing, configuration may come entirely from environment variables — which
// is how the Docker image is meant to be configured.
func Load(path string) (*Config, error) {
	var c Config

	if path != "" {
		switch _, statErr := os.Stat(path); {
		case statErr == nil:
			meta, err := toml.DecodeFile(path, &c)
			if err != nil {
				return nil, fmt.Errorf("decode config %q: %w", path, err)
			}
			if undecoded := meta.Undecoded(); len(undecoded) > 0 {
				keys := make([]string, len(undecoded))
				for i, k := range undecoded {
					keys[i] = k.String()
				}
				return nil, fmt.Errorf("unknown keys in config %q: %s", path, strings.Join(keys, ", "))
			}
		case os.IsNotExist(statErr):
			// No file: rely on environment variables only.
		default:
			return nil, fmt.Errorf("stat config %q: %w", path, statErr)
		}
	}

	if err := applyEnv(&c); err != nil {
		return nil, err
	}

	if c.Gotify.DefaultPriority == 0 {
		c.Gotify.DefaultPriority = 5
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// applyEnv overrides config fields from environment variables. Any value set in
// the environment takes precedence over the file.
func applyEnv(c *Config) error {
	if v := os.Getenv("DISCORD_USER_TOKEN"); v != "" {
		c.Discord.UserToken = v
	}
	if v, ok, err := boolEnv("DISCORD_NOTIFY_OWN"); err != nil {
		return err
	} else if ok {
		c.Discord.NotifyOwn = v
	}
	if v, ok, err := boolEnv("DISCORD_IGNORE_BOTS"); err != nil {
		return err
	} else if ok {
		c.Discord.IgnoreBots = v
	}
	if v := os.Getenv("GOTIFY_URL"); v != "" {
		c.Gotify.URL = v
	}
	if v := os.Getenv("GOTIFY_TOKEN"); v != "" {
		c.Gotify.Token = v
	}
	if v := os.Getenv("GOTIFY_DEFAULT_PRIORITY"); v != "" {
		p, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("GOTIFY_DEFAULT_PRIORITY: %w", err)
		}
		c.Gotify.DefaultPriority = p
	}
	if v := os.Getenv("WATCH"); v != "" {
		entries, err := parseWatchEnv(v)
		if err != nil {
			return err
		}
		c.Watch = entries // env replaces the file's watch list entirely
	}
	return nil
}

// parseWatchEnv parses the WATCH env var. Format: entries separated by ';',
// each entry is "label|channelid,channelid,...[|priority]".
// Example: WATCH="My Server #alerts|222,333|8; Other|444"
func parseWatchEnv(s string) ([]WatchEntry, error) {
	var out []WatchEntry
	for _, raw := range strings.Split(s, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, "|")
		if len(parts) < 2 {
			return nil, fmt.Errorf("WATCH entry %q must be 'label|channels[|priority]'", raw)
		}
		e := WatchEntry{Label: strings.TrimSpace(parts[0])}
		for _, ch := range strings.Split(parts[1], ",") {
			if ch = strings.TrimSpace(ch); ch != "" {
				e.ChannelIDs = append(e.ChannelIDs, ch)
			}
		}
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			p, err := strconv.Atoi(strings.TrimSpace(parts[2]))
			if err != nil {
				return nil, fmt.Errorf("WATCH entry %q priority: %w", raw, err)
			}
			e.Priority = &p
		}
		out = append(out, e)
	}
	return out, nil
}

// boolEnv reads a boolean env var. The bool is the parsed value; ok is false
// when the variable is unset (so the existing config value is kept).
func boolEnv(key string) (val bool, ok bool, err error) {
	s := os.Getenv(key)
	if s == "" {
		return false, false, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(s))
	if err != nil {
		return false, false, fmt.Errorf("%s: %w", key, err)
	}
	return b, true, nil
}

func (c *Config) validate() error {
	switch {
	case c.Discord.UserToken == "":
		return fmt.Errorf("discord.user_token (or env DISCORD_USER_TOKEN) is required")
	case c.Gotify.URL == "":
		return fmt.Errorf("gotify.url (or env GOTIFY_URL) is required")
	case c.Gotify.Token == "":
		return fmt.Errorf("gotify.token (or env GOTIFY_TOKEN) is required")
	case len(c.Watch) == 0:
		return fmt.Errorf("at least one [[watch]] entry (or env WATCH) is required")
	}
	for i, w := range c.Watch {
		if len(w.ChannelIDs) == 0 {
			return fmt.Errorf("watch[%d] (%q) has no channel_ids", i, w.Label)
		}
	}
	return nil
}

// Targets builds a channel-ID -> Target index from the watch list.
func (c *Config) Targets() map[string]Target {
	m := make(map[string]Target)
	for _, w := range c.Watch {
		prio := c.Gotify.DefaultPriority
		if w.Priority != nil {
			prio = *w.Priority
		}
		for _, ch := range w.ChannelIDs {
			m[ch] = Target{Label: w.Label, Priority: prio, GuildID: w.GuildID}
		}
	}
	return m
}

// ResolvePath returns the config path from the CONFIG_PATH env var if set,
// otherwise the provided default.
func ResolvePath(def string) string {
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		return p
	}
	return def
}
