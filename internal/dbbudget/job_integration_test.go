package dbbudget

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestRealJobRoleGate(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("loopback fixture required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	role := fmt.Sprintf("budget_fixture_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE ROLE ` + role + ` LOGIN PASSWORD 'local-fixture' CONNECTION LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP ROLE ` + role)
	u.User = url.UserPassword(role, "local-fixture")
	good, err := Open(u.String(), role, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = good.Exec("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	good.Close()
	for _, tc := range []struct{ sql, expected string }{
		{`ALTER ROLE ` + role + ` CONNECTION LIMIT -1`, role},
		{`ALTER ROLE ` + role + ` CONNECTION LIMIT 1 CREATEROLE`, role},
		{`ALTER ROLE ` + role + ` NOCREATEROLE`, "wrong_expected_role"},
	} {
		if _, err = admin.Exec(tc.sql); err != nil {
			t.Fatal(err)
		}
		if db, e := Open(u.String(), tc.expected, 1, 1, 1); e == nil {
			db.Close()
			t.Fatal("unsafe identity admitted")
		} else if strings.Contains(e.Error(), u.String()) {
			t.Fatal("credential leak")
		}
	}
	switched, _ := url.Parse(dsn)
	q := switched.Query()
	q.Set("options", "-c role="+role)
	switched.RawQuery = q.Encode()
	if db, e := Open(switched.String(), role, 1, 1, 1); e == nil {
		db.Close()
		t.Fatal("SET ROLE bypass admitted")
	}
}
