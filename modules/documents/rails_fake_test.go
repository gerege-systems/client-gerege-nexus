/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The identity rails, as this module sees them.
//
// These were internal/platform/eid and internal/platform/dan, in mock mode.
// Both are under internal/, so neither could come here with the module — which
// was, in the end, one of the two things that kept it in the platform's
// repository.
//
// The fakes behave the way the mock rails did, because that is what these tests
// were written against: eID answers a start with a session and a poll with a
// completed identity, and ДАН answers a citizen when it is reachable. What they
// do not do is dial anything, which the mocks did not either.
//
// The rails' own behaviour — a real eID handshake, a real certificate — is the
// platform's and is tested there. What is tested here is what this module does
// with the answers.

type fakeEID struct {
	mu       sync.Mutex
	sessions map[string]fakeCeremony
	started  int
}

// fakeCeremony is a push the fake is holding: who it went to, and when. Both
// matter — see Poll.
type fakeCeremony struct {
	nationalID string
	pushedAt   time.Time
}

// approvalTakes is how long the mock rail made a citizen take to answer, and so
// how long these tests were written to wait. The number is not arbitrary: the
// suite's own poll helper loops on RUNNING, and TestPollDoesNotWriteOnACancelled
// Context sleeps 1800ms to get past it.
const approvalTakes = 1500 * time.Millisecond

func newFakeEID() *fakeEID { return &fakeEID{sessions: map[string]fakeCeremony{}} }

func (f *fakeEID) Mode() string { return "mock" }

func (f *fakeEID) StartSignature(_ context.Context, nationalID, _, _ string) (*nexus.SignatureCeremony, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	id := "eid-session-" + strconv.Itoa(f.started) + "-" + nationalID
	f.sessions[id] = fakeCeremony{nationalID: nationalID, pushedAt: time.Now()}
	return &nexus.SignatureCeremony{
		SessionID:        id,
		DeviceLinkURL:    "https://eid.example.invalid/" + id,
		VerificationCode: "4242",
		ExpiresAt:        time.Now().Add(5 * time.Minute).Format(time.RFC3339),
	}, nil
}

// Poll answers RUNNING for the first second and a half, then COMPLETE — the
// mock rail's own timing.
//
// Answering COMPLETE straight away would be the simpler fake and a wrong one.
// A ceremony that finishes before the caller has polled once means the first
// poll consumes the session, and TestASessionWithNoStatedDeadlineIsNotExpired
// Early — which polls, ages the row past the backstop, and polls again — gets
// COMPLETE the second time because there is no unspent row left to expire. The
// test found this. The citizen has to still be deciding.
func (f *fakeEID) Poll(_ context.Context, sessionID string) (*nexus.CeremonyState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ceremony, started := f.sessions[sessionID]
	if !started {
		return &nexus.CeremonyState{State: "EXPIRED"}, nil
	}
	if time.Since(ceremony.pushedAt) < approvalTakes {
		return &nexus.CeremonyState{State: "RUNNING"}, nil
	}
	// COMPLETE, not "completed": the word is the rail's, defined in pkg/eid. A
	// fake that invented its own spelling would pass its own assertions and
	// fail every one the module makes.
	//
	// The citizen who approves is the one the request was pushed to. That is
	// the property the module checks after every poll — a signature approved by
	// somebody other than the person asked is refused — so a fake answering
	// with the same person every time would make that check untestable and
	// every chain of two signers fail.
	return &nexus.CeremonyState{
		State: "COMPLETE",
		Identity: &nexus.SignerIdentity{
			RegNumber: ceremony.nationalID, FirstName: "Бат", LastName: "Дорж",
			CertificateSerial: "01AB23CD-" + ceremony.nationalID,
			CertificateIssuer: "CN=eID Mongolia Test CA",
		},
	}, nil
}

// fakeDAN is reachable or it is not. The distinction is the whole subject of
// TestAnUnreachableDANIsProviderTroubleNotARejection: a channel that is not
// there and a citizen who was refused are opposite answers, and this platform
// once reported the first as the second.
type fakeDAN struct{ reachable bool }

func (f fakeDAN) Mode() string {
	if f.reachable {
		return "mock"
	}
	return "live"
}

func (f fakeDAN) AuthenticateCitizen(_ context.Context, regNumber, _ string) (*nexus.DANCitizen, error) {
	if !f.reachable {
		return nil, nexus.ErrIdentityRailUnavailable
	}
	return &nexus.DANCitizen{
		SessionID: "dan-session", RegNumber: regNumber,
		CivilID: "МН" + regNumber, LastName: "Дорж", FirstName: "Бат",
		MobileNumber: "99001122", Email: "bat@example.invalid",
		VerifiedAt: time.Now(),
	}, nil
}
