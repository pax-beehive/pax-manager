package userapi

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func sanitizeHarnessAuthAccount(account *domain.HarnessAuthAccount) *domain.HarnessAuthAccount {
	if account == nil {
		return nil
	}
	clean := domain.HarnessAuthAccount{
		Email:        harnessAuthAccountField(account.Email, 254),
		DisplayName:  harnessAuthAccountField(account.DisplayName, 256),
		Organization: harnessAuthAccountField(account.Organization, 256),
		Plan:         harnessAuthAccountField(account.Plan, 128),
	}
	if clean == (domain.HarnessAuthAccount{}) {
		return nil
	}
	return &clean
}

func harnessAuthAccountField(value string, maxBytes int) string {
	if !utf8.ValidString(value) {
		return ""
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ""
		}
	}
	value = strings.TrimSpace(value)
	if len(value) > maxBytes {
		return ""
	}
	return value
}
