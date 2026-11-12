package accounts

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

func TestFirstAdminOwnsTheMainWorkspace(t *testing.T) {
	s, _ := newStore(t)
	a := admin(t, s, "ada")
	if role, err := s.Role(a.ID, workspace.Local); err != nil || role != "owner" {
		t.Fatalf("first admin's role in workspace 1: %q %v", role, err)
	}
}

// An invitation is single use, expires, can be revoked, and gives the new account exactly its role in exactly its workspace.
func TestInvitationsAreSingleUseExpireAndApplyTheirRole(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	lab, err := s.CreateWorkspace("Lab", a.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret, inv, err := s.CreateInvitation(lab.ID, "researcher", "for Bo", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, name, err := s.PeekInvitation(secret); err != nil || name != "Lab" {
		t.Fatalf("peek: %q %v", name, err)
	}
	bo, got, err := s.AcceptInvitation(secret, "Bo", "bo", "another long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != inv.ID || bo.Admin {
		t.Fatalf("accepted %+v as %+v", got, bo)
	}
	if role, err := s.Role(bo.ID, lab.ID); err != nil || role != "researcher" {
		t.Fatalf("role in Lab: %q %v", role, err)
	}
	if _, err := s.Role(bo.ID, workspace.Local); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the invitation reached another workspace: %v", err)
	}
	// reuse refused, whether by a new account or by a signed-in one
	if _, _, err := s.AcceptInvitation(secret, "Cy", "cy", "yet another passphrase"); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("reused invitation: %v", err)
	}
	if _, err := s.ByUsername("cy"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused invitation still created its account")
	}
	if _, err := s.AcceptInvitationAs(secret, a.ID); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("reused invitation by a member: %v", err)
	}
	// expired refused
	old, _, _ := s.CreateInvitation(lab.ID, "viewer", "", a.ID)
	c.add(InvitationTTL + time.Second)
	if _, _, err := s.AcceptInvitation(old, "Di", "di", "a passphrase for di"); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("expired invitation: %v", err)
	}
	// revoked refused, and another workspace cannot revoke it
	rev, rinv, _ := s.CreateInvitation(lab.ID, "viewer", "", a.ID)
	if _, err := s.RevokeInvitation(workspace.Local, rinv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked from another workspace: %v", err)
	}
	if _, err := s.RevokeInvitation(lab.ID, rinv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvitationAs(rev, a.ID); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("revoked invitation: %v", err)
	}
	if _, _, err := s.CreateInvitation(lab.ID, "admin", "", a.ID); !errors.Is(err, ErrBadRole) {
		t.Fatalf("an unknown role was accepted: %v", err)
	}
}

// Two people racing with one invitation: exactly one account joins.
func TestInvitationRaceHasOneWinner(t *testing.T) {
	s, _ := newStore(t)
	a := admin(t, s, "ada")
	secret, _, _ := s.CreateInvitation(workspace.Local, "viewer", "", a.ID)
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.AcceptInvitation(secret, "P", "p"+string(rune('a'+i)), "a long passphrase for p")
			wins <- err == nil
		}(i)
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d accounts joined with one invitation", n)
	}
}

func TestTheLastOwnerStays(t *testing.T) {
	s, _ := newStore(t)
	a := admin(t, s, "ada")
	if _, err := s.SetRole(workspace.Local, a.ID, "viewer"); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("demoting the last Owner: %v", err)
	}
	if _, err := s.RemoveMember(workspace.Local, a.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing the last Owner: %v", err)
	}
	secret, _, _ := s.CreateInvitation(workspace.Local, "owner", "", a.ID)
	bo, _, err := s.AcceptInvitation(secret, "Bo", "bo", "another long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if old, err := s.SetRole(workspace.Local, a.ID, "researcher"); err != nil || old != "owner" {
		t.Fatalf("with another Owner, demoting works: %q %v", old, err)
	}
	if _, err := s.RemoveMember(workspace.Local, bo.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("Bo is now the last Owner: %v", err)
	}
	if _, err := s.RemoveMember(workspace.Local, a.ID); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.Members(workspace.Local); len(ms) != 1 || ms[0].AccountID != bo.ID {
		t.Fatalf("members after removal: %+v", ms)
	}
	if _, err := s.SetRole(workspace.Local, "acct-nobody", "viewer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a non-member's role: %v", err)
	}
}

func TestRootsArePerWorkspace(t *testing.T) {
	s, _ := newStore(t)
	a := admin(t, s, "ada")
	lab, _ := s.CreateWorkspace("Lab", a.ID, a.ID)
	if err := s.AddRoot(lab.ID, "/srv/lab", a.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Roots(workspace.Local); len(r) != 0 {
		t.Fatalf("workspace 1 sees Lab's root: %v", r)
	}
	if r, _ := s.Roots(lab.ID); len(r) != 1 || r[0] != "/srv/lab" {
		t.Fatalf("Lab's roots: %v", r)
	}
	if err := s.RemoveRoot(workspace.Local, "/srv/lab"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed from the wrong workspace: %v", err)
	}
}
