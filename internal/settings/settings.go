// Package settings is the typed view of the admin Settings tab: defaults,
// reading from the stored key/value pairs, and validating a submitted form.
package settings

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Limits and defaults.
const (
	DefaultSessionHours     = 24 * 30
	MaxSessionHours         = 24 * 90
	DefaultShareExpiryDays  = 30
	MaxShareExpiryDays      = 365 // a public link may never live longer than a year
	DefaultTimezone         = "UTC"
	UnitsUS                 = "us"
	UnitsMetric             = "metric"
	maxBaseURLLength        = 200
	maxTimezoneNameLength   = 64
	keyPublicBaseURL        = "public_base_url"
	keyTimezone             = "timezone"
	keyUnits                = "units"
	keyWeekStart            = "week_start"
	keySessionHours         = "session_hours"
	keyShareDefaultExpiryDy = "share_default_days"
)

// Settings are the general settings.
type Settings struct {
	// PublicBaseURL is the https address the app is reached at; emailed
	// links and share links are built from it. Empty until set.
	PublicBaseURL string
	// Timezone is an IANA name, used for the planner's days.
	Timezone string
	// Units is UnitsUS or UnitsMetric, the default display of amounts.
	Units string
	// WeekStart is the first day of the planner week, 0 = Sunday.
	WeekStart int
	// SessionHours is how long a sign-in lasts.
	SessionHours int
	// ShareDefaultDays pre-fills a new share link's expiry.
	ShareDefaultDays int
}

// Defaults are the values used until an admin changes them.
func Defaults() Settings {
	return Settings{
		Timezone:         DefaultTimezone,
		Units:            UnitsUS,
		WeekStart:        0,
		SessionHours:     DefaultSessionHours,
		ShareDefaultDays: DefaultShareExpiryDays,
	}
}

// FromMap builds Settings from stored pairs. A missing or unreadable value
// falls back to its default, so a bad row can never lock anyone out.
func FromMap(m map[string]string) Settings {
	s := Defaults()
	if v, ok := m[keyPublicBaseURL]; ok {
		if clean, msg := cleanBaseURL(v); msg == "" {
			s.PublicBaseURL = clean
		}
	}
	if v, ok := m[keyTimezone]; ok {
		if _, err := time.LoadLocation(v); err == nil && v != "" {
			s.Timezone = v
		}
	}
	if v := m[keyUnits]; v == UnitsUS || v == UnitsMetric {
		s.Units = v
	}
	if n, err := strconv.Atoi(m[keyWeekStart]); err == nil && n >= 0 && n <= 6 {
		s.WeekStart = n
	}
	if n, err := strconv.Atoi(m[keySessionHours]); err == nil && n >= 1 && n <= MaxSessionHours {
		s.SessionHours = n
	}
	if n, err := strconv.Atoi(m[keyShareDefaultExpiryDy]); err == nil && n >= 1 && n <= MaxShareExpiryDays {
		s.ShareDefaultDays = n
	}
	return s
}

// Map is the stored form.
func (s Settings) Map() map[string]string {
	return map[string]string{
		keyPublicBaseURL:        s.PublicBaseURL,
		keyTimezone:             s.Timezone,
		keyUnits:                s.Units,
		keyWeekStart:            strconv.Itoa(s.WeekStart),
		keySessionHours:         strconv.Itoa(s.SessionHours),
		keyShareDefaultExpiryDy: strconv.Itoa(s.ShareDefaultDays),
	}
}

// Form field names, shared with the template.
const (
	FieldPublicBaseURL = keyPublicBaseURL
	FieldTimezone      = keyTimezone
	FieldUnits         = keyUnits
	FieldWeekStart     = keyWeekStart
	FieldSessionHours  = keySessionHours
	FieldShareDays     = keyShareDefaultExpiryDy
)

// Parse validates a submitted form. It returns the settings and, per field,
// a message for each invalid value; the settings are only usable when the
// error map is empty.
func Parse(get func(string) string) (Settings, map[string]string) {
	errs := map[string]string{}
	s := Defaults()

	if v := strings.TrimSpace(get(FieldPublicBaseURL)); v != "" {
		clean, msg := cleanBaseURL(v)
		if msg != "" {
			errs[FieldPublicBaseURL] = msg
		}
		s.PublicBaseURL = clean
	}

	tz := strings.TrimSpace(get(FieldTimezone))
	switch {
	case tz == "":
		errs[FieldTimezone] = "Enter a timezone such as America/Denver."
	case len(tz) > maxTimezoneNameLength:
		errs[FieldTimezone] = "That timezone name is too long."
	default:
		if _, err := time.LoadLocation(tz); err != nil {
			errs[FieldTimezone] = "Unknown timezone. Use a name like America/Denver or UTC."
		}
		s.Timezone = tz
	}

	switch u := get(FieldUnits); u {
	case UnitsUS, UnitsMetric:
		s.Units = u
	default:
		errs[FieldUnits] = "Choose US or metric."
	}

	if n, err := strconv.Atoi(strings.TrimSpace(get(FieldWeekStart))); err != nil || n < 0 || n > 6 {
		errs[FieldWeekStart] = "Choose a day."
	} else {
		s.WeekStart = n
	}

	if n, err := strconv.Atoi(strings.TrimSpace(get(FieldSessionHours))); err != nil || n < 1 || n > MaxSessionHours {
		errs[FieldSessionHours] = fmt.Sprintf("Enter a number of hours from 1 to %d.", MaxSessionHours)
	} else {
		s.SessionHours = n
	}

	if n, err := strconv.Atoi(strings.TrimSpace(get(FieldShareDays))); err != nil || n < 1 || n > MaxShareExpiryDays {
		errs[FieldShareDays] = fmt.Sprintf("Enter a number of days from 1 to %d.", MaxShareExpiryDays)
	} else {
		s.ShareDefaultDays = n
	}
	return s, errs
}

// cleanBaseURL accepts only an https origin (no path, query or credentials)
// and returns it without a trailing slash, or a message saying what is wrong.
// The app is HTTPS-only: passkeys and Secure cookies need it.
func cleanBaseURL(raw string) (clean, msg string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if len(raw) > maxBaseURLLength {
		return "", "That address is too long."
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "Enter a full address such as https://shopping.example.com."
	}
	if u.Scheme != "https" {
		return "", "The address must start with https://."
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", "Use just the address, with no path, query or login."
	}
	return "https://" + u.Host, ""
}
