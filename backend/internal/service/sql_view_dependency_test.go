package service

import (
	"strings"
	"testing"
)

func TestBuildViewRebuildPlanSQL(t *testing.T) {
	plan := BuildViewRebuildPlan(ViewDependency{
		Schema:     "public",
		View:       "v_patient",
		Definition: " SELECT patient.id FROM patient",
	})

	if !strings.Contains(plan.DropSQL, `DROP VIEW IF EXISTS "public"."v_patient";`) {
		t.Fatalf("unexpected drop sql: %s", plan.DropSQL)
	}
	if !strings.Contains(plan.CreateSQL, `CREATE OR REPLACE VIEW "public"."v_patient" AS`) {
		t.Fatalf("unexpected create sql: %s", plan.CreateSQL)
	}
	if !strings.Contains(plan.CreateSQL, `SELECT patient.id FROM patient`) {
		t.Fatalf("unexpected create sql definition: %s", plan.CreateSQL)
	}
}
