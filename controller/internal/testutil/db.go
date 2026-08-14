// SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: LicenseRef-NvidiaProprietary
//
// NVIDIA CORPORATION, its affiliates and licensors retain all intellectual
// property and proprietary rights in and to this material, related
// documentation and any modifications thereto. Any use, reproduction,
// disclosure or distribution of this material and related documentation
// without an express license agreement from NVIDIA CORPORATION or
// its affiliates is strictly prohibited.

// Package testutil provides test utilities for the cloud-workflow-engine project.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
)

// SetupDB creates an in-memory SQLite database for testing.
// It registers the provided models and creates their tables.
// The database is automatically closed when the test completes.
func SetupDB(ctx context.Context, t *testing.T, models ...interface{}) *bun.DB {
	t.Helper()

	driverName, err := registerDriver()
	require.NoError(t, err)

	// Each test gets a unique in-memory database by using a UUID in the filename.
	// cache=shared allows multiple connections within the same sql.DB pool to share
	// the database, but different tests get completely isolated databases.
	sqldb, err := sql.Open(driverName,
		fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString()),
	)
	require.NoError(t, err)

	bunDB := bun.NewDB(sqldb, sqlitedialect.New())

	for _, model := range models {
		require.NoError(t, createTableForSQLite(ctx, bunDB, model))
	}

	t.Cleanup(func() {
		bunDB.Close()
	})

	return bunDB
}

//nolint:gochecknoglobals // sync.Once must be at package level to ensure single initialization
var registerDriverOnce = sync.Once{}

// sqliteDriverName is our own driver registration, separate from the "sqlite3"
// one go-sqlite3 registers in its init. Registering our own is what lets the
// gen_random_uuid hook be attached; the previous driver could not do this, which
// is why this used to return a fixed name.
const sqliteDriverName = "sqlite3_nke"

// registerDriver registers a SQLite driver carrying a custom gen_random_uuid
// function, and returns its name for sql.Open.
//
// SQLite has no gen_random_uuid(), but most of our models use it as the DEFAULT
// for their ID column, because the real datastore is PostgreSQL.
//
// Requires cgo. go-sqlite3 compiles to a stub when CGO_ENABLED=0 and then fails
// at the first query rather than at build time, so a test binary built without
// cgo will fail here with "requires cgo to work".
//
//nolint:nonamedreturns // named returns improve readability without using naked returns
func registerDriver() (driverName string, err error) {
	registerDriverOnce.Do(func() {
		sql.Register(sqliteDriverName, &sqlite3.SQLiteDriver{
			ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				// pure=false is required, not stylistic. gen_random_uuid takes no
				// arguments, so declaring it pure lets SQLite treat it as
				// deterministic, evaluate it once and reuse that value for every
				// row -- giving every record the same ID.
				return conn.RegisterFunc("gen_random_uuid", uuid.NewString, false)
			},
		})
	})
	if err != nil {
		return "", err
	}

	return sqliteDriverName, nil
}

func createTableForSQLite(ctx context.Context, db *bun.DB, model interface{}) error {
	q := db.NewCreateTable().Model(model).IfNotExists()

	queryBytes, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		return err
	}

	queryStr := string(queryBytes)

	// SQLite requires expressions in DEFAULT to be in parentheses, this is different from PostgreSQL.
	// We look for "DEFAULT gen_random_uuid()" and replace it with "DEFAULT (gen_random_uuid())"
	// If we start using other functions for defaults, we will need to update this, for now I choose
	// to not make this generic.
	queryStr = strings.ReplaceAll(queryStr, "DEFAULT gen_random_uuid()", "DEFAULT (gen_random_uuid())")

	_, err = db.ExecContext(ctx, queryStr)
	return err
}
