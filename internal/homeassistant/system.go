package homeassistant

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"
)

var configEntryIDRe = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

func validateConfigEntryID(id string) error {
	if !configEntryIDRe.MatchString(id) {
		return invalidArg("config entry id %q must be alphanumeric", id)
	}
	return nil
}

// SystemLogEntry is a deduplicated warning or error from the system log.
type SystemLogEntry struct {
	Name    string   `json:"name"`
	Message []string `json:"message"`
	Level   string   `json:"level"`
	// Source is [file, line].
	Source        []any    `json:"source"`
	Timestamp     UnixTime `json:"timestamp"`
	FirstOccurred UnixTime `json:"first_occurred"`
	Exception     string   `json:"exception"`
	Count         int      `json:"count"`
}

// ListSystemLog returns recent warnings and errors (admin only).
func (c *Client) ListSystemLog(ctx context.Context) ([]SystemLogEntry, error) {
	var out []SystemLogEntry
	err := c.wsCall(ctx, "system_log/list", nil, &out)
	return out, err
}

// ConfigEntry is an integration instance.
type ConfigEntry struct {
	EntryID                string   `json:"entry_id"`
	Domain                 string   `json:"domain"`
	Title                  string   `json:"title"`
	Source                 string   `json:"source"`
	State                  string   `json:"state"`
	Reason                 *string  `json:"reason"`
	DisabledBy             *string  `json:"disabled_by"`
	SupportsOptions        bool     `json:"supports_options"`
	SupportsRemoveDevice   bool     `json:"supports_remove_device"`
	SupportsUnload         bool     `json:"supports_unload"`
	SupportsReconfigure    bool     `json:"supports_reconfigure"`
	PrefDisableNewEntities bool     `json:"pref_disable_new_entities"`
	PrefDisablePolling     bool     `json:"pref_disable_polling"`
	NumSubentries          int      `json:"num_subentries"`
	CreatedAt              UnixTime `json:"created_at"`
	ModifiedAt             UnixTime `json:"modified_at"`
}

// ListConfigEntries lists config entries, optionally for one domain.
func (c *Client) ListConfigEntries(ctx context.Context, domain string) ([]ConfigEntry, error) {
	payload := map[string]any{}
	if domain != "" {
		if err := validateSlug("domain", domain); err != nil {
			return nil, err
		}
		payload["domain"] = domain
	}
	var out []ConfigEntry
	err := c.wsCall(ctx, "config_entries/get", payload, &out)
	return out, err
}

// GetConfigEntry returns one config entry (admin only).
func (c *Client) GetConfigEntry(ctx context.Context, entryID string) (*ConfigEntry, error) {
	if err := validateConfigEntryID(entryID); err != nil {
		return nil, err
	}
	var out struct {
		ConfigEntry ConfigEntry `json:"config_entry"`
	}
	if err := c.wsCall(ctx, "config_entries/get_single", map[string]any{"entry_id": entryID}, &out); err != nil {
		return nil, err
	}
	return &out.ConfigEntry, nil
}

// ReloadConfigEntry reloads an integration instance (admin only). It returns
// true when HA needs a restart to finish.
func (c *Client) ReloadConfigEntry(ctx context.Context, entryID string) (bool, error) {
	if err := validateConfigEntryID(entryID); err != nil {
		return false, err
	}
	var out struct {
		RequireRestart bool `json:"require_restart"`
	}
	err := c.doJSON(ctx, restRequest{
		method:  http.MethodPost,
		path:    []string{"api", "config", "config_entries", "entry", entryID, "reload"},
		timeout: 2 * time.Minute,
	}, &out)
	return out.RequireRestart, err
}

// SetConfigEntryDisabled disables (true) or enables (false) a config entry
// (admin only). It returns true when HA needs a restart to finish.
func (c *Client) SetConfigEntryDisabled(ctx context.Context, entryID string, disabled bool) (bool, error) {
	if err := validateConfigEntryID(entryID); err != nil {
		return false, err
	}
	var disabledBy any
	if disabled {
		disabledBy = "user"
	}
	var out struct {
		RequireRestart bool `json:"require_restart"`
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := c.wsCall(ctx, "config_entries/disable", map[string]any{"entry_id": entryID, "disabled_by": disabledBy}, &out)
	return out.RequireRestart, err
}

// Backup describes a backup known to the backup manager.
type Backup struct {
	BackupID              string                      `json:"backup_id"`
	Name                  string                      `json:"name"`
	Date                  string                      `json:"date"`
	DatabaseIncluded      bool                        `json:"database_included"`
	HomeassistantIncluded bool                        `json:"homeassistant_included"`
	HomeassistantVersion  *string                     `json:"homeassistant_version"`
	Folders               []string                    `json:"folders"`
	Addons                []json.RawMessage           `json:"addons"`
	Agents                map[string]BackupAgentState `json:"agents"`
	ExtraMetadata         map[string]any              `json:"extra_metadata"`
	FailedAgentIDs        []string                    `json:"failed_agent_ids"`
	WithAutomaticSettings *bool                       `json:"with_automatic_settings"`
}

// BackupAgentState is a backup's status in one storage location.
type BackupAgentState struct {
	Protected bool  `json:"protected"`
	Size      int64 `json:"size"`
}

// BackupInfo is the backup manager overview.
type BackupInfo struct {
	Backups                       []Backup          `json:"backups"`
	AgentErrors                   map[string]string `json:"agent_errors"`
	LastAttemptedAutomaticBackup  *string           `json:"last_attempted_automatic_backup"`
	LastCompletedAutomaticBackup  *string           `json:"last_completed_automatic_backup"`
	NextAutomaticBackup           *string           `json:"next_automatic_backup"`
	NextAutomaticBackupAdditional bool              `json:"next_automatic_backup_additional"`
	LastActionEvent               json.RawMessage   `json:"last_action_event"`
	State                         string            `json:"state"`
}

func (c *Client) BackupInfo(ctx context.Context) (*BackupInfo, error) {
	var out BackupInfo
	if err := c.wsCall(ctx, "backup/info", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BackupConfig is the automatic backup setup from Settings > System > Backups.
// The encryption password is never decoded into it.
type BackupConfig struct {
	// AutomaticBackupsConfigured is false until the automatic backup setup
	// has been completed in the UI.
	AutomaticBackupsConfigured    bool                         `json:"automatic_backups_configured"`
	Agents                        map[string]BackupAgentConfig `json:"agents"`
	CreateBackup                  BackupCreateSettings         `json:"create_backup"`
	Retention                     BackupRetention              `json:"retention"`
	Schedule                      BackupSchedule               `json:"schedule"`
	LastAttemptedAutomaticBackup  *string                      `json:"last_attempted_automatic_backup"`
	LastCompletedAutomaticBackup  *string                      `json:"last_completed_automatic_backup"`
	NextAutomaticBackup           *string                      `json:"next_automatic_backup"`
	NextAutomaticBackupAdditional bool                         `json:"next_automatic_backup_additional"`
}

// BackupAgentConfig holds the settings of one storage location. A nil
// Retention means the location follows the global retention.
type BackupAgentConfig struct {
	Protected bool             `json:"protected"`
	Retention *BackupRetention `json:"retention"`
}

// BackupCreateSettings is what an automatic backup includes and where it goes.
type BackupCreateSettings struct {
	AgentIDs         []string `json:"agent_ids"`
	IncludeAddons    []string `json:"include_addons"`
	IncludeAllAddons bool     `json:"include_all_addons"`
	IncludeDatabase  bool     `json:"include_database"`
	IncludeFolders   []string `json:"include_folders"`
	Name             *string  `json:"name"`
	// Encrypted reports whether an encryption password is set.
	Encrypted bool `json:"-"`
}

func (s *BackupCreateSettings) UnmarshalJSON(b []byte) error {
	type plain BackupCreateSettings
	var v struct {
		plain
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*s = BackupCreateSettings(v.plain)
	s.Encrypted = v.Password != nil && *v.Password != ""
	return nil
}

// BackupRetention keeps the newest Copies backups or those younger than Days.
// Both nil keeps backups forever.
type BackupRetention struct {
	Copies *int `json:"copies"`
	Days   *int `json:"days"`
}

// BackupSchedule is when automatic backups run. Recurrence is "never",
// "daily" or "custom_days", the last on Days such as "mon". A nil Time lets
// HA pick a time in its default window.
type BackupSchedule struct {
	Recurrence string   `json:"recurrence"`
	Days       []string `json:"days"`
	Time       *string  `json:"time"`
}

// BackupConfig returns the automatic backup settings (admin only).
func (c *Client) BackupConfig(ctx context.Context) (*BackupConfig, error) {
	var out struct {
		Config BackupConfig `json:"config"`
	}
	if err := c.wsCall(ctx, "backup/config/info", nil, &out); err != nil {
		return nil, err
	}
	return &out.Config, nil
}

// BackupDetails returns one backup; Backup is nil if it does not exist.
func (c *Client) BackupDetails(ctx context.Context, backupID string) (*Backup, map[string]string, error) {
	if err := validateRegistryID("backup", backupID); err != nil {
		return nil, nil, err
	}
	var out struct {
		AgentErrors map[string]string `json:"agent_errors"`
		Backup      *Backup           `json:"backup"`
	}
	err := c.wsCall(ctx, "backup/details", map[string]any{"backup_id": backupID}, &out)
	return out.Backup, out.AgentErrors, err
}

// GenerateBackup starts a backup using the user's automatic backup settings
// (storage locations, encryption password, included data) and returns the job
// id. Using the stored settings keeps backup passwords out of callers' hands
// and makes the backup land where the user expects; it fails until automatic
// backups have been configured in the UI. The backup runs in the background;
// poll BackupInfo (State returns to "idle") to find it.
func (c *Client) GenerateBackup(ctx context.Context) (string, error) {
	var out struct {
		BackupJobID string `json:"backup_job_id"`
	}
	err := c.wsCall(ctx, "backup/generate_with_automatic_settings", nil, &out)
	return out.BackupJobID, err
}

// PersistentNotification is a notification shown in the HA sidebar. Create
// and dismiss them with the persistent_notification.create/dismiss services.
type PersistentNotification struct {
	NotificationID string    `json:"notification_id"`
	Title          *string   `json:"title"`
	Message        string    `json:"message"`
	CreatedAt      time.Time `json:"created_at"`
}

func (c *Client) ListNotifications(ctx context.Context) ([]PersistentNotification, error) {
	var out []PersistentNotification
	err := c.wsCall(ctx, "persistent_notification/get", nil, &out)
	return out, err
}
