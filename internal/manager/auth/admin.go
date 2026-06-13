package auth

import "github.com/pax-beehive/pax-manager/internal/manager/domain"

type AdminPolicy interface {
	IsAdmin(email string) bool
	RoleForEmail(email string) string
}

type StaticAdminPolicy struct {
	admins map[string]bool
}

func NewStaticAdminPolicy(admins map[string]bool) StaticAdminPolicy {
	out := make(map[string]bool, len(admins))
	for email, enabled := range admins {
		if enabled {
			out[domain.NormalizeEmail(email)] = true
		}
	}
	return StaticAdminPolicy{admins: out}
}

func (p StaticAdminPolicy) IsAdmin(email string) bool {
	return p.admins[domain.NormalizeEmail(email)]
}

func (p StaticAdminPolicy) RoleForEmail(email string) string {
	if p.IsAdmin(email) {
		return "admin"
	}
	return "user"
}
