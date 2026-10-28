package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"github.com/jackc/pgx/v5"
)

func TestWorkspacePersistsDraftsPerUserAndRejectsConcurrentOverwrite(t *testing.T) {
	svc, row, _, _ := workbenchFixture(t)
	ctx := context.Background()
	alice := fmt.Sprintf("alice-%d", row.ID)
	bob := fmt.Sprintf("bob-%d", row.ID)
	state := DatabaseWorkspaceState{Version: 1, Pane: "sql", Tabs: []WorkspaceDraft{{ID: 7, DatabaseID: row.ID, Revision: row.Revision, Target: "fixed target", SQL: "select 'draft 1'", Name: "工作窗口", Parameters: "[]"}}, ClosedTabs: []WorkspaceDraft{{ID: 3, SQL: "select 'closed draft'"}}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := svc.SaveDatabaseWorkspace(ctx, alice, state, 0); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrWorkspaceConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("concurrent creates %d/%d", success, conflicts)
	}
	loaded, err := svc.DatabaseWorkspace(ctx, alice)
	if err != nil || loaded.Revision != 1 || loaded.State.Tabs[0].SQL != "select 'draft 1'" || len(loaded.State.ClosedTabs) != 1 {
		t.Fatalf("restored %+v %v", loaded, err)
	}
	other, err := svc.DatabaseWorkspace(ctx, bob)
	if err != nil || other.Revision != 0 || len(other.State.Tabs) != 0 {
		t.Fatal("user workspace leaked")
	}
	state.Tabs[0].SQL = "select 'new draft'"
	if _, err = svc.SaveDatabaseWorkspace(ctx, alice, state, 1); err != nil {
		t.Fatal(err)
	}
	state.Tabs[0].SQL = "select 'stale overwrite'"
	if _, err = svc.SaveDatabaseWorkspace(ctx, alice, state, 1); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("stale accepted %v", err)
	}
	loaded, _ = svc.DatabaseWorkspace(ctx, alice)
	if loaded.State.Tabs[0].SQL != "select 'new draft'" {
		t.Fatal("concurrent overwrite changed SQL")
	}
	state.Tabs = append(state.Tabs, state.Tabs[0])
	if _, err = svc.SaveDatabaseWorkspace(ctx, alice, state, 2); err == nil {
		t.Fatal("duplicate IDs accepted")
	}
	state.Tabs = state.Tabs[:1]
	state.ClosedTabs[0].ID = state.Tabs[0].ID
	if _, err = svc.SaveDatabaseWorkspace(ctx, alice, state, 2); err == nil {
		t.Fatal("duplicate archived ID accepted")
	}
}
func TestSavedQueriesEnforceOwnershipAndRevision(t *testing.T) {
	svc, row, _, _ := workbenchFixture(t)
	ctx := context.Background()
	alice := fmt.Sprintf("fav-alice-%d", row.ID)
	bob := fmt.Sprintf("fav-bob-%d", row.ID)
	saved, err := svc.SaveDatabaseQuery(ctx, alice, model.DatabaseSavedQuery{Name: "客户 SQL", DatabaseID: row.ID, SQL: "select $1::text", ParametersJSON: `["值"]`})
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.SavedDatabaseQueries(ctx, bob)
	if err != nil || len(other) != 0 {
		t.Fatal("favorites leaked")
	}
	forged := *saved
	forged.Name = "stolen"
	if _, err = svc.SaveDatabaseQuery(ctx, bob, forged); err == nil {
		t.Fatal("foreign favorite modified")
	}
	if err = svc.DeleteSavedDatabaseQuery(ctx, bob, saved.ID, 1); err == nil {
		t.Fatal("foreign favorite deleted")
	}
	saved.Name = "new title"
	updated, err := svc.SaveDatabaseQuery(ctx, alice, *saved)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update %+v %v", updated, err)
	}
	if _, err = svc.SaveDatabaseQuery(ctx, alice, *saved); err == nil {
		t.Fatal("stale favorite accepted")
	}
	if err = svc.DeleteSavedDatabaseQuery(ctx, alice, saved.ID, 1); err == nil {
		t.Fatal("stale favorite delete accepted")
	}
	if err = svc.DeleteSavedDatabaseQuery(ctx, alice, saved.ID, 2); err != nil {
		t.Fatal(err)
	}
}
func TestCatalogShowsConstraintsTriggersRoutinesAndSequenceWithoutAdvancingIt(t *testing.T) {
	_, row, cfg, conn := workbenchFixture(t)
	ctx := context.Background()
	schema := fmt.Sprintf("dms_catalog_%d", row.ID)
	q := pgx.Identifier{schema}.Sanitize()
	sql := `CREATE SCHEMA ` + q + `;CREATE SEQUENCE ` + q + `.seq AS bigint START 9007199254740993;CREATE TABLE ` + q + `.parent(id int primary key);CREATE TABLE ` + q + `.child(id int primary key,parent_id int REFERENCES ` + q + `.parent(id),value int CHECK(value>=0));CREATE FUNCTION ` + q + `.touch() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$;CREATE TRIGGER touch BEFORE UPDATE ON ` + q + `.child FOR EACH ROW EXECUTE FUNCTION ` + q + `.touch();CREATE FUNCTION ` + q + `.calculate(a int) RETURNS int LANGUAGE SQL AS $$SELECT a+1$$;CREATE FUNCTION ` + q + `.calculate(a text) RETURNS text LANGUAGE SQL AS $$SELECT a$$;CREATE PROCEDURE ` + q + `.noop() LANGUAGE SQL AS $$SELECT 1$$;`
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP SCHEMA "+q+" CASCADE")
	relations, err := opsprobe.Relations(ctx, cfg, schema)
	if err != nil || len(relations) != 3 {
		t.Fatalf("catalog %v %v", relations, err)
	}
	for _, rel := range relations {
		detail, err := opsprobe.DescribeRelation(ctx, cfg, rel.OID)
		if err != nil {
			t.Fatal(err)
		}
		if rel.Name == "child" {
			if !detail.Columns[0].PrimaryKey || detail.Columns[1].PrimaryKey || len(detail.Constraints) < 3 || len(detail.Triggers) != 1 {
				t.Fatalf("object details %+v", detail)
			}
		}
		if rel.Kind == "S" {
			if detail.Sequence == nil || detail.Sequence.Start != "9007199254740993" || detail.Sequence.Last != nil {
				t.Fatalf("sequence %+v", detail.Sequence)
			}
		}
	}
	var called bool
	if err := conn.QueryRow(ctx, "SELECT is_called FROM "+q+".seq").Scan(&called); err != nil || called {
		t.Fatal("catalog advanced sequence")
	}
	routines, err := opsprobe.Routines(ctx, cfg, schema)
	if err != nil || len(routines) != 4 {
		t.Fatalf("routines %+v %v", routines, err)
	}
	for _, routine := range routines {
		detail, err := opsprobe.DescribeRoutine(ctx, cfg, routine.OID)
		if err != nil || !strings.Contains(detail.Definition, routine.Name) || detail.Owner == "" {
			t.Fatalf("routine %+v %v", detail, err)
		}
	}
}
