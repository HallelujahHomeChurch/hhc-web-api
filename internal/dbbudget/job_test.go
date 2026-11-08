package dbbudget

import "testing"

func TestJobLoginBudget(t *testing.T) {
	valid := identity{current: "jobs", session: "jobs", limit: 24, login: true}
	if !valid.valid("jobs", 24) {
		t.Fatal("valid capped login rejected")
	}
	cases := []struct {
		name   string
		change func(*identity)
	}{
		{"wrong_login", func(r *identity) { r.current = "other"; r.session = "other" }},
		{"set_role", func(r *identity) { r.session = "admin" }},
		{"unlimited", func(r *identity) { r.limit = -1 }},
		{"over_budget", func(r *identity) { r.limit = 25 }},
		{"disabled", func(r *identity) { r.login = false }},
		{"superuser", func(r *identity) { r.super = true }},
		{"create_role", func(r *identity) { r.createRole = true }},
		{"create_database", func(r *identity) { r.createDB = true }},
		{"replication", func(r *identity) { r.replicate = true }},
		{"bypass_rls", func(r *identity) { r.bypass = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.change(&r)
			if r.valid("jobs", 24) {
				t.Fatal("unsafe role accepted")
			}
		})
	}
}
func TestPoolAboveBudgetFailsBeforeConnect(t *testing.T) {
	if _, err := Open("invalid-private-dsn", "jobs", 1, 2, 1); err == nil {
		t.Fatal("pool above role budget accepted")
	}
}
