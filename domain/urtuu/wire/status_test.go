package wire_test

import (
	"testing"
	"time"

	"github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
)

func TestTheStateMachineOnlyAllowsWhatItDeclares(t *testing.T) {
	allowed := []struct{ from, to wire.TaskStatus }{
		{wire.StatusReceived, wire.StatusAccepted},
		{wire.StatusReceived, wire.StatusReturned},
		{wire.StatusAccepted, wire.StatusDelegated},
		{wire.StatusInProgress, wire.StatusCompleted},
		{wire.StatusDelegated, wire.StatusCompleted},
		{wire.StatusCompleted, wire.StatusClosed},
		{wire.StatusReturned, wire.StatusClosed},
	}
	for _, move := range allowed {
		if !move.from.CanMoveTo(move.to) {
			t.Errorf("%s → %s was refused", move.from, move.to)
		}
	}

	refused := []struct{ from, to wire.TaskStatus }{
		// Arriving is not accepting: a task cannot be worked on before somebody
		// here has taken it.
		{wire.StatusReceived, wire.StatusInProgress},
		{wire.StatusReceived, wire.StatusCompleted},
		// Only the originator closes, and only after an outcome.
		{wire.StatusReceived, wire.StatusClosed},
		{wire.StatusAccepted, wire.StatusClosed},
		// Closed is the end of it.
		{wire.StatusClosed, wire.StatusAccepted},
		{wire.StatusClosed, wire.StatusReturned},
		// A completed task is not reopened; a new one is raised.
		{wire.StatusCompleted, wire.StatusInProgress},
		{wire.StatusReturned, wire.StatusAccepted},
	}
	for _, move := range refused {
		if move.from.CanMoveTo(move.to) {
			t.Errorf("%s → %s was allowed", move.from, move.to)
		}
	}
}

func TestOnlyClosedIsFinal(t *testing.T) {
	for _, status := range []wire.TaskStatus{
		wire.StatusReceived, wire.StatusAccepted, wire.StatusInProgress,
		wire.StatusDelegated, wire.StatusCompleted, wire.StatusReturned,
	} {
		if status.Final() {
			t.Errorf("%s reports itself final; nothing would ever close it", status)
		}
	}
	if !wire.StatusClosed.Final() {
		t.Error("CLOSED is not final")
	}
}

func TestKnownStatusRefusesAnythingInvented(t *testing.T) {
	if wire.KnownStatus("DONE") {
		t.Error("an invented status was accepted; a query filter could then match nothing silently")
	}
	if !wire.KnownStatus(wire.StatusInProgress) {
		t.Error("a real status was refused")
	}
}

func TestOverdueIsAFlagAndNotAState(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	if !wire.Overdue(wire.StatusInProgress, &past, now) {
		t.Error("work in progress past its deadline is not flagged late")
	}
	if wire.Overdue(wire.StatusInProgress, &future, now) {
		t.Error("work inside its deadline is flagged late")
	}
	if wire.Overdue(wire.StatusInProgress, nil, now) {
		t.Error("a task with no deadline is flagged late")
	}
	// Finishing late is still late. Only the originator accepting the outcome
	// settles it.
	if !wire.Overdue(wire.StatusCompleted, &past, now) {
		t.Error("a task finished after its deadline stopped being late by being finished")
	}
	if wire.Overdue(wire.StatusClosed, &past, now) {
		t.Error("a closed task is still reported late")
	}
}

func TestRequestCodeNamesFallBackRatherThanBlank(t *testing.T) {
	code := wire.RequestCode{
		Code:  "D-101",
		Names: map[string]string{"mn": "Хагас жилийн тооллого", "en": "Half-year count"},
	}
	if got := code.LocalizedName("en"); got != "Half-year count" {
		t.Errorf("en = %q", got)
	}
	// Not yet translated into Arabic: Mongolian is the source language of the
	// register, so it is what an untranslated code is shown as.
	if got := code.LocalizedName("ar"); got != "Хагас жилийн тооллого" {
		t.Errorf("ar = %q, want the Mongolian source", got)
	}
	if got := (wire.RequestCode{Code: "local.audit"}).LocalizedName("mn"); got != "local.audit" {
		t.Errorf("unlabelled = %q, want the code itself", got)
	}
}

func TestKnownSourceIsClosed(t *testing.T) {
	for _, source := range []string{wire.SourceRing, wire.SourceLink, wire.SourceLocal} {
		if !wire.KnownSource(source) {
			t.Errorf("%s was refused", source)
		}
	}
	if wire.KnownSource("manual") {
		t.Error("an invented source was accepted")
	}
}
