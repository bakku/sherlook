package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"bakku.dev/sherlook/internal/graph"
)

// formatListDate is compact: time for today, day+month this year, else full date.
func formatListDate(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("02 Jan")
	default:
		return t.Format("2006-01-02")
	}
}

func formatFullDate(t time.Time) string {
	return t.Local().Format("Mon, 02 Jan 2006 15:04")
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

const maxSubjectInFilename = 80

// exportFilename builds `YYYY-MM-DD_<subject>.eml` with a filesystem-safe subject.
func exportFilename(m graph.MessageSummary) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.TrimSpace(m.Subject) {
		bad := unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsSpace(r)
		if bad {
			if !lastUnderscore {
				b.WriteRune('_')
			}
			lastUnderscore = true
			continue
		}
		b.WriteRune(r)
		lastUnderscore = false
	}
	subject := strings.Trim(b.String(), "._")
	if r := []rune(subject); len(r) > maxSubjectInFilename {
		subject = strings.TrimRight(string(r[:maxSubjectInFilename]), "._")
	}
	if subject == "" {
		subject = "no-subject"
	}
	return m.ReceivedDateTime.Local().Format("2006-01-02") + "_" + subject + ".eml"
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// describeError turns client errors into actionable messages.
func describeError(err error) string {
	var auth *graph.AuthError
	var api *graph.APIError
	switch {
	case errors.As(err, &auth):
		return auth.Error() + " (check tenant ID, client ID and secret)"
	case errors.As(err, &api):
		switch {
		case api.Status == 401:
			return fmt.Sprintf("unauthorized (%s): the token was rejected; check that the app has the Mail.ReadWrite application permission with admin consent", api.Code)
		case api.Status == 403:
			return fmt.Sprintf("access denied (%s): the app has no access to this mailbox; check admin consent for Mail.ReadWrite (application) and the RBAC for Applications / access policy scope", api.Code)
		case api.Status == 404 && api.Code == "ErrorItemNotFound":
			return "message not found; it may have been moved or deleted elsewhere (press R to refresh)"
		case api.Status == 404:
			return fmt.Sprintf("mailbox not found (%s): check the address, or the app's RBAC scope", api.Code)
		case api.Status == 429:
			return "throttled by Microsoft Graph; try again in a moment"
		}
		return api.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	}
	return err.Error()
}
