// Package dbbudget enforces the deployment-owned Job connection envelope before work starts.
package dbbudget

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/jackc/pgx/v5/stdlib"
	"time"
)

type identity struct {
	current, session                                      string
	limit                                                 int
	login, super, createRole, createDB, replicate, bypass bool
}

func (r identity) valid(expected string, maximum int) bool {
	return maximum > 0 && r.current == expected && r.session == expected && r.limit > 0 && r.limit <= maximum && r.login && !r.super && !r.createRole && !r.createDB && !r.replicate && !r.bypass
}

// Open checks the authenticated login (not only SET ROLE's effective identity).
// It never changes roles, credentials or server limits. API pools do not use it.
func Open(dsn, expected string, maximum, open, idle int) (*sql.DB, error) {
	if open < 1 || open > maximum || idle < 0 || idle > open {
		return nil, errors.New("invalid Job pool budget")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("Job database configuration unavailable")
	}
	db.SetMaxOpenConns(open)
	db.SetMaxIdleConns(idle)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var r identity
	err = db.QueryRowContext(ctx, `SELECT current_user::text,session_user::text,rolconnlimit,rolcanlogin,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=session_user`).Scan(&r.current, &r.session, &r.limit, &r.login, &r.super, &r.createRole, &r.createDB, &r.replicate, &r.bypass)
	if err != nil || !r.valid(expected, maximum) {
		db.Close()
		return nil, errors.New("Job database role does not satisfy connection budget")
	}
	return db, nil
}
